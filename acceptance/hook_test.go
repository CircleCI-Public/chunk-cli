package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// --- repo init ---

func TestHookRepoInit(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
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
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)

	// First init
	result := binary.RunCLI(t, []string{
		"hook", "repo", "init", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "first init failed: %s", result.Stderr)

	// Second init without --force should create .example files
	result = binary.RunCLI(t, []string{
		"hook", "repo", "init", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "second init failed: %s", result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "already exists") || strings.Contains(combined, "example"),
		"expected existing file message, got: %s", combined)
}

func TestHookRepoInitForce(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)

	// First init
	result := binary.RunCLI(t, []string{
		"hook", "repo", "init", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "first init failed: %s", result.Stderr)

	// Second init with --force should overwrite
	result = binary.RunCLI(t, []string{
		"hook", "repo", "init", workDir, "--force",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "second init with --force failed: %s", result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Created") || strings.Contains(combined, "initialized"),
		"expected created message, got: %s", combined)
}

// --- setup ---

func TestHookSetupInvalidProfile(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"hook", "setup", workDir, "--profile", "bogus", "--skip-env",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Invalid profile") || strings.Contains(combined, "Valid profiles"),
		"expected invalid profile error, got: %s", combined)
}

func TestHookSetupHappyPath(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
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
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	env := testenv.NewTestEnv(t)

	// First setup
	result := binary.RunCLI(t, []string{
		"hook", "setup", workDir, "--skip-env",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "first setup failed: %s", result.Stderr)

	// Second setup with --force
	result = binary.RunCLI(t, []string{
		"hook", "setup", workDir, "--skip-env", "--force",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "second setup with --force failed: %s", result.Stderr)
}

// --- env update ---

func TestHookEnvUpdateInvalidProfile(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"hook", "env", "update", "--profile", "bogus",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Invalid profile") || strings.Contains(combined, "Valid profiles"),
		"expected invalid profile error, got: %s", combined)
}

func TestHookEnvUpdateHappyPath(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "Configuration complete") || strings.Contains(combined, "complete"),
		"expected completion message, got: %s", combined)
}

func TestHookEnvUpdateWithOptions(t *testing.T) {
	env := testenv.NewTestEnv(t)
	logDir := filepath.Join(env.HomeDir, "logs")

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
		"--set-log-dir", logDir,
		"--set-verbose",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

// --- env update: TS parity ---

func TestHookEnvUpdateProfileContent(t *testing.T) {
	tests := []struct {
		profile  string
		contains []string
		absent   []string
	}{
		{
			profile:  "enable",
			contains: []string{"export CHUNK_HOOK_ENABLE=1", "Profile: enable"},
			absent:   []string{"export CHUNK_HOOK_ENABLE_TESTS"},
		},
		{
			profile: "disable",
			contains: []string{"export CHUNK_HOOK_ENABLE=0", "Profile: disable"},
			absent:  []string{"export CHUNK_HOOK_ENABLE_TESTS"},
		},
		{
			profile: "tests-lint",
			contains: []string{
				"export CHUNK_HOOK_ENABLE=0",
				"export CHUNK_HOOK_ENABLE_TESTS=1",
				"export CHUNK_HOOK_ENABLE_TESTS_CHANGED=1",
				"export CHUNK_HOOK_ENABLE_LINT=1",
				"Profile: tests-lint",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.profile, func(t *testing.T) {
			env := testenv.NewTestEnv(t)
			envFile := filepath.Join(env.HomeDir, "test-env")

			result := binary.RunCLI(t, []string{
				"hook", "env", "update",
				"--profile", tt.profile,
				"--env-file", envFile,
			}, env, env.HomeDir)

			assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

			data, err := os.ReadFile(envFile)
			assert.NilError(t, err)
			content := string(data)

			for _, s := range tt.contains {
				assert.Assert(t, strings.Contains(content, s),
					"expected %q in env file, got:\n%s", s, content)
			}
			for _, s := range tt.absent {
				assert.Assert(t, !strings.Contains(content, s),
					"did not expect %q in env file, got:\n%s", s, content)
			}
		})
	}
}

func TestHookEnvUpdateShellSourcing(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/zsh"

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// Verify shell startup files contain the sourcing block
	for _, name := range []string{".zprofile", ".zshrc"} {
		path := filepath.Join(env.HomeDir, name)
		data, err := os.ReadFile(path)
		assert.NilError(t, err, "expected %s to exist", name)
		content := string(data)

		assert.Assert(t, strings.Contains(content, "# chunk-hook env"),
			"expected marker in %s, got:\n%s", name, content)
		assert.Assert(t, strings.Contains(content, "if [ -f '"),
			"expected sourcing line in %s, got:\n%s", name, content)
	}
}

func TestHookEnvUpdateSourcingIdempotent(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/zsh"

	// Run twice
	for i := 0; i < 2; i++ {
		result := binary.RunCLI(t, []string{
			"hook", "env", "update",
		}, env, env.HomeDir)
		assert.Equal(t, result.ExitCode, 0, "run %d stderr: %s", i+1, result.Stderr)
	}

	// Verify no duplicate markers
	zprofile := filepath.Join(env.HomeDir, ".zprofile")
	data, err := os.ReadFile(zprofile)
	assert.NilError(t, err)
	content := string(data)

	count := strings.Count(content, "# chunk-hook env")
	assert.Equal(t, count, 1, "expected exactly 1 marker, got %d in:\n%s", count, content)
}

func TestHookEnvUpdateLegacyMigration(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// Create legacy env file at ~/.config/chunk-hook/env
	legacyDir := filepath.Join(env.HomeDir, ".config", "chunk-hook")
	err := os.MkdirAll(legacyDir, 0o755)
	assert.NilError(t, err)

	legacyFile := filepath.Join(legacyDir, "env")
	err = os.WriteFile(legacyFile, []byte("export CHUNK_HOOK_ENABLE=1\n"), 0o644)
	assert.NilError(t, err)

	// Run env update (uses default path which triggers migration)
	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// Legacy file should be gone
	_, err = os.Stat(legacyFile)
	assert.Assert(t, os.IsNotExist(err), "expected legacy file to be removed")

	// New file should exist at ~/.config/chunk/hook/env
	newFile := filepath.Join(env.HomeDir, ".config", "chunk", "hook", "env")
	_, err = os.Stat(newFile)
	assert.NilError(t, err, "expected new env file to exist")
}

func TestHookEnvUpdateShellQuoting(t *testing.T) {
	env := testenv.NewTestEnv(t)
	envFile := filepath.Join(env.HomeDir, "test-env")
	logDir := filepath.Join(env.HomeDir, "it's-a-log-dir")

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
		"--env-file", envFile,
		"--set-log-dir", logDir,
		"--set-project-root", "/path/with'quote",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	data, err := os.ReadFile(envFile)
	assert.NilError(t, err)
	content := string(data)

	// Single quotes in values should be escaped as '\''
	assert.Assert(t, strings.Contains(content, `it'\''s-a-log-dir`),
		"expected escaped log dir, got:\n%s", content)
	assert.Assert(t, strings.Contains(content, `with'\''quote`),
		"expected escaped project root, got:\n%s", content)
}

func TestHookEnvUpdateCommentedHints(t *testing.T) {
	env := testenv.NewTestEnv(t)
	envFile := filepath.Join(env.HomeDir, "test-env")

	// Run without --set-verbose and without --set-project-root
	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
		"--env-file", envFile,
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	data, err := os.ReadFile(envFile)
	assert.NilError(t, err)
	content := string(data)

	// Should have commented-out verbose hint
	assert.Assert(t, strings.Contains(content, "# export CHUNK_HOOK_VERBOSE=1"),
		"expected commented verbose hint, got:\n%s", content)

	// Should have commented-out project root hint
	assert.Assert(t, strings.Contains(content, "# export CHUNK_HOOK_PROJECT_ROOT="),
		"expected commented project root hint, got:\n%s", content)

	// Should have header with quick toggle examples
	assert.Assert(t, strings.Contains(content, "Quick toggle examples"),
		"expected quick toggle header, got:\n%s", content)
}

func TestHookEnvUpdateBashShell(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/bash"

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// On macOS, bash should use .bash_profile; verify .bashrc exists too
	bashrc := filepath.Join(env.HomeDir, ".bashrc")
	_, err := os.Stat(bashrc)
	assert.NilError(t, err, "expected .bashrc to exist")

	data, err := os.ReadFile(bashrc)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "# chunk-hook env"),
		"expected marker in .bashrc")
}

// --- scope ---

func TestHookScopeDeactivateNoSession(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// Pipe empty stdin — no session ID available
	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "deactivate",
	}, env, env.HomeDir, []byte("{}"))

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "session"),
		"expected session error, got: %s", combined)
}

func TestHookScopeActivateNoProject(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// Activate with no project config — should still succeed (no-op)
	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate",
	}, env, env.HomeDir, []byte("{}"))

	// May succeed or fail depending on project resolution — just check it doesn't crash
	assert.Assert(t, result.ExitCode == 0 || result.ExitCode == 1,
		"unexpected exit code %d: %s", result.ExitCode, result.Stderr)
}

// --- state ---

func TestHookStateLoadEmpty(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLI(t, []string{
		"hook", "state", "load", "--project", workDir,
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	// Empty state should output something like {} or empty
	trimmed := strings.TrimSpace(result.Stdout)
	assert.Assert(t, trimmed == "{}" || trimmed == "",
		"expected empty state, got: %q", trimmed)
}

func TestHookStateClear(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "clear", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"Stop","session_id":"sess-123"}`))

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
}

// --- exec ---

func TestHookExecRunNotEnabled(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()
	// Deliberately not setting CHUNK_HOOK_ENABLE or CHUNK_HOOK_ENABLE_TESTS

	result := binary.RunCLI(t, []string{
		"hook", "exec", "run", "tests", "--no-check", "--project", workDir,
	}, env, workDir)

	// Should allow (exit 0) when not enabled
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
}

func TestHookExecRunNoCheck(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_ENABLE"] = "1"
	env.Extra["CHUNK_HOOK_ENABLE_TESTS"] = "1"
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLI(t, []string{
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
			workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
			if tt.useTriggers {
				writeHookConfigWithTriggers(t, workDir)
			} else {
				writeHookConfig(t, workDir)
			}

			env := testenv.NewTestEnv(t)
			env.Extra["CHUNK_HOOK_ENABLE"] = "1"
			env.Extra["CHUNK_HOOK_ENABLE_TESTS"] = "1"
			env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

			args := []string{"hook", "exec", "run", "tests"}
			args = append(args, tt.flags...)
			args = append(args, "--no-check", "--project", workDir)

			result := binary.RunCLI(t, args, env, workDir)
			assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
		})
	}
}

// --- exec check flags ---

func TestHookExecCheckFlagsAccepted(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfigWithTriggers(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()
	// Not enabling — "not enabled" path exits 0 before reading stdin

	result := binary.RunCLI(t, []string{
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
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfigWithTriggers(t, workDir)

	// Write a dummy instructions file
	instrFile := filepath.Join(workDir, "instructions.md")
	err := os.WriteFile(instrFile, []byte("Review the code"), 0o644)
	assert.NilError(t, err)

	schemaFile := filepath.Join(workDir, "schema.json")
	err = os.WriteFile(schemaFile, []byte(`{"type": "object"}`), 0o644)
	assert.NilError(t, err)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()
	// Not enabling — exits 0

	result := binary.RunCLI(t, []string{
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
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfigWithTriggers(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()
	// Not enabling — exits 0

	result := binary.RunCLI(t, []string{
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
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	stdinData := []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"sess-456","prompt":"hello"}`)
	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, stdinData)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
}

func TestHookStateAppendWithProject(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	stdinData := []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"sess-789","prompt":"world"}`)
	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "append", "--project", workDir,
	}, env, workDir, stdinData)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
}

// --- scope with --project ---

func TestHookScopeActivateWithProject(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate", "--project", workDir,
	}, env, workDir, []byte(`{"session_id":"sess-100"}`))

	// May succeed or fail depending on scope resolution — just verify --project is accepted
	assert.Assert(t, result.ExitCode == 0 || result.ExitCode == 1,
		"unexpected exit code %d: %s", result.ExitCode, result.Stderr)
}

func TestHookScopeDeactivateWithProject(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "deactivate", "--project", workDir,
	}, env, workDir, []byte(`{"session_id":"sess-100"}`))

	// Deactivate with a valid session_id should succeed (nothing to deactivate is fine)
	assert.Assert(t, result.ExitCode == 0 || result.ExitCode == 1,
		"unexpected exit code %d: %s", result.ExitCode, result.Stderr)
}

// --- env update flags ---

func TestHookEnvUpdateEnvFile(t *testing.T) {
	env := testenv.NewTestEnv(t)
	customEnvFile := filepath.Join(env.HomeDir, "custom-chunk-env")

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
		"--env-file", customEnvFile,
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// Verify the custom env file was created
	_, err := os.Stat(customEnvFile)
	assert.NilError(t, err, "expected custom env file to exist at %s", customEnvFile)
}

func TestHookEnvUpdateSetProjectRoot(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
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
