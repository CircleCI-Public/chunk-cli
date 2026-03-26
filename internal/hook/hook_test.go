package hook

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func testStreams() (iostream.Streams, *bytes.Buffer, *bytes.Buffer) {
	var out, errBuf bytes.Buffer
	return iostream.Streams{Out: &out, Err: &errBuf}, &out, &errBuf
}

func isBlockError(err error) bool {
	var blockErr *BlockError
	return errors.As(err, &blockErr)
}

// --- config ---

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, dir string)
		check func(t *testing.T, dir string, cfg *ResolvedConfig)
	}{
		{
			name:  "missing file returns defaults",
			setup: func(t *testing.T, dir string) {},
			check: func(t *testing.T, dir string, cfg *ResolvedConfig) {
				if cfg.ProjectDir != dir {
					t.Fatalf("expected ProjectDir %q, got %q", dir, cfg.ProjectDir)
				}
				if len(cfg.Execs) != 0 {
					t.Fatalf("expected no execs, got %d", len(cfg.Execs))
				}
				if _, ok := cfg.Triggers["pre-commit"]; !ok {
					t.Fatal("expected default pre-commit trigger")
				}
			},
		},
		{
			name: "valid JSON with execs tasks and triggers",
			setup: func(t *testing.T, dir string) {
				chunkDir := filepath.Join(dir, ".chunk")
				if err := os.MkdirAll(chunkDir, 0o755); err != nil {
					t.Fatal(err)
				}
				configJSON := `{
  "commands": [
    {"name": "tests", "run": "go test ./...", "timeout": 30},
    {"name": "lint", "run": "golangci-lint run"}
  ],
  "triggers": {
    "go-files": ["*.go", "**/*.go"]
  },
  "tasks": {
    "review": {"instructions": "Review code"}
  }
}`
				if err := os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(configJSON), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, dir string, cfg *ResolvedConfig) {
				if len(cfg.Execs) != 2 {
					t.Fatalf("expected 2 execs, got %d", len(cfg.Execs))
				}
				if cfg.Execs["tests"].Timeout != 30 {
					t.Fatalf("expected timeout 30, got %d", cfg.Execs["tests"].Timeout)
				}
				if cfg.Execs["lint"].Timeout != 300 {
					t.Fatalf("expected default timeout 300 for lint, got %d", cfg.Execs["lint"].Timeout)
				}
				if len(cfg.Tasks) != 1 {
					t.Fatalf("expected 1 task, got %d", len(cfg.Tasks))
				}
				if cfg.Tasks["review"].Limit != 3 {
					t.Fatalf("expected default limit 3, got %d", cfg.Tasks["review"].Limit)
				}
				if cfg.Tasks["review"].Timeout != 600 {
					t.Fatalf("expected default timeout 600, got %d", cfg.Tasks["review"].Timeout)
				}
				patterns, ok := cfg.Triggers["go-files"]
				if !ok {
					t.Fatal("expected go-files trigger")
				}
				if len(patterns) != 2 {
					t.Fatalf("expected 2 patterns, got %d", len(patterns))
				}
				if _, ok := cfg.Triggers["pre-commit"]; !ok {
					t.Fatal("expected default pre-commit trigger to be preserved")
				}
			},
		},
		{
			name: "sentinel dir from env",
			setup: func(t *testing.T, dir string) {
				t.Setenv("CHUNK_HOOK_SENTINELS_DIR", t.TempDir())
			},
			check: func(t *testing.T, dir string, cfg *ResolvedConfig) {
				sentDir := os.Getenv("CHUNK_HOOK_SENTINELS_DIR")
				if cfg.SentinelDir != sentDir {
					t.Fatalf("expected sentinel dir %q from env, got %q", sentDir, cfg.SentinelDir)
				}
			},
		},
		{
			name: "invalid JSON returns empty config",
			setup: func(t *testing.T, dir string) {
				chunkDir := filepath.Join(dir, ".chunk")
				if err := os.MkdirAll(chunkDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte("{{invalid"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			check: func(t *testing.T, dir string, cfg *ResolvedConfig) {
				if len(cfg.Execs) != 0 {
					t.Fatalf("expected no execs from invalid JSON, got %d", len(cfg.Execs))
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setup(t, dir)
			cfg := LoadConfig(dir)
			tt.check(t, dir, cfg)
		})
	}
}

// --- env ---

func TestIsEnabled(t *testing.T) {
	tests := []struct {
		name     string
		envVars  map[string]string
		cmdName  string
		expected bool
	}{
		{"disabled by default", nil, "tests", false},
		{"global enable", map[string]string{"CHUNK_HOOK_ENABLE": "1"}, "tests", true},
		{"global enable true", map[string]string{"CHUNK_HOOK_ENABLE": "true"}, "tests", true},
		{"global enable yes", map[string]string{"CHUNK_HOOK_ENABLE": "yes"}, "tests", true},
		{"global disable", map[string]string{"CHUNK_HOOK_ENABLE": "0"}, "tests", false},
		{"per-cmd override enable", map[string]string{
			"CHUNK_HOOK_ENABLE":       "0",
			"CHUNK_HOOK_ENABLE_TESTS": "1",
		}, "tests", true},
		{"per-cmd override disable", map[string]string{
			"CHUNK_HOOK_ENABLE":       "1",
			"CHUNK_HOOK_ENABLE_TESTS": "0",
		}, "tests", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear relevant env vars
			t.Setenv("CHUNK_HOOK_ENABLE", "")
			t.Setenv("CHUNK_HOOK_ENABLE_TESTS", "")

			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			got := IsEnabled(tt.cmdName)
			if got != tt.expected {
				t.Fatalf("IsEnabled(%q) = %v, want %v", tt.cmdName, got, tt.expected)
			}
		})
	}
}

func TestResolveProject(t *testing.T) {
	t.Run("absolute path returned as-is", func(t *testing.T) {
		got := ResolveProject("/abs/path")
		if got != "/abs/path" {
			t.Fatalf("expected /abs/path, got %q", got)
		}
	})

	t.Run("empty uses CLAUDE_PROJECT_DIR", func(t *testing.T) {
		t.Setenv("CLAUDE_PROJECT_DIR", "/from/claude")
		got := ResolveProject("")
		if got != "/from/claude" {
			t.Fatalf("expected /from/claude, got %q", got)
		}
	})

	t.Run("relative with PROJECT_ROOT", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_PROJECT_ROOT", "/root")
		got := ResolveProject("myproject")
		if got != "/root/myproject" {
			t.Fatalf("expected /root/myproject, got %q", got)
		}
	})

	t.Run("relative without PROJECT_ROOT uses cwd", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_PROJECT_ROOT", "")
		cwd, _ := os.Getwd()
		got := ResolveProject("myproject")
		expected := filepath.Join(cwd, "myproject")
		if got != expected {
			t.Fatalf("expected %q, got %q", expected, got)
		}
	})
}

// --- envupdate ---

func TestRunEnvUpdate(t *testing.T) {
	t.Run("writes env file with enable profile", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env")
		logDir := filepath.Join(dir, "logs")
		streams, _, _ := testStreams()

		err := RunEnvUpdate(EnvUpdateOptions{
			Profile:      "enable",
			EnvFile:      envFile,
			LogDir:       logDir,
			StartupFiles: []string{},
		}, streams)
		if err != nil {
			t.Fatal(err)
		}

		data, err := os.ReadFile(envFile)
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if !strings.Contains(content, "CHUNK_HOOK_ENABLE=1") {
			t.Fatal("expected CHUNK_HOOK_ENABLE=1 in env file")
		}
	})

	t.Run("writes disable profile", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env")
		logDir := filepath.Join(dir, "logs")
		streams, _, _ := testStreams()

		err := RunEnvUpdate(EnvUpdateOptions{
			Profile:      "disable",
			EnvFile:      envFile,
			LogDir:       logDir,
			StartupFiles: []string{},
		}, streams)
		if err != nil {
			t.Fatal(err)
		}

		data, _ := os.ReadFile(envFile)
		if !strings.Contains(string(data), "CHUNK_HOOK_ENABLE=0") {
			t.Fatal("expected CHUNK_HOOK_ENABLE=0 for disable profile")
		}
	})

	t.Run("writes tests-lint profile", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env")
		logDir := filepath.Join(dir, "logs")
		streams, _, _ := testStreams()

		err := RunEnvUpdate(EnvUpdateOptions{
			Profile:      "tests-lint",
			EnvFile:      envFile,
			LogDir:       logDir,
			StartupFiles: []string{},
		}, streams)
		if err != nil {
			t.Fatal(err)
		}

		data, _ := os.ReadFile(envFile)
		content := string(data)
		if !strings.Contains(content, "CHUNK_HOOK_ENABLE_TESTS=1") {
			t.Fatal("expected CHUNK_HOOK_ENABLE_TESTS=1")
		}
		if !strings.Contains(content, "CHUNK_HOOK_ENABLE_LINT=1") {
			t.Fatal("expected CHUNK_HOOK_ENABLE_LINT=1")
		}
	})

	t.Run("invalid profile", func(t *testing.T) {
		streams, _, _ := testStreams()
		err := RunEnvUpdate(EnvUpdateOptions{Profile: "bogus"}, streams)
		if err == nil {
			t.Fatal("expected error for invalid profile")
		}
	})

	t.Run("defaults profile to enable", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env")
		logDir := filepath.Join(dir, "logs")
		streams, _, _ := testStreams()

		err := RunEnvUpdate(EnvUpdateOptions{
			EnvFile:      envFile,
			LogDir:       logDir,
			StartupFiles: []string{},
		}, streams)
		if err != nil {
			t.Fatal(err)
		}

		data, _ := os.ReadFile(envFile)
		if !strings.Contains(string(data), "CHUNK_HOOK_ENABLE=1") {
			t.Fatal("expected default enable profile")
		}
	})

	t.Run("verbose flag", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env")
		logDir := filepath.Join(dir, "logs")
		streams, _, _ := testStreams()

		err := RunEnvUpdate(EnvUpdateOptions{
			EnvFile:      envFile,
			LogDir:       logDir,
			Verbose:      true,
			StartupFiles: []string{},
		}, streams)
		if err != nil {
			t.Fatal(err)
		}

		data, _ := os.ReadFile(envFile)
		if !strings.Contains(string(data), "CHUNK_HOOK_VERBOSE=1") {
			t.Fatal("expected CHUNK_HOOK_VERBOSE=1")
		}
	})

	t.Run("project root", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env")
		logDir := filepath.Join(dir, "logs")
		streams, _, _ := testStreams()

		err := RunEnvUpdate(EnvUpdateOptions{
			EnvFile:      envFile,
			LogDir:       logDir,
			ProjectRoot:  "/my/projects",
			StartupFiles: []string{},
		}, streams)
		if err != nil {
			t.Fatal(err)
		}

		data, _ := os.ReadFile(envFile)
		if !strings.Contains(string(data), "CHUNK_HOOK_PROJECT_ROOT='/my/projects'") {
			t.Fatal("expected project root in env file")
		}
	})

	t.Run("creates log directory", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env")
		logDir := filepath.Join(dir, "nested", "logs")
		streams, _, _ := testStreams()

		err := RunEnvUpdate(EnvUpdateOptions{
			EnvFile:      envFile,
			LogDir:       logDir,
			StartupFiles: []string{},
		}, streams)
		if err != nil {
			t.Fatal(err)
		}

		info, err := os.Stat(logDir)
		if err != nil {
			t.Fatal("log directory not created")
		}
		if !info.IsDir() {
			t.Fatal("expected directory")
		}
	})
}

// --- repoinit ---

func TestRunRepoInit(t *testing.T) {
	t.Run("creates all template files", func(t *testing.T) {
		dir := t.TempDir()
		streams, _, _ := testStreams()

		err := RunRepoInit(dir, false, streams)
		if err != nil {
			t.Fatal(err)
		}

		expected := []string{
			".chunk/hook/.gitignore",
			".claude/settings.json",
		}
		for _, rel := range expected {
			path := filepath.Join(dir, rel)
			if _, err := os.Stat(path); err != nil {
				t.Fatalf("expected %s to exist", rel)
			}
		}
	})

	t.Run("substitutes project name in settings.json", func(t *testing.T) {
		dir := t.TempDir()
		// Create a subdir with known name
		projDir := filepath.Join(dir, "my-project")
		if err := os.Mkdir(projDir, 0o755); err != nil {
			t.Fatal(err)
		}
		streams, _, _ := testStreams()

		err := RunRepoInit(projDir, false, streams)
		if err != nil {
			t.Fatal(err)
		}

		data, _ := os.ReadFile(filepath.Join(projDir, ".claude", "settings.json"))
		if strings.Contains(string(data), "__PROJECT__") {
			t.Fatal("expected __PROJECT__ to be substituted")
		}
		if !strings.Contains(string(data), "my-project") {
			t.Fatal("expected project name in settings.json")
		}
	})

	t.Run("existing files get .example variant", func(t *testing.T) {
		dir := t.TempDir()
		streams, _, errBuf := testStreams()

		// First init
		if err := RunRepoInit(dir, false, streams); err != nil {
			t.Fatal(err)
		}

		// Second init without force
		errBuf.Reset()
		if err := RunRepoInit(dir, false, streams); err != nil {
			t.Fatal(err)
		}

		// Should have written example files
		errOutput := errBuf.String()
		if !strings.Contains(errOutput, "already exists") {
			t.Fatalf("expected 'already exists' message, got: %s", errOutput)
		}

		// Check that an example file was created for one of the template files
		exampleGitignore := filepath.Join(dir, ".chunk", "hook", ".example.gitignore")
		exampleSettings := filepath.Join(dir, ".claude", "settings.example.json")
		if _, err := os.Stat(exampleGitignore); err != nil {
			if _, err2 := os.Stat(exampleSettings); err2 != nil {
				t.Fatal("expected at least one .example file to exist")
			}
		}
	})

	t.Run("force overwrites existing files", func(t *testing.T) {
		dir := t.TempDir()
		streams, _, errBuf := testStreams()

		// First init
		if err := RunRepoInit(dir, false, streams); err != nil {
			t.Fatal(err)
		}

		// Second init with force
		errBuf.Reset()
		if err := RunRepoInit(dir, true, streams); err != nil {
			t.Fatal(err)
		}

		errOutput := errBuf.String()
		if strings.Contains(errOutput, "already exists") {
			t.Fatalf("force should overwrite, not create examples: %s", errOutput)
		}
		if !strings.Contains(errOutput, "Created") {
			t.Fatalf("expected 'Created' messages, got: %s", errOutput)
		}
	})
}

func TestSanitizeProjectName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"my-project", "my-project"},
		{"my project", "my_project"},
		{"my/project", "my_project"},
		{"safe.name_here-123", "safe.name_here-123"},
		{"@scope/pkg", "_scope_pkg"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeProjectName(tt.input)
			if got != tt.expected {
				t.Fatalf("sanitizeProjectName(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestMakeExamplePath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/dir/config.yml", "/dir/config.example.yml"},
		{"/dir/config.json", "/dir/config.example.json"},
		{"/dir/.gitignore", "/dir/.example.gitignore"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := makeExamplePath(tt.input)
			if got != tt.expected {
				t.Fatalf("makeExamplePath(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

// --- sentinel ---

func TestWriteAndReadSentinel(t *testing.T) {
	sentDir := t.TempDir()
	projDir := "/test/project"
	name := "tests"

	data := SentinelData{
		Status:     "pass",
		StartedAt:  "2024-01-01T00:00:00Z",
		FinishedAt: "2024-01-01T00:00:05Z",
		ExitCode:   0,
		Command:    "go test ./...",
		Output:     "ok",
		Project:    projDir,
	}

	if err := writeSentinel(sentDir, projDir, name, data); err != nil {
		t.Fatal(err)
	}

	// Verify file exists and is valid JSON
	path := SentinelPath(sentDir, projDir, name)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var got SentinelData
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "pass" {
		t.Fatalf("expected status pass, got %q", got.Status)
	}
	if got.Command != "go test ./..." {
		t.Fatalf("expected command, got %q", got.Command)
	}
}

func TestSentinelIDDeterministic(t *testing.T) {
	id1 := sentinelID("/project", "tests")
	id2 := sentinelID("/project", "tests")
	if id1 != id2 {
		t.Fatalf("sentinel ID not deterministic: %q != %q", id1, id2)
	}

	// Different project should give different ID
	id3 := sentinelID("/other", "tests")
	if id1 == id3 {
		t.Fatal("expected different IDs for different projects")
	}
}

// --- scope ---

func TestActivateScope(t *testing.T) {
	t.Run("writes marker file with matching path", func(t *testing.T) {
		dir := t.TempDir()
		// Must include tool_input with a path matching the project dir
		input := fmt.Sprintf(`{"session_id":"sess-123","tool_input":{"file_path":"%s/main.go"}}`, dir)
		stdin := strings.NewReader(input)

		err := ActivateScope(dir, stdin)
		if err != nil {
			t.Fatal(err)
		}

		markerPath := filepath.Join(dir, ".chunk", "hook", ".chunk-hook-active")
		data, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatal("expected marker file to exist")
		}

		var marker map[string]interface{}
		if err := json.Unmarshal(data, &marker); err != nil {
			t.Fatal(err)
		}
		if marker["sessionId"] != "sess-123" {
			t.Fatalf("expected sessionId sess-123, got %v", marker["sessionId"])
		}
	})

	t.Run("no session is no-op", func(t *testing.T) {
		dir := t.TempDir()
		stdin := strings.NewReader(`{}`)

		err := ActivateScope(dir, stdin)
		if err != nil {
			t.Fatal(err)
		}

		markerPath := filepath.Join(dir, ".chunk", "hook", ".chunk-hook-active")
		if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
			t.Fatal("expected no marker file when no session_id")
		}
	})

	t.Run("invalid JSON is no-op", func(t *testing.T) {
		dir := t.TempDir()
		stdin := strings.NewReader("not json")

		err := ActivateScope(dir, stdin)
		if err != nil {
			t.Fatal("expected no error for invalid JSON")
		}
	})
}

func TestDeactivateScope(t *testing.T) {
	t.Run("removes marker file", func(t *testing.T) {
		dir := t.TempDir()

		// Create marker
		stdin := strings.NewReader(`{"session_id":"sess-123"}`)
		if err := ActivateScope(dir, stdin); err != nil {
			t.Fatal(err)
		}

		// Deactivate
		stdin = strings.NewReader(`{"session_id":"sess-123"}`)
		if err := DeactivateScope(dir, stdin); err != nil {
			t.Fatal(err)
		}

		markerPath := filepath.Join(dir, ".chunk", "hook", ".chunk-hook-active")
		if _, err := os.Stat(markerPath); !os.IsNotExist(err) {
			t.Fatal("expected marker to be removed")
		}
	})

	t.Run("no session_id returns error", func(t *testing.T) {
		dir := t.TempDir()
		stdin := strings.NewReader(`{}`)

		err := DeactivateScope(dir, stdin)
		if err == nil {
			t.Fatal("expected error when no session_id")
		}
		if !strings.Contains(err.Error(), "session") {
			t.Fatalf("expected session error, got: %s", err)
		}
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		dir := t.TempDir()
		stdin := strings.NewReader("bad")

		err := DeactivateScope(dir, stdin)
		if err == nil {
			t.Fatal("expected error for invalid JSON")
		}
	})

	t.Run("missing marker is no-op", func(t *testing.T) {
		dir := t.TempDir()
		stdin := strings.NewReader(`{"session_id":"sess-123"}`)

		err := DeactivateScope(dir, stdin)
		if err != nil {
			t.Fatal("expected no error when marker doesn't exist")
		}
	})
}

// --- state ---

func TestStateSave(t *testing.T) {
	t.Run("saves event by name", func(t *testing.T) {
		sentDir := t.TempDir()
		projDir := "/test/proj"
		stdin := strings.NewReader(`{"hook_event_name":"UserPromptSubmit","session_id":"s1","prompt":"hello"}`)

		if err := StateSave(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		state := readState(sentDir, projDir)
		event, ok := state["UserPromptSubmit"].(map[string]interface{})
		if !ok {
			t.Fatal("expected UserPromptSubmit in state")
		}
		entries, ok := event["__entries"].([]interface{})
		if !ok {
			t.Fatal("expected __entries")
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
	})

	t.Run("no event name is no-op", func(t *testing.T) {
		sentDir := t.TempDir()
		stdin := strings.NewReader(`{"session_id":"s1"}`)

		if err := StateSave(sentDir, "/proj", stdin); err != nil {
			t.Fatal(err)
		}
		// No state file should be written
		state := readState(sentDir, "/proj")
		if len(state) != 0 {
			t.Fatalf("expected empty state, got %v", state)
		}
	})

	t.Run("invalid JSON is no-op", func(t *testing.T) {
		sentDir := t.TempDir()
		stdin := strings.NewReader("not json")

		err := StateSave(sentDir, "/proj", stdin)
		if err != nil {
			t.Fatal("expected no error for invalid JSON")
		}
	})

	t.Run("different session clears state", func(t *testing.T) {
		sentDir := t.TempDir()
		projDir := "/proj"

		// Save with session s1
		stdin := strings.NewReader(`{"hook_event_name":"E1","session_id":"s1"}`)
		if err := StateSave(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		// Save with session s2 — should clear s1 data
		stdin = strings.NewReader(`{"hook_event_name":"E2","session_id":"s2"}`)
		if err := StateSave(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		state := readState(sentDir, projDir)
		if _, ok := state["E1"]; ok {
			t.Fatal("expected E1 to be cleared when session changed")
		}
		if _, ok := state["E2"]; !ok {
			t.Fatal("expected E2 to be present")
		}
	})
}

func TestStateAppend(t *testing.T) {
	t.Run("appends to existing entries", func(t *testing.T) {
		sentDir := t.TempDir()
		projDir := "/proj"

		// Save first
		stdin := strings.NewReader(`{"hook_event_name":"E","session_id":"s1","n":1}`)
		if err := StateSave(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		// Append
		stdin = strings.NewReader(`{"hook_event_name":"E","session_id":"s1","n":2}`)
		if err := StateAppend(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		state := readState(sentDir, projDir)
		event, _ := state["E"].(map[string]interface{})
		entries, _ := event["__entries"].([]interface{})
		if len(entries) != 2 {
			t.Fatalf("expected 2 entries after append, got %d", len(entries))
		}
	})

	t.Run("creates event if not exists", func(t *testing.T) {
		sentDir := t.TempDir()
		stdin := strings.NewReader(`{"hook_event_name":"New","session_id":"s1"}`)

		if err := StateAppend(sentDir, "/proj", stdin); err != nil {
			t.Fatal(err)
		}

		state := readState(sentDir, "/proj")
		if _, ok := state["New"]; !ok {
			t.Fatal("expected New event to exist")
		}
	})

	t.Run("different session clears then appends", func(t *testing.T) {
		sentDir := t.TempDir()
		projDir := "/proj"

		stdin := strings.NewReader(`{"hook_event_name":"E","session_id":"s1"}`)
		if err := StateSave(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		// Append with different session
		stdin = strings.NewReader(`{"hook_event_name":"E","session_id":"s2"}`)
		if err := StateAppend(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		state := readState(sentDir, projDir)
		session := getSessionID(state)
		if session != "s2" {
			t.Fatalf("expected session s2, got %q", session)
		}
	})
}

func TestStateLoad(t *testing.T) {
	t.Run("empty state outputs empty object", func(t *testing.T) {
		sentDir := t.TempDir()
		streams, out, _ := testStreams()

		if err := StateLoad(sentDir, "/proj", "", streams); err != nil {
			t.Fatal(err)
		}

		trimmed := strings.TrimSpace(out.String())
		if trimmed != "{}" {
			t.Fatalf("expected {}, got %q", trimmed)
		}
	})

	t.Run("outputs saved state", func(t *testing.T) {
		sentDir := t.TempDir()
		projDir := "/proj"

		stdin := strings.NewReader(`{"hook_event_name":"E","session_id":"s1"}`)
		if err := StateSave(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		streams, out, _ := testStreams()
		if err := StateLoad(sentDir, projDir, "", streams); err != nil {
			t.Fatal(err)
		}

		var state map[string]interface{}
		if err := json.Unmarshal(out.Bytes(), &state); err != nil {
			t.Fatalf("output not valid JSON: %s", out.String())
		}
		if _, ok := state["E"]; !ok {
			t.Fatal("expected event E in output")
		}
	})

	t.Run("with field outputs compact", func(t *testing.T) {
		sentDir := t.TempDir()
		projDir := "/proj"

		stdin := strings.NewReader(`{"hook_event_name":"E","session_id":"s1"}`)
		if err := StateSave(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		streams, out, _ := testStreams()
		if err := StateLoad(sentDir, projDir, "E", streams); err != nil {
			t.Fatal(err)
		}

		// With a field value the output should be compact (single line)
		if strings.Contains(out.String(), "\n  ") {
			t.Fatal("expected compact JSON for field query")
		}
	})
}

func TestStateClear(t *testing.T) {
	t.Run("removes state file", func(t *testing.T) {
		sentDir := t.TempDir()
		projDir := "/proj"

		// Save something
		stdin := strings.NewReader(`{"hook_event_name":"E","session_id":"s1"}`)
		if err := StateSave(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		// Clear
		stdin = strings.NewReader(`{"session_id":"s1"}`)
		if err := StateClear(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		state := readState(sentDir, projDir)
		if len(state) != 0 {
			t.Fatalf("expected empty state after clear, got %v", state)
		}
	})

	t.Run("different session skips clear", func(t *testing.T) {
		sentDir := t.TempDir()
		projDir := "/proj"

		stdin := strings.NewReader(`{"hook_event_name":"E","session_id":"s1"}`)
		if err := StateSave(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		// Try to clear with different session
		stdin = strings.NewReader(`{"session_id":"s2"}`)
		if err := StateClear(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		// State should still exist
		state := readState(sentDir, projDir)
		if _, ok := state["E"]; !ok {
			t.Fatal("expected state to be preserved for different session")
		}
	})

	t.Run("no session clears unconditionally", func(t *testing.T) {
		sentDir := t.TempDir()
		projDir := "/proj"

		stdin := strings.NewReader(`{"hook_event_name":"E","session_id":"s1"}`)
		if err := StateSave(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		// Clear with no session
		stdin = strings.NewReader(`{}`)
		if err := StateClear(sentDir, projDir, stdin); err != nil {
			t.Fatal(err)
		}

		state := readState(sentDir, projDir)
		if len(state) != 0 {
			t.Fatalf("expected empty state, got %v", state)
		}
	})
}

// --- exec ---

func TestRunExecRun(t *testing.T) {
	t.Run("not enabled returns nil", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_ENABLE", "")
		cfg := &ResolvedConfig{SentinelDir: t.TempDir(), ProjectDir: t.TempDir()}

		err := RunExecRun(cfg, ExecRunFlags{Name: "tests"})
		if err != nil {
			t.Fatal("expected nil when not enabled")
		}
	})

	t.Run("runs command and writes sentinel", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_ENABLE", "1")
		sentDir := t.TempDir()
		projDir := t.TempDir()
		cfg := &ResolvedConfig{
			SentinelDir: sentDir,
			ProjectDir:  projDir,
			Execs: map[string]ExecConfig{
				"tests": {Command: "echo hello", Timeout: 10},
			},
		}

		err := RunExecRun(cfg, ExecRunFlags{Name: "tests", Always: true})
		if err != nil {
			t.Fatal(err)
		}

		// Check sentinel was written
		path := SentinelPath(sentDir, projDir, "tests")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("expected sentinel file to exist")
		}

		var sd SentinelData
		if err := json.Unmarshal(data, &sd); err != nil {
			t.Fatal(err)
		}
		if sd.Status != "pass" {
			t.Fatalf("expected pass, got %q", sd.Status)
		}
		if sd.Output != "hello" {
			t.Fatalf("expected output 'hello', got %q", sd.Output)
		}
	})

	t.Run("failing command with no-check returns nil", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_ENABLE", "1")
		sentDir := t.TempDir()
		projDir := t.TempDir()
		cfg := &ResolvedConfig{
			SentinelDir: sentDir,
			ProjectDir:  projDir,
		}

		err := RunExecRun(cfg, ExecRunFlags{
			Name:    "fail",
			Cmd:     "exit 1",
			NoCheck: true,
			Always:  true,
		})
		if err != nil {
			t.Fatal("expected nil with no-check, got:", err)
		}

		path := SentinelPath(sentDir, projDir, "fail")
		data, _ := os.ReadFile(path)
		var sd SentinelData
		if err := json.Unmarshal(data, &sd); err != nil {
			t.Fatalf("unmarshal sentinel: %v", err)
		}
		if sd.Status != "fail" {
			t.Fatalf("expected fail status, got %q", sd.Status)
		}
		if sd.ExitCode != 1 {
			t.Fatalf("expected exit code 1, got %d", sd.ExitCode)
		}
	})

	t.Run("failing command without no-check returns error", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_ENABLE", "1")
		cfg := &ResolvedConfig{
			SentinelDir: t.TempDir(),
			ProjectDir:  t.TempDir(),
		}

		err := RunExecRun(cfg, ExecRunFlags{
			Name:   "fail",
			Cmd:    "exit 1",
			Always: true,
		})
		if err == nil {
			t.Fatal("expected error for failing command")
		}
		if !strings.Contains(err.Error(), "fail") {
			t.Fatalf("expected error about failure, got: %s", err)
		}
	})

	t.Run("cmd flag overrides config", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_ENABLE", "1")
		sentDir := t.TempDir()
		projDir := t.TempDir()
		cfg := &ResolvedConfig{
			SentinelDir: sentDir,
			ProjectDir:  projDir,
			Execs: map[string]ExecConfig{
				"tests": {Command: "echo config-cmd"},
			},
		}

		err := RunExecRun(cfg, ExecRunFlags{
			Name:   "tests",
			Cmd:    "echo flag-cmd",
			Always: true,
		})
		if err != nil {
			t.Fatal(err)
		}

		path := SentinelPath(sentDir, projDir, "tests")
		data, _ := os.ReadFile(path)
		var sd SentinelData
		if err := json.Unmarshal(data, &sd); err != nil {
			t.Fatalf("unmarshal sentinel: %v", err)
		}
		if sd.Command != "echo flag-cmd" {
			t.Fatalf("expected flag command override, got %q", sd.Command)
		}
	})

	t.Run("fallback command when none configured", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_ENABLE", "1")
		cfg := &ResolvedConfig{
			SentinelDir: t.TempDir(),
			ProjectDir:  t.TempDir(),
			Execs:       map[string]ExecConfig{},
		}

		// Should use the echo fallback and not error
		err := RunExecRun(cfg, ExecRunFlags{Name: "unknown", NoCheck: true, Always: true})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunExecCheck(t *testing.T) {
	t.Run("not enabled returns nil", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_ENABLE", "")
		t.Setenv("CHUNK_HOOK_ENABLE_TESTS", "")
		cfg := &ResolvedConfig{}

		err := RunExecCheck(cfg, ExecCheckFlags{Name: "tests"}, nil)
		if err != nil {
			t.Fatal("expected nil when not enabled")
		}
	})

	t.Run("enabled with no sentinel auto-runs", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_ENABLE", "1")
		dir := t.TempDir()
		cfg := &ResolvedConfig{
			ProjectDir:  dir,
			SentinelDir: t.TempDir(),
			Execs: map[string]ExecConfig{
				"tests": {Command: "echo ok", Timeout: 300, Always: true},
			},
			Tasks:    map[string]TaskConfig{},
			Triggers: map[string][]string{},
		}

		err := RunExecCheck(cfg, ExecCheckFlags{Name: "tests", Always: true}, nil)
		// Should auto-run and pass since "echo ok" succeeds
		if err != nil {
			t.Fatalf("expected auto-run to pass, got: %v", err)
		}
	})
}

// --- setup ---

func TestValidateProfile(t *testing.T) {
	for _, p := range ValidProfiles {
		if err := ValidateProfile(p); err != nil {
			t.Fatalf("expected valid profile %q, got error: %s", p, err)
		}
	}

	if err := ValidateProfile("bogus"); err == nil {
		t.Fatal("expected error for invalid profile")
	}
}

func TestRunSetup(t *testing.T) {
	t.Run("combines env update and repo init", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env")
		streams, _, _ := testStreams()

		err := RunSetup(dir, "enable", false, false, envFile, streams)
		if err != nil {
			t.Fatal(err)
		}

		// Env file written
		if _, err := os.Stat(envFile); err != nil {
			t.Fatal("expected env file")
		}
		// Gitignore written (template file)
		gitignorePath := filepath.Join(dir, ".chunk", "hook", ".gitignore")
		if _, err := os.Stat(gitignorePath); err != nil {
			t.Fatal("expected .gitignore")
		}
	})

	t.Run("skip-env skips env update", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env")
		streams, _, _ := testStreams()

		err := RunSetup(dir, "", false, true, envFile, streams)
		if err != nil {
			t.Fatal(err)
		}

		// Env file should NOT exist
		if _, err := os.Stat(envFile); !os.IsNotExist(err) {
			t.Fatal("expected no env file with skip-env")
		}
		// Gitignore should still exist (template file)
		gitignorePath := filepath.Join(dir, ".chunk", "hook", ".gitignore")
		if _, err := os.Stat(gitignorePath); err != nil {
			t.Fatal("expected .gitignore")
		}
	})

	t.Run("invalid profile returns error", func(t *testing.T) {
		streams, _, _ := testStreams()
		err := RunSetup(t.TempDir(), "nope", false, false, "", streams)
		if err == nil {
			t.Fatal("expected error for invalid profile")
		}
	})

	t.Run("defaults profile to enable", func(t *testing.T) {
		dir := t.TempDir()
		envFile := filepath.Join(dir, "env")
		streams, _, _ := testStreams()

		err := RunSetup(dir, "", false, false, envFile, streams)
		if err != nil {
			t.Fatal(err)
		}

		data, _ := os.ReadFile(envFile)
		if !strings.Contains(string(data), "CHUNK_HOOK_ENABLE=1") {
			t.Fatal("expected default enable profile")
		}
	})
}

// --- sync ---

func TestParseSpecs(t *testing.T) {
	t.Run("valid specs", func(t *testing.T) {
		specs, err := ParseSpecs([]string{"exec:tests", "task:review"})
		if err != nil {
			t.Fatal(err)
		}
		if len(specs) != 2 {
			t.Fatalf("expected 2 specs, got %d", len(specs))
		}
		if specs[0].Type != "exec" || specs[0].Name != "tests" {
			t.Fatalf("unexpected spec[0]: %+v", specs[0])
		}
		if specs[1].Type != "task" || specs[1].Name != "review" {
			t.Fatalf("unexpected spec[1]: %+v", specs[1])
		}
	})

	t.Run("no colon", func(t *testing.T) {
		_, err := ParseSpecs([]string{"invalid"})
		if err == nil {
			t.Fatal("expected error for missing colon")
		}
	})

	t.Run("empty name", func(t *testing.T) {
		_, err := ParseSpecs([]string{"exec:"})
		if err == nil {
			t.Fatal("expected error for empty name")
		}
	})

	t.Run("invalid type", func(t *testing.T) {
		_, err := ParseSpecs([]string{"sync:tests"})
		if err == nil {
			t.Fatal("expected error for invalid type")
		}
	})

	t.Run("empty args", func(t *testing.T) {
		_, err := ParseSpecs(nil)
		if err == nil {
			t.Fatal("expected error for no specs")
		}
	})
}

func TestRunSyncCheck(t *testing.T) {
	t.Run("not enabled returns nil", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_ENABLE", "")
		t.Setenv("CHUNK_HOOK_ENABLE_TESTS", "")
		cfg := &ResolvedConfig{}

		err := RunSyncCheck(cfg, SyncCheckFlags{
			Specs: []CommandSpec{{Type: "exec", Name: "tests"}},
		}, nil)
		if err != nil {
			t.Fatal("expected nil when not enabled")
		}
	})

	t.Run("enabled with valid config", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_ENABLE", "1")
		dir := t.TempDir()
		cfg := &ResolvedConfig{
			ProjectDir:  dir,
			SentinelDir: t.TempDir(),
			Execs: map[string]ExecConfig{
				"tests": {Command: "echo ok", Timeout: 300},
			},
			Tasks:    map[string]TaskConfig{},
			Triggers: map[string][]string{},
		}

		err := RunSyncCheck(cfg, SyncCheckFlags{
			Specs: []CommandSpec{{Type: "exec", Name: "tests"}},
		}, nil)
		// With no sentinel and no git repo, this may block or allow
		// depending on change detection; just check no panic
		_ = err
	})
}

// --- task ---

func TestRunTaskCheck(t *testing.T) {
	t.Run("not enabled returns nil", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_ENABLE", "")
		cfg := &ResolvedConfig{}

		err := RunTaskCheck(cfg, TaskCheckFlags{Name: "review"}, nil)
		if err != nil {
			t.Fatal("expected nil when not enabled")
		}
	})

	t.Run("enabled with no sentinel blocks", func(t *testing.T) {
		t.Setenv("CHUNK_HOOK_ENABLE", "1")
		dir := t.TempDir()
		cfg := &ResolvedConfig{
			ProjectDir:  dir,
			SentinelDir: t.TempDir(),
			Tasks: map[string]TaskConfig{
				"review": {Instructions: "", Limit: 3, Timeout: 600},
			},
			Execs:    map[string]ExecConfig{},
			Triggers: map[string][]string{},
		}

		err := RunTaskCheck(cfg, TaskCheckFlags{Name: "review"}, nil)
		// Should block because there's no sentinel and no baseline fingerprint
		if err == nil {
			t.Fatal("expected block error when no sentinel exists")
		}
		if !isBlockError(err) {
			t.Fatalf("expected BlockError, got: %T: %v", err, err)
		}
	})
}

// --- templates ---

func TestTemplateFilesNotEmpty(t *testing.T) {
	if len(templateFiles) == 0 {
		t.Fatal("expected template files to be defined")
	}
	for _, tf := range templateFiles {
		if tf.relativePath == "" {
			t.Fatal("template file has empty relative path")
		}
		if tf.content == "" {
			t.Fatal("template file has empty content: " + tf.relativePath)
		}
	}
}
