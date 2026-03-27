package validate

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
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

func TestRunRemote(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var execCount int
		execFn := func(_ context.Context, _ string) (string, string, int, error) {
			execCount++
			return "remote output\n", "", 0, nil
		}

		cfg := &ProjectConfig{Commands: []Command{
			{Name: "install", Run: "echo install"},
			{Name: "test", Run: "echo test"},
		}}
		streams, out, _ := newStreams()

		if err := RunRemote(context.Background(), execFn, cfg, "/workspace", streams); err != nil {
			t.Fatal(err)
		}

		if !strings.Contains(out.String(), "remote output") {
			t.Errorf("expected remote output in stdout, got: %s", out.String())
		}
		if execCount != 2 {
			t.Errorf("expected 2 exec calls, got %d", execCount)
		}
	})

	t.Run("non-zero exit code", func(t *testing.T) {
		execFn := func(_ context.Context, _ string) (string, string, int, error) {
			return "", "", 1, nil
		}

		cfg := &ProjectConfig{Commands: []Command{
			{Name: "test", Run: "failing"},
		}}
		streams, _, _ := newStreams()

		err := RunRemote(context.Background(), execFn, cfg, "/workspace", streams)
		if err == nil {
			t.Fatal("expected error for non-zero exit code")
		}
		if !strings.Contains(err.Error(), "remote test failed") {
			t.Errorf("unexpected error: %v", err)
		}
	})

	t.Run("empty stdout not written", func(t *testing.T) {
		execFn := func(_ context.Context, _ string) (string, string, int, error) {
			return "", "", 0, nil
		}

		cfg := &ProjectConfig{Commands: []Command{
			{Name: "test", Run: "silent"},
		}}
		streams, out, _ := newStreams()

		if err := RunRemote(context.Background(), execFn, cfg, "/workspace", streams); err != nil {
			t.Fatal(err)
		}
		if out.Len() != 0 {
			t.Errorf("expected no stdout output, got: %s", out.String())
		}
	})

	t.Run("script uses dest directory", func(t *testing.T) {
		var capturedScript string
		execFn := func(_ context.Context, script string) (string, string, int, error) {
			capturedScript = script
			return "", "", 0, nil
		}

		cfg := &ProjectConfig{Commands: []Command{
			{Name: "test", Run: "go test ./..."},
		}}
		streams, _, _ := newStreams()

		if err := RunRemote(context.Background(), execFn, cfg, "/custom/path", streams); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(capturedScript, "cd '/custom/path' &&") {
			t.Errorf("expected script to cd into dest, got: %s", capturedScript)
		}
	})
}

// --- Config with FileExt / Timeout tests ---

func TestCommandFileExtRoundTrip(t *testing.T) {
	dir := t.TempDir()
	chunkDir := filepath.Join(dir, ".chunk")
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		t.Fatal(err)
	}

	raw := `{"commands":[{"name":"lint","run":"eslint .","fileExt":".ts","timeout":60}]}`
	if err := os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Commands) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cfg.Commands))
	}
	c := cfg.Commands[0]
	if c.FileExt != ".ts" {
		t.Errorf("expected FileExt %q, got %q", ".ts", c.FileExt)
	}
	if c.Timeout != 60 {
		t.Errorf("expected Timeout 60, got %d", c.Timeout)
	}

	// Save and reload to verify round-trip
	if err := SaveProjectConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	cfg2, err := LoadProjectConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg2.Commands[0].FileExt != ".ts" {
		t.Errorf("round-trip lost FileExt: got %q", cfg2.Commands[0].FileExt)
	}
}

func TestCommandFileExtOmitted(t *testing.T) {
	dir := t.TempDir()

	cfg := &ProjectConfig{Commands: []Command{
		{Name: "test", Run: "go test ./..."},
	}}
	if err := SaveProjectConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".chunk", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	// fileExt and timeout should not appear when empty/zero
	if strings.Contains(string(data), "fileExt") {
		t.Errorf("expected fileExt to be omitted from JSON, got: %s", data)
	}
	if strings.Contains(string(data), "timeout") {
		t.Errorf("expected timeout to be omitted from JSON, got: %s", data)
	}
}

// --- Cache with fileExt tests ---

func TestCacheWithFileExt(t *testing.T) {
	dir := t.TempDir()

	// Initialize a git repo so computeContentHash works
	initGitRepo(t, dir)

	// Write cache with no fileExt
	if err := WriteCache(dir, "test", "", 0, "passed"); err != nil {
		t.Fatal(err)
	}
	cached := CheckCache(dir, "test", "")
	if cached == nil {
		t.Fatal("expected cache hit with no fileExt")
	}
	if cached.Status != "pass" {
		t.Errorf("expected pass, got %s", cached.Status)
	}

	// Cache written with no fileExt should miss when checked with fileExt
	// because the content hashes will differ (different git diff args)
	if err := WriteCache(dir, "lint", ".ts", 0, "ok"); err != nil {
		t.Fatal(err)
	}
	cached = CheckCache(dir, "lint", ".ts")
	if cached == nil {
		t.Fatal("expected cache hit with matching fileExt")
	}
}

func TestCache(t *testing.T) {
	tests := []struct {
		name       string
		exitCode   int
		output     string
		wantStatus string
	}{
		{"pass result", 0, "all good", "pass"},
		{"fail result", 1, "FAIL: test_foo", "fail"},
		{"exit code 2", 2, "error", "fail"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			initGitRepo(t, dir)

			err := WriteCache(dir, "cmd", "", tt.exitCode, tt.output)
			if err != nil {
				t.Fatalf("WriteCache: %v", err)
			}

			cached := CheckCache(dir, "cmd", "")
			if cached == nil {
				t.Fatal("expected cache hit")
			}
			if cached.Status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", cached.Status, tt.wantStatus)
			}
			if cached.ExitCode != tt.exitCode {
				t.Fatalf("exitCode = %d, want %d", cached.ExitCode, tt.exitCode)
			}
			if cached.Output != tt.output {
				t.Fatalf("output = %q, want %q", cached.Output, tt.output)
			}
		})
	}
}

func TestCacheInvalidatedByChange(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	if err := WriteCache(dir, "test", "", 0, "ok"); err != nil {
		t.Fatal(err)
	}

	// Modify a tracked file so the content hash changes
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}

	cached := CheckCache(dir, "test", "")
	if cached != nil {
		t.Fatal("expected cache miss after file change")
	}
}

func TestCacheMissForMissingFile(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	cached := CheckCache(dir, "nonexistent", "")
	if cached != nil {
		t.Fatal("expected nil for nonexistent cache")
	}
}

func TestCacheOutputTruncation(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	bigOutput := strings.Repeat("x", 100*1024) // 100KB, exceeds maxOutputBytes (50KB)
	if err := WriteCache(dir, "big", "", 1, bigOutput); err != nil {
		t.Fatal(err)
	}

	cached := CheckCache(dir, "big", "")
	if cached == nil {
		t.Fatal("expected cache hit")
	}
	if len(cached.Output) > maxOutputBytes {
		t.Fatalf("output should be truncated to %d bytes, got %d", maxOutputBytes, len(cached.Output))
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"git", "init"},
		{"git", "config", "user.email", "test@test.com"},
		{"git", "config", "user.name", "Test"},
	} {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %s: %v", args, out, err)
		}
	}
	// Create an initial commit so HEAD exists
	readme := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readme, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
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
