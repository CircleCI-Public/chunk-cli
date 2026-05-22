package validate

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func writeConfig(t *testing.T, dir string, commands []config.Command) string {
	t.Helper()
	chunkDir := filepath.Join(dir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	cfg := config.ProjectConfig{Commands: commands}
	data, err := json.Marshal(cfg)
	assert.NilError(t, err)
	path := filepath.Join(chunkDir, "config.json")
	assert.NilError(t, os.WriteFile(path, data, 0o644))
	return path
}

func newStreams() (iostream.Streams, *bytes.Buffer, *bytes.Buffer) {
	var out, errBuf bytes.Buffer
	return iostream.Streams{Out: &out, Err: &errBuf}, &out, &errBuf
}

// --- LoadProjectConfig tests ---

func TestLoadProjectConfig(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, []config.Command{
			{Name: "install", Run: "npm install"},
			{Name: "test", Run: "npm test"},
		})

		cfg, err := config.LoadProjectConfig(dir)
		assert.NilError(t, err)
		assert.Equal(t, len(cfg.Commands), 2)
		assert.Equal(t, cfg.Commands[0].Name, "install")
		assert.Equal(t, cfg.Commands[0].Run, "npm install")
		assert.Equal(t, cfg.Commands[1].Name, "test")
		assert.Equal(t, cfg.Commands[1].Run, "npm test")
	})

	t.Run("empty commands", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, []config.Command{})

		cfg, err := config.LoadProjectConfig(dir)
		assert.NilError(t, err)
		assert.Equal(t, len(cfg.Commands), 0)
	})

	t.Run("missing file", func(t *testing.T) {
		dir := t.TempDir()
		_, err := config.LoadProjectConfig(dir)
		assert.ErrorContains(t, err, "could not read config.json")
	})

	t.Run("invalid json", func(t *testing.T) {
		dir := t.TempDir()
		chunkDir := filepath.Join(dir, ".chunk")
		assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
		assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte("{bad"), 0o644))

		_, err := config.LoadProjectConfig(dir)
		assert.ErrorContains(t, err, "parse config.json")
	})
}

// --- HasCommands / FindCommand tests ---

func TestHasCommands(t *testing.T) {
	empty := &config.ProjectConfig{}
	assert.Assert(t, !empty.HasCommands())

	withCmd := &config.ProjectConfig{Commands: []config.Command{{Name: "test", Run: "go test"}}}
	assert.Assert(t, withCmd.HasCommands())
}

func TestFindCommand(t *testing.T) {
	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "install", Run: "npm install"},
		{Name: "test", Run: "npm test"},
	}}

	found := cfg.FindCommand("test")
	assert.Assert(t, found != nil, "expected to find 'test' command")
	assert.Equal(t, found.Run, "npm test")
	assert.Assert(t, cfg.FindCommand("nonexistent") == nil)
}

// --- Config with FileExt / Timeout tests ---

func TestCommandFileExtRoundTrip(t *testing.T) {
	dir := t.TempDir()
	chunkDir := filepath.Join(dir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))

	raw := `{"commands":[{"name":"lint","run":"eslint .","fileExt":".ts","timeout":60}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(raw), 0o644))

	cfg, err := config.LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Equal(t, len(cfg.Commands), 1)

	c := cfg.Commands[0]
	assert.Equal(t, c.FileExt, ".ts")
	assert.Equal(t, c.Timeout, 60)

	// Save and reload to verify round-trip
	assert.NilError(t, config.SaveProjectConfig(dir, cfg))
	cfg2, err := config.LoadProjectConfig(dir)
	assert.NilError(t, err)
	assert.Equal(t, cfg2.Commands[0].FileExt, ".ts")
}

func TestCommandFileExtOmitted(t *testing.T) {
	dir := t.TempDir()

	cfg := &config.ProjectConfig{Commands: []config.Command{
		{Name: "test", Run: "go test ./..."},
	}}
	assert.NilError(t, config.SaveProjectConfig(dir, cfg))

	data, err := os.ReadFile(filepath.Join(dir, ".chunk", "config.json"))
	assert.NilError(t, err)
	// fileExt and timeout should not appear when empty/zero
	assert.Assert(t, !strings.Contains(string(data), "fileExt"), "expected fileExt to be omitted, got: %s", data)
	assert.Assert(t, !strings.Contains(string(data), "timeout"), "expected timeout to be omitted, got: %s", data)
}

// --- RemoteExecutor integration with real SSH server ---

func TestRemoteExecutor_SSHIntegration(t *testing.T) {
	newCCIClient := func(t *testing.T, serverURL string) *circleci.Client {
		t.Helper()
		client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: serverURL})
		assert.NilError(t, err)
		return client
	}

	execCallback := func(t *testing.T, session *sidecar.Session) func(context.Context, string) (string, string, int, error) {
		t.Helper()
		return func(ctx context.Context, script string) (string, string, int, error) {
			result, err := sidecar.ExecOverSSH(ctx, session, "sh -c "+sidecar.ShellEscape(script), nil, nil)
			if err != nil {
				return "", "", 0, err
			}
			return result.Stdout, result.Stderr, result.ExitCode, nil
		}
	}

	t.Run("success", func(t *testing.T) {
		keyFile, pubKey := fakes.GenerateSSHKeypair(t)
		sshSrv := fakes.NewSSHServer(t, pubKey)
		sshSrv.SetResult("hello from remote\n", 0)

		cci := fakes.NewFakeCircleCI()
		cci.AddKeyURL = sshSrv.Addr()
		cciSrv := httptest.NewServer(cci)
		defer cciSrv.Close()

		t.Setenv(config.EnvHome, t.TempDir())
		client := newCCIClient(t, cciSrv.URL)
		session, err := sidecar.OpenSession(context.Background(), client, "sidecar-123", keyFile, "")
		assert.NilError(t, err)

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "echo hello"},
		}}
		streams, out, _ := newStreams()
		var statusBuf bytes.Buffer

		exec := NewRemoteExecutor(execCallback(t, session), "/workspace/repo", streams)
		runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
		assert.NilError(t, runner.RunAll(context.Background()))
		assert.Assert(t, strings.Contains(out.String(), "hello from remote"), "got: %s", out.String())
		assert.Equal(t, len(sshSrv.Commands()), 1)
	})

	t.Run("non-zero exit code", func(t *testing.T) {
		keyFile, pubKey := fakes.GenerateSSHKeypair(t)
		sshSrv := fakes.NewSSHServer(t, pubKey)
		sshSrv.SetResult("", 1)

		cci := fakes.NewFakeCircleCI()
		cci.AddKeyURL = sshSrv.Addr()
		cciSrv := httptest.NewServer(cci)
		defer cciSrv.Close()

		t.Setenv(config.EnvHome, t.TempDir())
		client := newCCIClient(t, cciSrv.URL)
		session, err := sidecar.OpenSession(context.Background(), client, "sidecar-123", keyFile, "")
		assert.NilError(t, err)

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "false"},
		}}
		streams, _, _ := newStreams()
		var statusBuf bytes.Buffer

		exec := NewRemoteExecutor(execCallback(t, session), "/workspace/repo", streams)
		runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
		err = runner.RunAll(context.Background())
		assert.ErrorContains(t, err, "remote test failed")
	})

	t.Run("multiple commands stop on first failure", func(t *testing.T) {
		keyFile, pubKey := fakes.GenerateSSHKeypair(t)
		sshSrv := fakes.NewSSHServer(t, pubKey)
		sshSrv.SetResult("", 1)

		cci := fakes.NewFakeCircleCI()
		cci.AddKeyURL = sshSrv.Addr()
		cciSrv := httptest.NewServer(cci)
		defer cciSrv.Close()

		t.Setenv(config.EnvHome, t.TempDir())
		client := newCCIClient(t, cciSrv.URL)
		session, err := sidecar.OpenSession(context.Background(), client, "sidecar-123", keyFile, "")
		assert.NilError(t, err)

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "install", Run: "npm install"},
			{Name: "test", Run: "npm test"},
		}}
		streams, _, _ := newStreams()
		var statusBuf bytes.Buffer

		exec := NewRemoteExecutor(execCallback(t, session), "/workspace/repo", streams)
		runner := NewRunner(cfg, exec, testStatus(&statusBuf), streams)
		err = runner.RunAll(context.Background())
		assert.ErrorContains(t, err, "remote install failed")
		assert.Equal(t, len(sshSrv.Commands()), 1)
	})
}
