package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
)

func TestSkillsInstall(t *testing.T) {
	env := testenv.NewTestEnv(t)

	claudeDir := filepath.Join(env.HomeDir, ".claude")
	err := os.MkdirAll(claudeDir, 0o755)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{"skill", "install"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	// Claude skills should be installed.
	assert.Assert(t, strings.Contains(combined, "claude:"),
		"expected per-agent output for claude, got: %s", combined)

	for _, name := range []string{"chunk-review", "chunk-testing-gaps", "debug-ci-failures"} {
		skillFile := filepath.Join(claudeDir, "skills", name, "SKILL.md")
		info, err := os.Stat(skillFile)
		assert.NilError(t, err, "expected skill %s to exist", name)
		assert.Assert(t, info.Size() > 0, "expected skill %s to be non-empty", name)
	}
}

func TestSkillsInstallSkipsUnavailableAgent(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// Only create .claude, not .codex.
	claudeDir := filepath.Join(env.HomeDir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	result := binary.RunCLI(t, []string{"skill", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "codex: skipped"),
		"expected codex skipped message, got: %s", combined)

	// .codex dir should not have been created.
	_, err := os.Stat(filepath.Join(env.HomeDir, ".codex", "skills"))
	assert.Assert(t, os.IsNotExist(err), "should not create .codex/skills")
}

func TestSkillsInstallUpToDate(t *testing.T) {
	env := testenv.NewTestEnv(t)
	claudeDir := filepath.Join(env.HomeDir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	// First install.
	binary.RunCLI(t, []string{"skill", "install"}, env, env.HomeDir)

	// Second install should show "up to date".
	result := binary.RunCLI(t, []string{"skill", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "up to date"),
		"expected up-to-date message on second install, got: %s", combined)
}

func TestSkillsList(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"skill", "list"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "chunk-review"),
		"expected 'chunk-review' in output, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "chunk-testing-gaps"),
		"expected 'chunk-testing-gaps' in output, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "debug-ci-failures"),
		"expected 'debug-ci-failures' in output, got: %s", combined)
	// Should show per-agent status.
	assert.Assert(t, strings.Contains(combined, "claude:"),
		"expected per-agent status for claude, got: %s", combined)
}

func TestSkillsListShowsDescriptions(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"skill", "list"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	// Check for a fragment from one of the descriptions.
	assert.Assert(t, strings.Contains(combined, "mutation test"),
		"expected skill description in output, got: %s", combined)
}
