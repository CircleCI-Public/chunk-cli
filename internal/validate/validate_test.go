package validate

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sandbox"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestShellEscape(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"plain path", "/workspace/src", "'/workspace/src'"},
		{"single quote", "it's", "'it'\\''s'"},
		{"multiple single quotes", "a'b'c", "'a'\\''b'\\''c'"},
		{"dollar sign", "$HOME", "'$HOME'"},
		{"newline", "foo\nbar", "'foo\nbar'"},
		{"backtick", "`cmd`", "'`cmd`'"},
		{"backslash", `foo\bar`, `'foo\bar'`},
		{"spaces", "hello world", "'hello world'"},
		{"empty string", "", "''"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellEscape(tt.input)
			assert.Equal(t, got, tt.want)
		})
	}
}

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

// --- RunDryRun tests ---

func TestRunDryRun(t *testing.T) {
	t.Run("prints commands", func(t *testing.T) {
		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "install", Run: "npm install"},
			{Name: "test", Run: "npm test"},
		}}
		streams, out, _ := newStreams()

		assert.NilError(t, RunDryRun(cfg, "", streams))

		assert.Assert(t, strings.Contains(out.String(), "install: npm install"), "got: %s", out.String())
		assert.Assert(t, strings.Contains(out.String(), "test: npm test"), "got: %s", out.String())
	})

	t.Run("no commands", func(t *testing.T) {
		cfg := &config.ProjectConfig{}
		streams, _, _ := newStreams()

		err := RunDryRun(cfg, "", streams)
		assert.ErrorContains(t, err, "no validate commands")
	})
}

// --- RunAll tests ---

func TestRunAll(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "install", Run: "echo installed"},
			{Name: "test", Run: "echo tested"},
		}}
		streams, _, errBuf := newStreams()

		assert.NilError(t, RunAll(context.Background(), ".", cfg, streams))
		assert.Assert(t, strings.Contains(errBuf.String(), "installed"), "got: %s", errBuf.String())
		assert.Assert(t, strings.Contains(errBuf.String(), "tested"), "got: %s", errBuf.String())
		assert.Assert(t, strings.Contains(errBuf.String(), "Running install"), "got: %s", errBuf.String())
	})

	t.Run("no commands", func(t *testing.T) {
		cfg := &config.ProjectConfig{}
		streams, _, _ := newStreams()

		err := RunAll(context.Background(), ".", cfg, streams)
		assert.ErrorContains(t, err, "no validate commands")
	})

	t.Run("command failure", func(t *testing.T) {
		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "false"},
		}}
		streams, _, _ := newStreams()

		err := RunAll(context.Background(), ".", cfg, streams)
		assert.ErrorContains(t, err, "test command failed")
	})

	t.Run("skips remaining after failure", func(t *testing.T) {
		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "install", Run: "false"},
			{Name: "test", Run: "echo should-not-run"},
			{Name: "lint", Run: "echo should-not-run-either"},
		}}
		streams, _, errBuf := newStreams()

		err := RunAll(context.Background(), ".", cfg, streams)
		assert.Assert(t, err != nil, "expected error")
		assert.Assert(t, strings.Contains(errBuf.String(), "test: skipped"), "got: %s", errBuf.String())
		assert.Assert(t, strings.Contains(errBuf.String(), "lint: skipped"), "got: %s", errBuf.String())
	})

	t.Run("single command success", func(t *testing.T) {
		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "echo ok"},
		}}
		streams, _, errBuf := newStreams()

		assert.NilError(t, RunAll(context.Background(), ".", cfg, streams))
		assert.Assert(t, strings.Contains(errBuf.String(), "ok"), "got: %s", errBuf.String())
	})
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
	assert.Assert(t, !strings.Contains(string(data), "fileExt"), "expected fileExt to be omitted, got: %s", data)
	assert.Assert(t, !strings.Contains(string(data), "timeout"), "expected timeout to be omitted, got: %s", data)
}

// --- RunRemote tests ---

func TestRunRemote(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var execCount int
		execFn := func(_ context.Context, _ string) (string, string, int, error) {
			execCount++
			return "remote output\n", "", 0, nil
		}

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "install", Run: "echo install"},
			{Name: "test", Run: "echo test"},
		}}
		streams, out, _ := newStreams()

		assert.NilError(t, RunRemote(context.Background(), execFn, cfg, "/workspace", streams))
		assert.Assert(t, strings.Contains(out.String(), "remote output"), "got: %s", out.String())
		assert.Equal(t, execCount, 2)
	})

	t.Run("non-zero exit code", func(t *testing.T) {
		execFn := func(_ context.Context, _ string) (string, string, int, error) {
			return "", "", 1, nil
		}

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "failing"},
		}}
		streams, _, _ := newStreams()

		err := RunRemote(context.Background(), execFn, cfg, "/workspace", streams)
		assert.ErrorContains(t, err, "remote test failed")
	})

	t.Run("empty stdout not written", func(t *testing.T) {
		execFn := func(_ context.Context, _ string) (string, string, int, error) {
			return "", "", 0, nil
		}

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "silent"},
		}}
		streams, out, _ := newStreams()

		assert.NilError(t, RunRemote(context.Background(), execFn, cfg, "/workspace", streams))
		assert.Equal(t, out.Len(), 0)
	})

	t.Run("script uses dest directory", func(t *testing.T) {
		var capturedScript string
		execFn := func(_ context.Context, script string) (string, string, int, error) {
			capturedScript = script
			return "", "", 0, nil
		}

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "go test ./..."},
		}}
		streams, _, _ := newStreams()

		assert.NilError(t, RunRemote(context.Background(), execFn, cfg, "/custom/path", streams))
		assert.Assert(t, strings.HasPrefix(capturedScript, "cd '/custom/path' &&"), "got: %s", capturedScript)
	})
}

// TestRunRemoteSSH tests RunRemote end-to-end with a real fake SSH server,
// verifying the exec callback correctly passes stdout/stderr/exitCode through.
func TestRunRemoteSSH(t *testing.T) {
	newCCIClient := func(t *testing.T, serverURL string) *circleci.Client {
		t.Helper()
		t.Setenv("CIRCLECI_BASE_URL", serverURL)
		t.Setenv("CIRCLE_TOKEN", "test-token")
		client, err := circleci.NewClient()
		assert.NilError(t, err)
		return client
	}

	execCallback := func(t *testing.T, session *sandbox.Session) func(context.Context, string) (string, string, int, error) {
		t.Helper()
		return func(ctx context.Context, script string) (string, string, int, error) {
			result, err := sandbox.ExecOverSSH(ctx, session, "sh -c "+sandbox.ShellEscape(script), nil, nil)
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

		t.Setenv("HOME", t.TempDir())
		client := newCCIClient(t, cciSrv.URL)
		session, err := sandbox.OpenSession(context.Background(), client, "sandbox-123", keyFile, "")
		assert.NilError(t, err)

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "echo hello"},
		}}
		streams, out, _ := newStreams()

		assert.NilError(t, RunRemote(context.Background(), execCallback(t, session), cfg, "/workspace/repo", streams))
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

		t.Setenv("HOME", t.TempDir())
		client := newCCIClient(t, cciSrv.URL)
		session, err := sandbox.OpenSession(context.Background(), client, "sandbox-123", keyFile, "")
		assert.NilError(t, err)

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "false"},
		}}
		streams, _, _ := newStreams()

		err = RunRemote(context.Background(), execCallback(t, session), cfg, "/workspace/repo", streams)
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

		t.Setenv("HOME", t.TempDir())
		client := newCCIClient(t, cciSrv.URL)
		session, err := sandbox.OpenSession(context.Background(), client, "sandbox-123", keyFile, "")
		assert.NilError(t, err)

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "install", Run: "npm install"},
			{Name: "test", Run: "npm test"},
		}}
		streams, _, _ := newStreams()

		err = RunRemote(context.Background(), execCallback(t, session), cfg, "/workspace/repo", streams)
		assert.ErrorContains(t, err, "remote install failed")
		assert.Equal(t, len(sshSrv.Commands()), 1)
	})
}

// --- HasUncommittedChanges tests ---

func TestHasUncommittedChanges(t *testing.T) {
	t.Run("clean repo returns false", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		changed, err := HasUncommittedChanges(dir)
		assert.NilError(t, err)
		assert.Assert(t, !changed, "expected no changes on a clean repo")
	})

	t.Run("modified tracked file returns true", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)
		assert.NilError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("modified"), 0o644))

		changed, err := HasUncommittedChanges(dir)
		assert.NilError(t, err)
		assert.Assert(t, changed, "expected changes after modifying a tracked file")
	})

	t.Run("staged file returns true", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)
		newFile := filepath.Join(dir, "new.go")
		assert.NilError(t, os.WriteFile(newFile, []byte("package main"), 0o644))
		cmd := exec.Command("git", "add", "new.go")
		cmd.Dir = dir
		assert.NilError(t, cmd.Run())

		changed, err := HasUncommittedChanges(dir)
		assert.NilError(t, err)
		assert.Assert(t, changed, "expected changes after staging a new file")
	})

	t.Run("non-git dir returns false without error", func(t *testing.T) {
		dir := t.TempDir()

		changed, err := HasUncommittedChanges(dir)
		assert.NilError(t, err)
		assert.Assert(t, !changed, "expected false for non-git directory")
	})
}

// --- Attempt tracking tests ---

func TestTrackFailedAttempt(t *testing.T) {
	t.Run("increments on same content hash", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		n1 := TrackFailedAttempt(dir)
		assert.Equal(t, n1, 1)

		n2 := TrackFailedAttempt(dir)
		assert.Equal(t, n2, 2)

		n3 := TrackFailedAttempt(dir)
		assert.Equal(t, n3, 3)
	})

	t.Run("resets to 1 when content changes", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		n1 := TrackFailedAttempt(dir)
		assert.Equal(t, n1, 1)

		n2 := TrackFailedAttempt(dir)
		assert.Equal(t, n2, 2)

		// Modify a tracked file so the content hash changes.
		assert.NilError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed"), 0o644))

		n3 := TrackFailedAttempt(dir)
		assert.Equal(t, n3, 1, "expected reset to 1 after content change")
	})

	t.Run("starts at 1 with no prior state", func(t *testing.T) {
		dir := t.TempDir()
		initGitRepo(t, dir)

		n := TrackFailedAttempt(dir)
		assert.Equal(t, n, 1)
	})
}

func TestResetAttempts(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	TrackFailedAttempt(dir)
	TrackFailedAttempt(dir)

	ResetAttempts(dir)

	n := TrackFailedAttempt(dir)
	assert.Equal(t, n, 1, "expected attempt count to restart after reset")
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
		{"git", "config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %s: %v", args, out, err)
		}
	}
	// Create an initial commit so HEAD exists
	readme := filepath.Join(dir, "README.md")
	assert.NilError(t, os.WriteFile(readme, []byte("test"), 0o644))
	for _, args := range [][]string{
		{"git", "add", "."},
		{"git", "commit", "-m", "init"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %s: %v", args, out, err)
		}
	}
}
