package acceptance

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

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
			profile:  "disable",
			contains: []string{"export CHUNK_HOOK_ENABLE=0", "Profile: disable"},
			absent:   []string{"export CHUNK_HOOK_ENABLE_TESTS"},
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

	for i := 0; i < 2; i++ {
		result := binary.RunCLI(t, []string{
			"hook", "env", "update",
		}, env, env.HomeDir)
		assert.Equal(t, result.ExitCode, 0, "run %d stderr: %s", i+1, result.Stderr)
	}

	zprofile := filepath.Join(env.HomeDir, ".zprofile")
	data, err := os.ReadFile(zprofile)
	assert.NilError(t, err)
	content := string(data)

	count := strings.Count(content, "# chunk-hook env")
	assert.Equal(t, count, 1, "expected exactly 1 marker, got %d in:\n%s", count, content)
}

func TestHookEnvUpdateLegacyMigration(t *testing.T) {
	env := testenv.NewTestEnv(t)

	legacyDir := filepath.Join(env.HomeDir, ".config", "chunk-hook")
	err := os.MkdirAll(legacyDir, 0o755)
	assert.NilError(t, err)

	legacyFile := filepath.Join(legacyDir, "env")
	err = os.WriteFile(legacyFile, []byte("export CHUNK_HOOK_ENABLE=1\n"), 0o644)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	_, err = os.Stat(legacyFile)
	assert.Assert(t, os.IsNotExist(err), "expected legacy file to be removed")

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

	assert.Assert(t, strings.Contains(content, `it'\''s-a-log-dir`),
		"expected escaped log dir, got:\n%s", content)
	assert.Assert(t, strings.Contains(content, `with'\''quote`),
		"expected escaped project root, got:\n%s", content)
}

func TestHookEnvUpdateCommentedHints(t *testing.T) {
	env := testenv.NewTestEnv(t)
	envFile := filepath.Join(env.HomeDir, "test-env")

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
		"--env-file", envFile,
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	data, err := os.ReadFile(envFile)
	assert.NilError(t, err)
	content := string(data)

	assert.Assert(t, strings.Contains(content, "# export CHUNK_HOOK_VERBOSE=1"),
		"expected commented verbose hint, got:\n%s", content)

	assert.Assert(t, strings.Contains(content, "# export CHUNK_HOOK_PROJECT_ROOT="),
		"expected commented project root hint, got:\n%s", content)

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

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate",
	}, env, env.HomeDir, []byte("{}"))

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

	result := binary.RunCLI(t, []string{
		"validate", "tests", "--no-check", "--project", workDir,
	}, env, workDir)

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
		"validate", "tests", "--no-check", "--project", workDir,
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
}

// --- exec run flags ---

func TestHookExecRunFlags(t *testing.T) {
	tests := []struct {
		name        string
		flags       []string
		useTriggers bool
	}{
		{"cmd override", []string{"--override-cmd", "echo overridden"}, false},
		{"always", []string{"--always"}, false},
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

			args := []string{"validate", "tests"}
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

	result := binary.RunCLI(t, []string{
		"validate", "tests", "--check",
		"--staged",
		"--always",
		"--on", "go-files",
		"--trigger", "*.go",
		"--matcher", "Write",
		"--limit", "3",
		"--project", workDir,
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "expected exit 0 (not enabled path), stderr: %s", result.Stderr)
}

// --- task check flags ---

func TestHookTaskCheckFlagsAccepted(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfigWithTriggers(t, workDir)

	instrFile := filepath.Join(workDir, "instructions.md")
	err := os.WriteFile(instrFile, []byte("Review the code"), 0o644)
	assert.NilError(t, err)

	schemaFile := filepath.Join(workDir, "schema.json")
	err = os.WriteFile(schemaFile, []byte(`{"type": "object"}`), 0o644)
	assert.NilError(t, err)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLI(t, []string{
		"validate", "review", "--task",
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

	result := binary.RunCLI(t, []string{
		"validate", "--sync",
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

	assert.Assert(t, result.ExitCode == 0 || result.ExitCode == 1,
		"unexpected exit code %d: %s", result.ExitCode, result.Stderr)
}

func TestHookScopeDeactivateWithProject(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	env := testenv.NewTestEnv(t)

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "deactivate", "--project", workDir,
	}, env, workDir, []byte(`{"session_id":"sess-100"}`))

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

// --- env update: options and shell variants ---

func TestHookEnvUpdateXDGConfigHome(t *testing.T) {
	env := testenv.NewTestEnv(t)
	customXDG := filepath.Join(env.HomeDir, "custom-xdg")
	env.Extra["XDG_CONFIG_HOME"] = customXDG

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	envFile := filepath.Join(customXDG, "chunk", "hook", "env")
	_, err := os.Stat(envFile)
	assert.NilError(t, err, "expected env file at %s", envFile)
}

func TestHookEnvUpdateLogDirCreated(t *testing.T) {
	env := testenv.NewTestEnv(t)
	logDir := filepath.Join(env.HomeDir, "deep", "nested", "logs")

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
		"--env-file", filepath.Join(env.HomeDir, "test-env"),
		"--set-log-dir", logDir,
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	info, err := os.Stat(logDir)
	assert.NilError(t, err, "expected log directory to be created at %s", logDir)
	assert.Assert(t, info.IsDir(), "expected %s to be a directory", logDir)
}

func TestHookEnvUpdateLogDirInEnvFile(t *testing.T) {
	env := testenv.NewTestEnv(t)
	envFile := filepath.Join(env.HomeDir, "test-env")
	logDir := filepath.Join(env.HomeDir, "my-logs")

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
		"--env-file", envFile,
		"--set-log-dir", logDir,
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	data, err := os.ReadFile(envFile)
	assert.NilError(t, err)
	content := string(data)
	assert.Assert(t, strings.Contains(content, fmt.Sprintf("export CHUNK_HOOK_LOG_DIR='%s'", logDir)),
		"expected CHUNK_HOOK_LOG_DIR in env file, got:\n%s", content)
}

func TestHookEnvUpdateProjectRootInEnvFile(t *testing.T) {
	env := testenv.NewTestEnv(t)
	envFile := filepath.Join(env.HomeDir, "test-env")

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
		"--env-file", envFile,
		"--set-project-root", "/my/workspace",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	data, err := os.ReadFile(envFile)
	assert.NilError(t, err)
	content := string(data)
	assert.Assert(t, strings.Contains(content, "export CHUNK_HOOK_PROJECT_ROOT='/my/workspace'"),
		"expected uncommented CHUNK_HOOK_PROJECT_ROOT export, got:\n%s", content)
	assert.Assert(t, !strings.Contains(content, "# export CHUNK_HOOK_PROJECT_ROOT="),
		"expected CHUNK_HOOK_PROJECT_ROOT to be uncommented, got:\n%s", content)
}

func TestHookEnvUpdateVerboseInEnvFile(t *testing.T) {
	env := testenv.NewTestEnv(t)
	envFile := filepath.Join(env.HomeDir, "test-env")

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
		"--env-file", envFile,
		"--set-verbose",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	data, err := os.ReadFile(envFile)
	assert.NilError(t, err)
	content := string(data)
	assert.Assert(t, strings.Contains(content, "export CHUNK_HOOK_VERBOSE=1"),
		"expected CHUNK_HOOK_VERBOSE=1 in env file, got:\n%s", content)
	assert.Assert(t, !strings.Contains(content, "# export CHUNK_HOOK_VERBOSE=1"),
		"expected CHUNK_HOOK_VERBOSE to be uncommented, got:\n%s", content)
}

func TestHookEnvUpdateBashShellBashProfile(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("bash_profile test only applies on macOS")
	}

	env := testenv.NewTestEnv(t)
	env.Extra["SHELL"] = "/bin/bash"

	result := binary.RunCLI(t, []string{
		"hook", "env", "update",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	bashProfile := filepath.Join(env.HomeDir, ".bash_profile")
	data, err := os.ReadFile(bashProfile)
	assert.NilError(t, err, "expected .bash_profile to exist")
	assert.Assert(t, strings.Contains(string(data), "# chunk-hook env"),
		"expected marker in .bash_profile")
}

// --- scope activate ---

func TestHookScopeActivateHappyPath(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	stdin := fmt.Sprintf(`{
		"session_id": "sess-activate-1",
		"tool_input": {"file_path": "%s/main.go"}
	}`, workDir)

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate", "--project", workDir,
	}, env, workDir, []byte(stdin))

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	markerPath := filepath.Join(workDir, ".chunk", "hook", ".chunk-hook-active")
	data, err := os.ReadFile(markerPath)
	assert.NilError(t, err, "expected marker file at %s", markerPath)

	var marker struct {
		SessionID string `json:"sessionId"`
		Timestamp int64  `json:"timestamp"`
	}
	err = json.Unmarshal(data, &marker)
	assert.NilError(t, err)
	assert.Equal(t, marker.SessionID, "sess-activate-1")
	assert.Assert(t, marker.Timestamp > 0, "expected positive timestamp")
}

func TestHookScopeActivateNonMatchingPaths(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	stdin := `{
		"session_id": "sess-mismatch",
		"tool_input": {"file_path": "/some/other/project/main.go"}
	}`

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate", "--project", workDir,
	}, env, workDir, []byte(stdin))

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	markerPath := filepath.Join(workDir, ".chunk", "hook", ".chunk-hook-active")
	_, err := os.Stat(markerPath)
	assert.Assert(t, os.IsNotExist(err), "expected no marker for non-matching paths")
}

func TestHookScopeActivatePathVariants(t *testing.T) {
	pathKeys := []string{"file_path", "filePath", "path", "directory"}

	for _, key := range pathKeys {
		t.Run(key, func(t *testing.T) {
			workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
			writeHookConfig(t, workDir)

			env := testenv.NewTestEnv(t)
			env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

			stdin := fmt.Sprintf(`{
				"session_id": "sess-variant",
				"tool_input": {"%s": "%s/src/file.go"}
			}`, key, workDir)

			result := binary.RunCLIWithStdin(t, []string{
				"hook", "scope", "activate", "--project", workDir,
			}, env, workDir, []byte(stdin))

			assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

			markerPath := filepath.Join(workDir, ".chunk", "hook", ".chunk-hook-active")
			_, err := os.Stat(markerPath)
			assert.NilError(t, err, "expected marker for path key %q", key)
		})
	}
}

func TestHookScopeActivateSessionConflictNotExpired(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	stdinA := fmt.Sprintf(`{
		"session_id": "sess-A",
		"tool_input": {"file_path": "%s/main.go"}
	}`, workDir)
	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate", "--project", workDir,
	}, env, workDir, []byte(stdinA))
	assert.Equal(t, result.ExitCode, 0)

	stdinB := fmt.Sprintf(`{
		"session_id": "sess-B",
		"tool_input": {"file_path": "%s/main.go"}
	}`, workDir)
	result = binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate", "--project", workDir,
	}, env, workDir, []byte(stdinB))
	assert.Equal(t, result.ExitCode, 0)

	markerPath := filepath.Join(workDir, ".chunk", "hook", ".chunk-hook-active")
	data, err := os.ReadFile(markerPath)
	assert.NilError(t, err)
	var marker struct {
		SessionID string `json:"sessionId"`
	}
	err = json.Unmarshal(data, &marker)
	assert.NilError(t, err)
	assert.Equal(t, marker.SessionID, "sess-A", "expected session-A to keep the marker")
}

func TestHookScopeActivateSessionConflictExpired(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()
	env.Extra["CHUNK_HOOK_MARKER_TTL_MS"] = "1"

	stdinA := fmt.Sprintf(`{
		"session_id": "sess-A",
		"tool_input": {"file_path": "%s/main.go"}
	}`, workDir)
	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate", "--project", workDir,
	}, env, workDir, []byte(stdinA))
	assert.Equal(t, result.ExitCode, 0)

	stdinB := fmt.Sprintf(`{
		"session_id": "sess-B",
		"tool_input": {"file_path": "%s/main.go"}
	}`, workDir)
	result = binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate", "--project", workDir,
	}, env, workDir, []byte(stdinB))
	assert.Equal(t, result.ExitCode, 0)

	markerPath := filepath.Join(workDir, ".chunk", "hook", ".chunk-hook-active")
	data, err := os.ReadFile(markerPath)
	assert.NilError(t, err)
	var marker struct {
		SessionID string `json:"sessionId"`
	}
	err = json.Unmarshal(data, &marker)
	assert.NilError(t, err)
	assert.Equal(t, marker.SessionID, "sess-B", "expected session-B to reclaim the marker")
}

func TestHookScopeActivateClaudeProjectDirFallback(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()
	env.Extra["CLAUDE_PROJECT_DIR"] = workDir

	stdin := fmt.Sprintf(`{
		"session_id": "sess-cpd",
		"tool_input": {"file_path": "%s/main.go"}
	}`, workDir)

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate",
	}, env, workDir, []byte(stdin))

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	markerPath := filepath.Join(workDir, ".chunk", "hook", ".chunk-hook-active")
	_, err := os.Stat(markerPath)
	assert.NilError(t, err, "expected marker via CLAUDE_PROJECT_DIR fallback")
}

// --- scope deactivate ---

func TestHookScopeDeactivateHappyPath(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	stdinActivate := fmt.Sprintf(`{
		"session_id": "sess-deact",
		"tool_input": {"file_path": "%s/main.go"}
	}`, workDir)
	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate", "--project", workDir,
	}, env, workDir, []byte(stdinActivate))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "deactivate", "--project", workDir,
	}, env, workDir, []byte(`{"session_id": "sess-deact"}`))
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	markerPath := filepath.Join(workDir, ".chunk", "hook", ".chunk-hook-active")
	_, err := os.Stat(markerPath)
	assert.Assert(t, os.IsNotExist(err), "expected marker to be removed after deactivate")
}

func TestHookScopeDeactivateDifferentSession(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	stdinActivate := fmt.Sprintf(`{
		"session_id": "sess-A",
		"tool_input": {"file_path": "%s/main.go"}
	}`, workDir)
	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate", "--project", workDir,
	}, env, workDir, []byte(stdinActivate))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "deactivate", "--project", workDir,
	}, env, workDir, []byte(`{"session_id": "sess-B"}`))
	assert.Equal(t, result.ExitCode, 0)

	markerPath := filepath.Join(workDir, ".chunk", "hook", ".chunk-hook-active")
	_, err := os.Stat(markerPath)
	assert.NilError(t, err, "expected marker to remain when different session deactivates")
}

func TestHookScopeDeactivateClaudeProjectDirFallback(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()
	env.Extra["CLAUDE_PROJECT_DIR"] = workDir

	stdinActivate := fmt.Sprintf(`{
		"session_id": "sess-cpd-d",
		"tool_input": {"file_path": "%s/main.go"}
	}`, workDir)
	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate", "--project", workDir,
	}, env, workDir, []byte(stdinActivate))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "deactivate",
	}, env, workDir, []byte(`{"session_id": "sess-cpd-d"}`))
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	markerPath := filepath.Join(workDir, ".chunk", "hook", ".chunk-hook-active")
	_, err := os.Stat(markerPath)
	assert.Assert(t, os.IsNotExist(err), "expected marker removed via CLAUDE_PROJECT_DIR fallback")
}

// --- state save/load ---

func TestHookStateSaveLoadRoundTrip(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	stdinData := []byte(`{"hook_event_name":"Stop","session_id":"sess-rt","reason":"done"}`)
	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, stdinData)
	assert.Equal(t, result.ExitCode, 0, "save stderr: %s", result.Stderr)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "load stderr: %s", result.Stderr)

	var state map[string]interface{}
	err := json.Unmarshal([]byte(result.Stdout), &state)
	assert.NilError(t, err, "expected valid JSON, got: %s", result.Stdout)
	assert.Assert(t, state["Stop"] != nil, "expected Stop event in state")
}

func TestHookStateSaveOverwritesSameEvent(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"Stop","session_id":"sess-ow","reason":"first"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"Stop","session_id":"sess-ow","reason":"second"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "Stop.reason", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	assert.Equal(t, strings.TrimSpace(result.Stdout), "second")
}

func TestHookStateSaveSessionReset(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"EventA","session_id":"sess-A","data":"alpha"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"EventB","session_id":"sess-B","data":"beta"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	assert.Assert(t, !strings.Contains(result.Stdout, "EventA"),
		"expected EventA to be cleared after session change, got: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "EventB"),
		"expected EventB in state, got: %s", result.Stdout)
}

func TestHookStateAppendLoadRoundTrip(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "append", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"ToolUse","session_id":"sess-ap","tool":"Bash"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	assert.Assert(t, strings.Contains(result.Stdout, "__entries"),
		"expected __entries in state, got: %s", result.Stdout)
}

func TestHookStateAppendMultiple(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "append", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"ToolUse","session_id":"sess-multi","tool":"Bash"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLIWithStdin(t, []string{
		"hook", "state", "append", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"ToolUse","session_id":"sess-multi","tool":"Edit"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)

	var state map[string]interface{}
	err := json.Unmarshal([]byte(result.Stdout), &state)
	assert.NilError(t, err)

	toolUse, ok := state["ToolUse"].(map[string]interface{})
	assert.Assert(t, ok, "expected ToolUse in state")
	entries, ok := toolUse["__entries"].([]interface{})
	assert.Assert(t, ok, "expected __entries array")
	assert.Equal(t, len(entries), 2, "expected 2 entries")
}

func TestHookStateAppendSessionReset(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "append", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"ToolUse","session_id":"sess-A","tool":"Bash"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLIWithStdin(t, []string{
		"hook", "state", "append", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"ToolUse","session_id":"sess-B","tool":"Edit"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)

	var state map[string]interface{}
	err := json.Unmarshal([]byte(result.Stdout), &state)
	assert.NilError(t, err)
	toolUse, ok := state["ToolUse"].(map[string]interface{})
	assert.Assert(t, ok)
	entries, ok := toolUse["__entries"].([]interface{})
	assert.Assert(t, ok)
	assert.Equal(t, len(entries), 1, "expected 1 entry after session reset")
}

// --- state load: field access ---

func TestHookStateLoadDotNotation(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"UserPromptSubmit","session_id":"sess-dot","prompt":"hello world"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "UserPromptSubmit.prompt", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	assert.Equal(t, strings.TrimSpace(result.Stdout), "hello world")
}

func TestHookStateLoadDefaultEntriesSugar(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "append", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"ToolUse","session_id":"sess-idx","tool":"Bash"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLIWithStdin(t, []string{
		"hook", "state", "append", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"ToolUse","session_id":"sess-idx","tool":"Edit"}`))
	assert.Equal(t, result.ExitCode, 0)

	// The __entries sugar redirects ToolUse.tool to __entries[0].tool
	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "ToolUse.tool", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	assert.Equal(t, strings.TrimSpace(result.Stdout), "Bash",
		"default __entries sugar should return first entry's tool")
}

func TestHookStateLoadStringAsPlainText(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"Ev","session_id":"sess-str","text":"plain value"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "Ev.text", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	output := strings.TrimSpace(result.Stdout)
	assert.Assert(t, !strings.HasPrefix(output, "\""), "expected plain text, not JSON string: %s", output)
	assert.Equal(t, output, "plain value")
}

func TestHookStateLoadNonStringAsJSON(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"Ev","session_id":"sess-num","count":42}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "Ev.count", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	assert.Equal(t, strings.TrimSpace(result.Stdout), "42")
}

func TestHookStateLoadNonexistentField(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"Ev","session_id":"sess-ne","val":"x"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "Ev.nonexistent", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	assert.Equal(t, strings.TrimSpace(result.Stdout), "")
}

// --- state clear ---

func TestHookStateClearVerifyFileDeletion(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"Ev","session_id":"sess-del","val":"x"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	assert.Assert(t, strings.TrimSpace(result.Stdout) != "{}" && strings.TrimSpace(result.Stdout) != "",
		"expected non-empty state before clear")

	result = binary.RunCLIWithStdin(t, []string{
		"hook", "state", "clear", "--project", workDir,
	}, env, workDir, []byte(`{"session_id":"sess-del"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	trimmed := strings.TrimSpace(result.Stdout)
	assert.Assert(t, trimmed == "{}" || trimmed == "",
		"expected empty state after clear, got: %s", trimmed)
}

func TestHookStateClearSessionMismatchSkips(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, []byte(`{"hook_event_name":"Ev","session_id":"sess-A","val":"keep"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLIWithStdin(t, []string{
		"hook", "state", "clear", "--project", workDir,
	}, env, workDir, []byte(`{"session_id":"sess-B"}`))
	assert.Equal(t, result.ExitCode, 0)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "Ev.val", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	assert.Equal(t, strings.TrimSpace(result.Stdout), "keep")
}

func TestHookStateClearWithoutPriorState(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "clear", "--project", workDir,
	}, env, workDir, []byte(`{"session_id":"sess-empty"}`))
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
}

// --- end-to-end lifecycle ---

func TestHookStateFullLifecycle(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	sessionID := "sess-lifecycle"

	// 1. Save an event
	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", workDir,
	}, env, workDir, []byte(fmt.Sprintf(`{"hook_event_name":"UserPromptSubmit","session_id":"%s","prompt":"build it"}`, sessionID)))
	assert.Equal(t, result.ExitCode, 0)

	// 2. Append another event
	result = binary.RunCLIWithStdin(t, []string{
		"hook", "state", "append", "--project", workDir,
	}, env, workDir, []byte(fmt.Sprintf(`{"hook_event_name":"ToolUse","session_id":"%s","tool":"Bash"}`, sessionID)))
	assert.Equal(t, result.ExitCode, 0)

	// 3. Load and verify both events exist
	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	assert.Assert(t, strings.Contains(result.Stdout, "UserPromptSubmit"))
	assert.Assert(t, strings.Contains(result.Stdout, "ToolUse"))

	// 4. Load specific field
	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "UserPromptSubmit.prompt", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	assert.Equal(t, strings.TrimSpace(result.Stdout), "build it")

	// 5. Clear
	result = binary.RunCLIWithStdin(t, []string{
		"hook", "state", "clear", "--project", workDir,
	}, env, workDir, []byte(fmt.Sprintf(`{"session_id":"%s"}`, sessionID)))
	assert.Equal(t, result.ExitCode, 0)

	// 6. Verify empty
	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "--project", workDir,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	trimmed := strings.TrimSpace(result.Stdout)
	assert.Assert(t, trimmed == "{}" || trimmed == "",
		"expected empty state after clear, got: %s", trimmed)
}

func TestHookScopeActivateDeactivateLifecycle(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()

	markerPath := filepath.Join(workDir, ".chunk", "hook", ".chunk-hook-active")

	// 1. Activate
	stdinActivate := fmt.Sprintf(`{
		"session_id": "sess-life",
		"tool_input": {"file_path": "%s/main.go"}
	}`, workDir)
	result := binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "activate", "--project", workDir,
	}, env, workDir, []byte(stdinActivate))
	assert.Equal(t, result.ExitCode, 0)

	_, err := os.Stat(markerPath)
	assert.NilError(t, err, "expected marker after activate")

	// 2. Deactivate
	result = binary.RunCLIWithStdin(t, []string{
		"hook", "scope", "deactivate", "--project", workDir,
	}, env, workDir, []byte(`{"session_id": "sess-life"}`))
	assert.Equal(t, result.ExitCode, 0)

	_, err = os.Stat(markerPath)
	assert.Assert(t, os.IsNotExist(err), "expected marker removed after deactivate")
}

// --- env var coverage ---

func TestHookProjectRootRelativeResolution(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeHookConfig(t, workDir)

	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_HOOK_SENTINELS_DIR"] = t.TempDir()
	env.Extra["CHUNK_HOOK_PROJECT_ROOT"] = filepath.Dir(workDir)

	dirName := filepath.Base(workDir)

	result := binary.RunCLIWithStdin(t, []string{
		"hook", "state", "save", "--project", dirName,
	}, env, workDir, []byte(`{"hook_event_name":"Ev","session_id":"sess-rel","val":"resolved"}`))
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	result = binary.RunCLI(t, []string{
		"hook", "state", "load", "Ev.val", "--project", dirName,
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0)
	assert.Equal(t, strings.TrimSpace(result.Stdout), "resolved")
}

// --- helpers ---

func writeHookConfig(t *testing.T, workDir string) {
	t.Helper()
	chunkDir := filepath.Join(workDir, ".chunk")
	err := os.MkdirAll(chunkDir, 0o755)
	assert.NilError(t, err)

	config := `{
  "commands": [{"name": "tests", "run": "echo passed", "timeout": 10}],
  "tasks": {"review": {"instructions": "Review the code", "limit": 3}}
}`
	err = os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(config), 0o644)
	assert.NilError(t, err)
}

func writeHookConfigWithTriggers(t *testing.T, workDir string) {
	t.Helper()
	chunkDir := filepath.Join(workDir, ".chunk")
	err := os.MkdirAll(chunkDir, 0o755)
	assert.NilError(t, err)

	config := `{
  "commands": [{"name": "tests", "run": "echo passed", "timeout": 10}],
  "tasks": {"review": {"instructions": "Review the code", "limit": 3}},
  "triggers": {"go-files": ["*.go"]}
}`
	err = os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(config), 0o644)
	assert.NilError(t, err)
}
