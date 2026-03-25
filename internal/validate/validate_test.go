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

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func writeConfig(t *testing.T, dir string, commands []Command) string {
	t.Helper()
	chunkDir := filepath.Join(dir, ".chunk")
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := ProjectConfig{Commands: commands}
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(chunkDir, "config.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
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
		writeConfig(t, dir, []Command{
			{Name: "install", Run: "npm install"},
			{Name: "test", Run: "npm test"},
		})

		cfg, err := LoadProjectConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Commands) != 2 {
			t.Fatalf("expected 2 commands, got %d", len(cfg.Commands))
		}
		if cfg.Commands[0].Name != "install" || cfg.Commands[0].Run != "npm install" {
			t.Errorf("unexpected first command: %+v", cfg.Commands[0])
		}
		if cfg.Commands[1].Name != "test" || cfg.Commands[1].Run != "npm test" {
			t.Errorf("unexpected second command: %+v", cfg.Commands[1])
		}
	})

	t.Run("empty commands", func(t *testing.T) {
		dir := t.TempDir()
		writeConfig(t, dir, []Command{})

		cfg, err := LoadProjectConfig(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(cfg.Commands) != 0 {
			t.Fatalf("expected 0 commands, got %d", len(cfg.Commands))
		}
	})

	t.Run("missing file", func(t *testing.T) {
		dir := t.TempDir()
		_, err := LoadProjectConfig(dir)
		if err == nil {
			t.Fatal("expected error for missing config")
		}
		if !strings.Contains(err.Error(), "could not read config.json") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("invalid json", func(t *testing.T) {
		dir := t.TempDir()
		chunkDir := filepath.Join(dir, ".chunk")
		if err := os.MkdirAll(chunkDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte("{bad"), 0o644); err != nil {
			t.Fatal(err)
		}

		_, err := LoadProjectConfig(dir)
		if err == nil {
			t.Fatal("expected error for invalid json")
		}
		if !strings.Contains(err.Error(), "parse config.json") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// --- HasCommands / FindCommand tests ---

func TestHasCommands(t *testing.T) {
	empty := &ProjectConfig{}
	if empty.HasCommands() {
		t.Error("expected HasCommands() == false for empty config")
	}

	withCmd := &ProjectConfig{Commands: []Command{{Name: "test", Run: "go test"}}}
	if !withCmd.HasCommands() {
		t.Error("expected HasCommands() == true")
	}
}

func TestFindCommand(t *testing.T) {
	cfg := &ProjectConfig{Commands: []Command{
		{Name: "install", Run: "npm install"},
		{Name: "test", Run: "npm test"},
	}}

	found := cfg.FindCommand("test")
	if found == nil {
		t.Fatal("expected to find 'test' command")
	}
	if found.Run != "npm test" {
		t.Errorf("expected 'npm test', got %q", found.Run)
	}

	if cfg.FindCommand("nonexistent") != nil {
		t.Error("expected nil for nonexistent command")
	}
}

// --- RunDryRun tests ---

func TestRunDryRun(t *testing.T) {
	t.Run("prints commands", func(t *testing.T) {
		cfg := &ProjectConfig{Commands: []Command{
			{Name: "install", Run: "npm install"},
			{Name: "test", Run: "npm test"},
		}}
		streams, out, _ := newStreams()

		if err := RunDryRun(cfg, "", streams); err != nil {
			t.Fatal(err)
		}

		output := out.String()
		if !strings.Contains(output, "install: npm install") {
			t.Errorf("expected install command in output, got: %s", output)
		}
		if !strings.Contains(output, "test: npm test") {
			t.Errorf("expected test command in output, got: %s", output)
		}
	})

	t.Run("no commands", func(t *testing.T) {
		cfg := &ProjectConfig{}
		streams, _, _ := newStreams()

		err := RunDryRun(cfg, "", streams)
		if err == nil {
			t.Fatal("expected error for empty config")
		}
		if !strings.Contains(err.Error(), "no validate commands") {
			t.Errorf("unexpected error: %v", err)
		}
	})
}

// --- RunAll tests ---

func TestRunAll(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		cfg := &ProjectConfig{Commands: []Command{
			{Name: "install", Run: "echo installed"},
			{Name: "test", Run: "echo tested"},
		}}
		streams, out, errBuf := newStreams()

		if err := RunAll(context.Background(), ".", true, cfg, streams); err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(out.String(), "installed") {
			t.Errorf("expected 'installed' in stdout, got: %s", out.String())
		}
		if !strings.Contains(out.String(), "tested") {
			t.Errorf("expected 'tested' in stdout, got: %s", out.String())
		}
		if !strings.Contains(errBuf.String(), "Running install") {
			t.Errorf("expected status on stderr, got: %s", errBuf.String())
		}
	})

	t.Run("no commands", func(t *testing.T) {
		cfg := &ProjectConfig{}
		streams, _, _ := newStreams()

		err := RunAll(context.Background(), ".", true, cfg, streams)
		if err == nil {
			t.Fatal("expected error for empty config")
		}
		if !strings.Contains(err.Error(), "no validate commands") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("command failure", func(t *testing.T) {
		cfg := &ProjectConfig{Commands: []Command{
			{Name: "test", Run: "false"},
		}}
		streams, _, _ := newStreams()

		err := RunAll(context.Background(), ".", true, cfg, streams)
		if err == nil {
			t.Fatal("expected error for failing command")
		}
		if !strings.Contains(err.Error(), "test command failed") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("skips remaining after failure", func(t *testing.T) {
		cfg := &ProjectConfig{Commands: []Command{
			{Name: "install", Run: "false"},
			{Name: "test", Run: "echo should-not-run"},
			{Name: "lint", Run: "echo should-not-run-either"},
		}}
		streams, out, errBuf := newStreams()

		err := RunAll(context.Background(), ".", true, cfg, streams)
		if err == nil {
			t.Fatal("expected error")
		}

		// The skipped commands should not produce stdout
		if strings.Contains(out.String(), "should-not-run") {
			t.Errorf("skipped command should not produce output, got: %s", out.String())
		}

		// Both remaining commands should be reported as skipped
		stderr := errBuf.String()
		if !strings.Contains(stderr, "test: skipped") {
			t.Errorf("expected 'test: skipped' in stderr, got: %s", stderr)
		}
		if !strings.Contains(stderr, "lint: skipped") {
			t.Errorf("expected 'lint: skipped' in stderr, got: %s", stderr)
		}
	})

	t.Run("single command success", func(t *testing.T) {
		cfg := &ProjectConfig{Commands: []Command{
			{Name: "test", Run: "echo ok"},
		}}
		streams, out, _ := newStreams()

		if err := RunAll(context.Background(), ".", true, cfg, streams); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "ok") {
			t.Errorf("expected 'ok' in output, got: %s", out.String())
		}
	})
}

// --- RunRemote tests ---

func newFakeClient(t *testing.T, cci *fakes.FakeCircleCI) (*circleci.Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	t.Setenv("CIRCLE_TOKEN", "fake-token")
	t.Setenv("CIRCLECI_BASE_URL", srv.URL)

	client, err := circleci.NewClient()
	if err != nil {
		t.Fatal(err)
	}
	return client, srv
}

func TestRunRemote(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		cci.ExecResponse = &fakes.ExecResponse{
			CommandID: "cmd-1",
			PID:       1,
			Stdout:    "remote output\n",
			ExitCode:  0,
		}
		client, _ := newFakeClient(t, cci)

		cfg := &ProjectConfig{Commands: []Command{
			{Name: "install", Run: "echo install"},
			{Name: "test", Run: "echo test"},
		}}
		streams, out, _ := newStreams()

		err := RunRemote(context.Background(), client, cfg, "sandbox-1", "org-1", streams)
		if err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(out.String(), "remote output") {
			t.Errorf("expected remote output in stdout, got: %s", out.String())
		}

		// Verify both commands were executed
		reqs := cci.Recorder.AllRequests()
		var execCount int
		for _, r := range reqs {
			if strings.HasPrefix(r.URL.Path, "/api/v2/sandbox/instances/") && strings.HasSuffix(r.URL.Path, "/exec") {
				execCount++
			}
		}
		if execCount != 2 {
			t.Errorf("expected 2 exec requests, got %d", execCount)
		}
	})

	t.Run("non-zero exit code", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		cci.ExecResponse = &fakes.ExecResponse{
			CommandID: "cmd-1",
			PID:       1,
			ExitCode:  1,
		}
		client, _ := newFakeClient(t, cci)

		cfg := &ProjectConfig{Commands: []Command{
			{Name: "test", Run: "failing"},
		}}
		streams, _, _ := newStreams()

		err := RunRemote(context.Background(), client, cfg, "sandbox-1", "org-1", streams)
		if err == nil {
			t.Fatal("expected error for non-zero exit code")
		}
		if !strings.Contains(err.Error(), "remote test failed") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty stdout not written", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		cci.ExecResponse = &fakes.ExecResponse{
			CommandID: "cmd-1",
			PID:       1,
			Stdout:    "",
			ExitCode:  0,
		}
		client, _ := newFakeClient(t, cci)

		cfg := &ProjectConfig{Commands: []Command{
			{Name: "test", Run: "silent"},
		}}
		streams, out, _ := newStreams()

		err := RunRemote(context.Background(), client, cfg, "sandbox-1", "org-1", streams)
		if err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 {
			t.Errorf("expected no stdout output, got: %s", out.String())
		}
	})
}
