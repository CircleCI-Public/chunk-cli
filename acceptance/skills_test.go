package acceptance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
)

// setupFakeClaude writes a fake claude script to a temp bin dir and returns
// the dir. The script logs every invocation to a file and responds to the
// recognised plugin subcommands with canned output.
func setupFakeClaude(t *testing.T) (binDir, logFile string) {
	t.Helper()
	binDir = t.TempDir()
	logFile = filepath.Join(binDir, "invocations.log")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %s
case "$*" in
  "plugin install circleci --yes --scope project")
    echo "Installing plugin... Successfully installed plugin: circleci@circleci-claude-marketplace (scope: project)"
    exit 0;;
  "plugin install circleci --yes --scope user")
    echo "Installing plugin... Successfully installed plugin: circleci@circleci-claude-marketplace (scope: user)"
    exit 0;;
  "plugin list --json")
    echo '[{"id":"circleci@circleci-claude-marketplace","scope":"project","enabled":true}]'
    exit 0;;
  *)
    echo "unexpected args: $*" >&2
    exit 1;;
esac
`, logFile)
	assert.NilError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755))
	return binDir, logFile
}

func withFakeClaude(env *testenv.TestEnv, binDir string) {
	env.Extra["PATH"] = binDir + ":" + os.Getenv("PATH")
}

func TestSkillsInstall(t *testing.T) {
	env := testenv.NewTestEnv(t)
	binDir, logFile := setupFakeClaude(t)
	withFakeClaude(env, binDir)

	result := binary.RunCLI(t, []string{"skill", "install"}, env, t.TempDir())

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "claude:"),
		"expected per-agent output, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "circleci"),
		"expected plugin name in output, got: %s", combined)

	log, _ := os.ReadFile(logFile)
	assert.Assert(t, strings.Contains(string(log), "plugin install circleci"),
		"expected plugin install to be called, got: %s", string(log))
}

func TestSkillsInstallUserScope(t *testing.T) {
	env := testenv.NewTestEnv(t)
	binDir, logFile := setupFakeClaude(t)
	withFakeClaude(env, binDir)

	result := binary.RunCLI(t, []string{"skill", "install", "--user"}, env, t.TempDir())
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	log, _ := os.ReadFile(logFile)
	assert.Assert(t, strings.Contains(string(log), "--scope user"),
		"expected --scope user passed to claude, got: %s", string(log))
}

func TestSkillsInstallSkipsWhenCLIAbsent(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["PATH"] = t.TempDir() // isolated PATH — no claude available

	result := binary.RunCLI(t, []string{"skill", "install"}, env, t.TempDir())
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "skipped"),
		"expected skipped message when claude not found, got: %s", combined)
}

func TestSkillsInstallMutuallyExclusiveFlags(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"skill", "install", "--user", "--project"}, env, t.TempDir())
	assert.Assert(t, result.ExitCode != 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "mutually exclusive"),
		"expected 'mutually exclusive' error, got: %s", combined)
}

func TestSkillsInstallUserScopeNoHomeNeeded(t *testing.T) {
	// --user no longer requires HOME since we delegate to the claude CLI.
	env := testenv.NewTestEnv(t)
	env.HomeDir = ""
	binDir, logFile := setupFakeClaude(t)
	env.Extra["PATH"] = binDir + ":" + os.Getenv("PATH")

	result := binary.RunCLI(t, []string{"skill", "install", "--user"}, env, t.TempDir())
	assert.Equal(t, result.ExitCode, 0,
		"expected success even without HOME, stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	log, _ := os.ReadFile(logFile)
	assert.Assert(t, strings.Contains(string(log), "--scope user"),
		"expected --scope user in call, got: %s", string(log))
}

func TestSkillsList(t *testing.T) {
	env := testenv.NewTestEnv(t)
	binDir, _ := setupFakeClaude(t)
	withFakeClaude(env, binDir)

	result := binary.RunCLI(t, []string{"skill", "list"}, env, t.TempDir())
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	for _, name := range []string{"chunk-review", "chunk-sidecar", "chunk-testing-gaps", "debug-ci-failures"} {
		assert.Assert(t, strings.Contains(combined, name),
			"expected skill %s in list output, got: %s", name, combined)
	}
	assert.Assert(t, strings.Contains(combined, "claude:"),
		"expected per-agent status line, got: %s", combined)
}

func TestSkillsListShowsCurrentWhenInstalled(t *testing.T) {
	env := testenv.NewTestEnv(t)
	binDir, _ := setupFakeClaude(t)
	withFakeClaude(env, binDir)

	result := binary.RunCLI(t, []string{"skill", "list"}, env, t.TempDir())
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "current"),
		"expected 'current' state, got: %s", combined)
}

func TestSkillsListShowsMissingWhenNotInstalled(t *testing.T) {
	env := testenv.NewTestEnv(t)
	// Fake claude that returns empty plugin list.
	binDir := t.TempDir()
	script := "#!/bin/sh\ncase \"$*\" in\n  \"plugin list --json\") echo '[]'; exit 0;;\n  *) exit 1;;\nesac\n"
	assert.NilError(t, os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755))
	withFakeClaude(env, binDir)

	result := binary.RunCLI(t, []string{"skill", "list"}, env, t.TempDir())
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "missing"),
		"expected 'missing' state when not installed, got: %s", combined)
}
