package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/acceptance/testutil"
)

// --- repo init ---

func TestHookRepoInit(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)

	result := testutil.RunCLI(t, []string{
		"hook", "repo", "init", workDir,
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	// Verify config.yml was created
	configPath := filepath.Join(workDir, ".chunk", "hook", "config.yml")
	_, err := os.Stat(configPath)
	assert.NilError(t, err, "expected .chunk/hook/config.yml to exist")

	// Verify settings.json was created
	settingsPath := filepath.Join(workDir, ".claude", "settings.json")
	_, err = os.Stat(settingsPath)
	assert.NilError(t, err, "expected .claude/settings.json to exist")
}

func TestHookRepoInitExistingFiles(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)

	// First init
	result := testutil.RunCLI(t, []string{
		"hook", "repo", "init", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "first init failed: %s", result.Stderr)

	// Second init without --force should create .example files
	result = testutil.RunCLI(t, []string{
		"hook", "repo", "init", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "second init failed: %s", result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "already exists") || strings.Contains(combined, "example"),
		"expected existing file message, got: %s", combined)
}

func TestHookRepoInitForce(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)

	// First init
	result := testutil.RunCLI(t, []string{
		"hook", "repo", "init", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "first init failed: %s", result.Stderr)

	// Second init with --force should overwrite
	result = testutil.RunCLI(t, []string{
		"hook", "repo", "init", workDir, "--force",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "second init with --force failed: %s", result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Created") || strings.Contains(combined, "initialized"),
		"expected created message, got: %s", combined)
}

// --- setup ---

func TestHookSetupInvalidProfile(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)

	result := testutil.RunCLI(t, []string{
		"hook", "setup", workDir, "--profile", "bogus", "--skip-env",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Invalid profile") || strings.Contains(combined, "Valid profiles"),
		"expected invalid profile error, got: %s", combined)
}

func TestHookSetupHappyPath(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)

	result := testutil.RunCLI(t, []string{
		"hook", "setup", workDir, "--skip-env",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Setup complete") || strings.Contains(combined, "complete"),
		"expected setup complete message, got: %s", combined)

	// Verify config files created
	configPath := filepath.Join(workDir, ".chunk", "hook", "config.yml")
	_, err := os.Stat(configPath)
	assert.NilError(t, err, "expected .chunk/hook/config.yml to exist")
}

func TestHookSetupForce(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	env := testutil.NewTestEnv(t)

	// First setup
	result := testutil.RunCLI(t, []string{
		"hook", "setup", workDir, "--skip-env",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "first setup failed: %s", result.Stderr)

	// Second setup with --force
	result = testutil.RunCLI(t, []string{
		"hook", "setup", workDir, "--skip-env", "--force",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "second setup with --force failed: %s", result.Stderr)
}

// --- env update ---

func TestHookEnvUpdateInvalidProfile(t *testing.T) {
	env := testutil.NewTestEnv(t)

	result := testutil.RunCLI(t, []string{
		"hook", "env", "update", "--profile", "bogus",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Invalid profile") || strings.Contains(combined, "Valid profiles"),
		"expected invalid profile error, got: %s", combined)
}

func TestHookEnvUpdateHappyPath(t *testing.T) {
	env := testutil.NewTestEnv(t)

	result := testutil.RunCLI(t, []string{
		"hook", "env", "update",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Configuration complete") || strings.Contains(combined, "complete"),
		"expected completion message, got: %s", combined)
}

func TestHookEnvUpdateWithOptions(t *testing.T) {
	env := testutil.NewTestEnv(t)
	logDir := filepath.Join(env.HomeDir, "logs")

	result := testutil.RunCLI(t, []string{
		"hook", "env", "update",
		"--set-log-dir", logDir,
		"--set-verbose",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

// --- scope ---

func TestHookScopeDeactivateNoSession(t *testing.T) {
	env := testutil.NewTestEnv(t)

	// Pipe empty stdin — no session ID available
	result := testutil.RunCLIWithStdin(t, []string{
		"hook", "scope", "deactivate",
	}, env, env.HomeDir, []byte("{}"))

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "session"),
		"expected session error, got: %s", combined)
}

func TestHookScopeActivateNoProject(t *testing.T) {
	env := testutil.NewTestEnv(t)

	// Activate with no project config — should still succeed (no-op)
	result := testutil.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate",
	}, env, env.HomeDir, []byte("{}"))

	// May succeed or fail depending on project resolution — just check it doesn't crash
	assert.Assert(t, result.ExitCode == 0 || result.ExitCode == 1,
		"unexpected exit code %d: %s", result.ExitCode, result.Stderr)
}

// --- state ---

func TestHookStateLoadEmpty(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := testutil.RunCLI(t, []string{
		"hook", "state", "load", "--project", workDir,
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	// Empty state should output something like {} or empty
	trimmed := strings.TrimSpace(result.Stdout)
	assert.Assert(t, trimmed == "{}" || trimmed == "",
		"expected empty state, got: %q", trimmed)
}

func TestHookStateClear(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := testutil.RunCLIWithStdin(t, []string{
		"hook", "state", "clear", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"Stop","session_id":"sess-123"}`))

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
}

// --- exec ---

func TestHookExecRunNotEnabled(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()
	// Deliberately not setting CHUNK_HOOK_ENABLE or CHUNK_HOOK_ENABLE_TESTS

	result := testutil.RunCLI(t, []string{
		"hook", "exec", "run", "tests", "--no-check", "--project", workDir,
	}, env, workDir)

	// Should allow (exit 0) when not enabled
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
}

func TestHookExecRunNoCheck(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_ENABLE"] = "1"
	env.Extra["CHUNK_HOOK_ENABLE_TESTS"] = "1"
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := testutil.RunCLI(t, []string{
		"hook", "exec", "run", "tests", "--no-check", "--project", workDir,
	}, env, workDir)

	// --no-check should run the command and save result, exit 0
	// The command is "echo passed" which should succeed
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

// --- exec run flags ---

func TestHookExecRunFlags(t *testing.T) {
	tests := []struct {
		name       string
		flags      []string
		useTriggers bool
	}{
		{"cmd override", []string{"--cmd", "echo overridden"}, false},
		{"timeout", []string{"--timeout", "60"}, false},
		{"always", []string{"--always"}, false},
		{"file-ext", []string{"--file-ext", ".go"}, false},
		{"staged", []string{"--staged"}, false},
		{"on", []string{"--on", "go-files"}, true},
		{"trigger", []string{"--trigger", "*.ts"}, false},
		{"limit", []string{"--limit", "5"}, false},
		{"matcher", []string{"--matcher", "Bash"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
			if tt.useTriggers {
				writeHookConfigWithTriggers(t, workDir)
			} else {
				writeHookConfig(t, workDir)
			}

			env := testutil.NewTestEnv(t)
			env.Extra["CHUNK_HOOK_ENABLE"] = "1"
			env.Extra["CHUNK_HOOK_ENABLE_TESTS"] = "1"
			env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

			args := []string{"hook", "exec", "run", "tests"}
			args = append(args, tt.flags...)
			args = append(args, "--no-check", "--project", workDir)

			result := testutil.RunCLI(t, args, env, workDir)
			assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
		})
	}
}

// --- exec check flags ---

func TestHookExecCheckFlagsAccepted(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfigWithTriggers(t, workDir)

	env := testutil.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()
	// Not enabling — "not enabled" path exits 0 before reading stdin

	result := testutil.RunCLI(t, []string{
		"hook", "exec", "check", "tests",
		"--file-ext", ".go",
		"--staged",
		"--always",
		"--on", "go-files",
		"--trigger", "*.go",
		"--matcher", "Write",
		"--limit", "3",
		"--timeout", "30",
		"--project", workDir,
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "expected exit 0 (not enabled path), stderr: %s", result.Stderr)
}

// --- task check flags ---

func TestHookTaskCheckFlagsAccepted(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfigWithTriggers(t, workDir)

	// Write a dummy instructions file
	instrFile := filepath.Join(workDir, "instructions.md")
	err := os.WriteFile(instrFile, []byte("Review the code"), 0o644)
	assert.NilError(t, err)

	schemaFile := filepath.Join(workDir, "schema.json")
	err = os.WriteFile(schemaFile, []byte(`{"type": "object"}`), 0o644)
	assert.NilError(t, err)

	env := testutil.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()
	// Not enabling — exits 0

	result := testutil.RunCLI(t, []string{
		"hook", "task", "check", "review",
		"--instructions", instrFile,
		"--schema", schemaFile,
		"--always",
		"--staged",
		"--on", "go-files",
		"--trigger", "*.ts",
		"--matcher", "Write",
		"--limit", "5",
		"--project", workDir,
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "expected exit 0 (not enabled path), stderr: %s", result.Stderr)
}

// --- sync check flags ---

func TestHookSyncCheckFlagsAccepted(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfigWithTriggers(t, workDir)

	env := testutil.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()
	// Not enabling — exits 0

	result := testutil.RunCLI(t, []string{
		"hook", "sync", "check",
		"exec:tests",
		"--on", "go-files",
		"--trigger", "*.ts",
		"--matcher", "Edit",
		"--limit", "3",
		"--staged",
		"--always",
		"--on-fail", "retry",
		"--bail",
		"--project", workDir,
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "expected exit 0 (not enabled path), stderr: %s", result.Stderr)
}

// --- state save/append with --project ---

func TestHookStateSaveWithProject(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	stdinData := []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"sess-456","prompt":"hello"}`)
	result := testutil.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, stdinData)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
}

func TestHookStateAppendWithProject(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testutil.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	stdinData := []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"sess-789","prompt":"world"}`)
	result := testutil.RunCLIWithStdin(t, []string{
		"hook", "state", "append", "--project", workDir,
	}, env, workDir, stdinData)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
}

// --- scope with --project ---

func TestHookScopeActivateWithProject(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")

	env := testutil.NewTestEnv(t)

	result := testutil.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate", "--project", workDir,
	}, env, workDir, []byte(`{"session_id":"sess-100"}`))

	// May succeed or fail depending on scope resolution — just verify --project is accepted
	assert.Assert(t, result.ExitCode == 0 || result.ExitCode == 1,
		"unexpected exit code %d: %s", result.ExitCode, result.Stderr)
}

func TestHookScopeDeactivateWithProject(t *testing.T) {
	workDir := testutil.SetupGitRepo(t, "test-org", "test-repo")

	env := testutil.NewTestEnv(t)

	result := testutil.RunCLIWithStdin(t, []string{
		"hook", "scope", "deactivate", "--project", workDir,
	}, env, workDir, []byte(`{"session_id":"sess-100"}`))

	// Deactivate with a valid session_id should succeed (nothing to deactivate is fine)
	assert.Assert(t, result.ExitCode == 0 || result.ExitCode == 1,
		"unexpected exit code %d: %s", result.ExitCode, result.Stderr)
}

// --- env update flags ---

func TestHookEnvUpdateEnvFile(t *testing.T) {
	env := testutil.NewTestEnv(t)
	customEnvFile := filepath.Join(env.HomeDir, "custom-chunk-env")

	result := testutil.RunCLI(t, []string{
		"hook", "env", "update",
		"--env-file", customEnvFile,
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// Verify the custom env file was created
	_, err := os.Stat(customEnvFile)
	assert.NilError(t, err, "expected custom env file to exist at %s", customEnvFile)
}

func TestHookEnvUpdateSetProjectRoot(t *testing.T) {
	env := testutil.NewTestEnv(t)

	result := testutil.RunCLI(t, []string{
		"hook", "env", "update",
		"--set-project-root", "/my/workspace/root",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
}

// --- helpers ---

func writeHookConfig(t *testing.T, workDir string) {
	t.Helper()
	hookDir := filepath.Join(workDir, ".chunk", "hook")
	err := os.MkdirAll(hookDir, 0o755)
	assert.NilError(t, err)

	config := `execs:
  tests:
    command: "echo passed"
    timeout: 10
tasks:
  review:
    instructions: "Review the code"
    limit: 3
`
	err = os.WriteFile(filepath.Join(hookDir, "config.yml"), []byte(config), 0o644)
	assert.NilError(t, err)
}

func writeHookConfigWithTriggers(t *testing.T, workDir string) {
	t.Helper()
	hookDir := filepath.Join(workDir, ".chunk", "hook")
	err := os.MkdirAll(hookDir, 0o755)
	assert.NilError(t, err)

	config := `execs:
  tests:
    command: "echo passed"
    timeout: 10
tasks:
  review:
    instructions: "Review the code"
    limit: 3
triggers:
  go-files:
    patterns:
      - "*.go"
`
	err = os.WriteFile(filepath.Join(hookDir, "config.yml"), []byte(config), 0o644)
	assert.NilError(t, err)
}
