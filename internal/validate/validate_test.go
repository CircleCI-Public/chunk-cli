package validate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
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

func testStatus(buf *bytes.Buffer) iostream.StatusFunc {
	return func(_ iostream.Level, msg string) {
		fmt.Fprintln(buf, msg)
	}
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
		var out bytes.Buffer

		assert.NilError(t, RunDryRun(cfg, "", testStatus(&out)))

		assert.Assert(t, strings.Contains(out.String(), "install: npm install"), "got: %s", out.String())
		assert.Assert(t, strings.Contains(out.String(), "test: npm test"), "got: %s", out.String())
	})

	t.Run("no commands", func(t *testing.T) {
		cfg := &config.ProjectConfig{}
		var out bytes.Buffer

		err := RunDryRun(cfg, "", testStatus(&out))
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
		streams, out, _ := newStreams()
		var statusBuf bytes.Buffer

		assert.NilError(t, RunAll(context.Background(), ".", cfg, testStatus(&statusBuf), streams))
		assert.Assert(t, strings.Contains(out.String(), "installed"), "got: %s", out.String())
		assert.Assert(t, strings.Contains(out.String(), "tested"), "got: %s", out.String())
		assert.Assert(t, strings.Contains(statusBuf.String(), "Running install"), "got: %s", statusBuf.String())
	})

	t.Run("no commands", func(t *testing.T) {
		cfg := &config.ProjectConfig{}
		streams, _, _ := newStreams()
		var statusBuf bytes.Buffer

		err := RunAll(context.Background(), ".", cfg, testStatus(&statusBuf), streams)
		assert.ErrorContains(t, err, "no validate commands")
	})

	t.Run("command failure", func(t *testing.T) {
		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "false"},
		}}
		streams, _, _ := newStreams()
		var statusBuf bytes.Buffer

		err := RunAll(context.Background(), ".", cfg, testStatus(&statusBuf), streams)
		assert.ErrorContains(t, err, "test command failed")
	})

	t.Run("skips remaining after failure", func(t *testing.T) {
		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "install", Run: "false"},
			{Name: "test", Run: "echo should-not-run"},
			{Name: "lint", Run: "echo should-not-run-either"},
		}}
		streams, out, _ := newStreams()
		var statusBuf bytes.Buffer

		err := RunAll(context.Background(), ".", cfg, testStatus(&statusBuf), streams)
		assert.Assert(t, err != nil, "expected error")
		assert.Assert(t, !strings.Contains(out.String(), "should-not-run"), "skipped command should not produce output, got: %s", out.String())
		assert.Assert(t, strings.Contains(statusBuf.String(), "test: skipped"), "got: %s", statusBuf.String())
		assert.Assert(t, strings.Contains(statusBuf.String(), "lint: skipped"), "got: %s", statusBuf.String())
	})

	t.Run("single command success", func(t *testing.T) {
		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "echo ok"},
		}}
		streams, out, _ := newStreams()
		var statusBuf bytes.Buffer

		assert.NilError(t, RunAll(context.Background(), ".", cfg, testStatus(&statusBuf), streams))
		assert.Assert(t, strings.Contains(out.String(), "ok"), "got: %s", out.String())
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
	// fileExt and timeout should not appear when empty/zero
	assert.Assert(t, !strings.Contains(string(data), "fileExt"), "expected fileExt to be omitted, got: %s", data)
	assert.Assert(t, !strings.Contains(string(data), "timeout"), "expected timeout to be omitted, got: %s", data)
}

// --- RunRemote tests ---

func TestRunRemote(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var execCount int
		var out bytes.Buffer
		execFn := func(_ context.Context, _ string) (int, error) {
			execCount++
			_, _ = fmt.Fprint(&out, "remote output\n")
			return 0, nil
		}

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "install", Run: "echo install"},
			{Name: "test", Run: "echo test"},
		}}

		assert.NilError(t, RunRemote(context.Background(), execFn, cfg, "", "/workspace", t.TempDir(), func(iostream.Level, string) {}))
		assert.Assert(t, strings.Contains(out.String(), "remote output"), "got: %s", out.String())
		assert.Equal(t, execCount, 2)
	})

	t.Run("non-zero exit code", func(t *testing.T) {
		execFn := func(_ context.Context, _ string) (int, error) {
			return 1, nil
		}

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "failing"},
		}}

		err := RunRemote(context.Background(), execFn, cfg, "", "/workspace", t.TempDir(), func(iostream.Level, string) {})
		assert.ErrorContains(t, err, "remote test failed")
	})

	t.Run("named runs only matching command", func(t *testing.T) {
		var capturedScripts []string
		execFn := func(_ context.Context, script string) (int, error) {
			capturedScripts = append(capturedScripts, script)
			return 0, nil
		}

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "install", Run: "echo install"},
			{Name: "test", Run: "echo test"},
		}}

		assert.NilError(t, RunRemote(context.Background(), execFn, cfg, "test", "/workspace", t.TempDir(), func(iostream.Level, string) {}))
		assert.Equal(t, len(capturedScripts), 1)
		assert.Assert(t, strings.Contains(capturedScripts[0], "echo test"), "got: %s", capturedScripts[0])
	})

	t.Run("named returns error for unknown command", func(t *testing.T) {
		execFn := func(_ context.Context, _ string) (int, error) {
			return 0, nil
		}

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "echo test"},
		}}

		err := RunRemote(context.Background(), execFn, cfg, "lint", "/workspace", t.TempDir(), func(iostream.Level, string) {})
		assert.ErrorContains(t, err, `"lint" not configured`)
	})

	t.Run("script uses dest directory", func(t *testing.T) {
		var capturedScript string
		execFn := func(_ context.Context, script string) (int, error) {
			capturedScript = script
			return 0, nil
		}

		cfg := &config.ProjectConfig{Commands: []config.Command{
			{Name: "test", Run: "go test ./..."},
		}}

		assert.NilError(t, RunRemote(context.Background(), execFn, cfg, "", "/custom/path", t.TempDir(), func(iostream.Level, string) {}))
		assert.Assert(t, strings.HasPrefix(capturedScript, "cd '/custom/path' &&"), "got: %s", capturedScript)
	})
}

func TestRunRemoteInline(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var capturedScript string
		var out bytes.Buffer
		execFn := func(_ context.Context, script string) (int, error) {
			capturedScript = script
			_, _ = fmt.Fprint(&out, "inline output\n")
			return 0, nil
		}

		assert.NilError(t, RunRemoteInline(context.Background(), execFn, "custom", "echo hello", "/workspace/repo", func(iostream.Level, string) {}))
		assert.Assert(t, strings.Contains(out.String(), "inline output"), "got: %s", out.String())
		assert.Assert(t, strings.HasPrefix(capturedScript, "cd '/workspace/repo' &&"), "got: %s", capturedScript)
	})

	t.Run("non-zero exit code", func(t *testing.T) {
		execFn := func(_ context.Context, _ string) (int, error) {
			return 1, nil
		}

		err := RunRemoteInline(context.Background(), execFn, "custom", "false", "/workspace", func(iostream.Level, string) {})
		assert.ErrorContains(t, err, "remote custom failed")
	})

	t.Run("exec error", func(t *testing.T) {
		execFn := func(_ context.Context, _ string) (int, error) {
			return 0, fmt.Errorf("connection lost")
		}

		err := RunRemoteInline(context.Background(), execFn, "custom", "echo hi", "/workspace", func(iostream.Level, string) {})
		assert.ErrorContains(t, err, "remote custom")
		assert.ErrorContains(t, err, "connection lost")
	})
}
