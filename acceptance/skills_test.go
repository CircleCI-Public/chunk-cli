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

	// Only create .claude, not .agents.
	claudeDir := filepath.Join(env.HomeDir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	result := binary.RunCLI(t, []string{"skill", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "codex: skipped"),
		"expected codex skipped message, got: %s", combined)

	// .agents dir should not have been created.
	_, err := os.Stat(filepath.Join(env.HomeDir, ".agents", "skills"))
	assert.Assert(t, os.IsNotExist(err), "should not create .agents/skills")
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

func TestSkillsInstallCodexPath(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// Only create .agents, not .claude.
	codexDir := filepath.Join(env.HomeDir, ".agents")
	assert.NilError(t, os.MkdirAll(codexDir, 0o755))

	result := binary.RunCLI(t, []string{"skill", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "codex:"),
		"expected per-agent output for codex, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "claude: skipped"),
		"expected claude skipped, got: %s", combined)

	for _, name := range []string{"chunk-review", "chunk-testing-gaps", "debug-ci-failures"} {
		skillFile := filepath.Join(codexDir, "skills", name, "SKILL.md")
		info, err := os.Stat(skillFile)
		assert.NilError(t, err, "expected skill %s to exist under .agents", name)
		assert.Assert(t, info.Size() > 0, "expected skill %s to be non-empty", name)
	}
}

func TestSkillsInstallBothAgents(t *testing.T) {
	env := testenv.NewTestEnv(t)

	claudeDir := filepath.Join(env.HomeDir, ".claude")
	codexDir := filepath.Join(env.HomeDir, ".agents")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))
	assert.NilError(t, os.MkdirAll(codexDir, 0o755))

	result := binary.RunCLI(t, []string{"skill", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "claude:"),
		"expected per-agent output for claude, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "codex:"),
		"expected per-agent output for codex, got: %s", combined)
	// Neither should be skipped.
	assert.Assert(t, !strings.Contains(combined, "skipped"),
		"expected no skipped agents, got: %s", combined)

	// Verify files exist under both agent dirs.
	for _, dir := range []string{claudeDir, codexDir} {
		for _, name := range []string{"chunk-review", "chunk-testing-gaps", "debug-ci-failures"} {
			skillFile := filepath.Join(dir, "skills", name, "SKILL.md")
			_, err := os.Stat(skillFile)
			assert.NilError(t, err, "expected skill %s under %s", name, dir)
		}
	}
}

func TestSkillsInstallOutdatedUpdate(t *testing.T) {
	env := testenv.NewTestEnv(t)
	claudeDir := filepath.Join(env.HomeDir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	// First install.
	result := binary.RunCLI(t, []string{"skill", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "first install failed: %s", result.Stderr)

	// Tamper with one skill file to make it outdated.
	tampered := filepath.Join(claudeDir, "skills", "chunk-review", "SKILL.md")
	assert.NilError(t, os.WriteFile(tampered, []byte("tampered content"), 0o644))

	// Re-run install: should detect outdated and update.
	result = binary.RunCLI(t, []string{"skill", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "updated"),
		"expected 'updated' message for tampered skill, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "chunk-review"),
		"expected chunk-review in update output, got: %s", combined)

	// Verify file content is restored (no longer tampered).
	restored, err := os.ReadFile(tampered)
	assert.NilError(t, err)
	assert.Assert(t, string(restored) != "tampered content",
		"expected content to be restored after update")
	assert.Assert(t, len(restored) > 100,
		"expected restored skill file to have substantial content, got %d bytes", len(restored))
}

func TestSkillsInstallNoAgentDirs(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// Don't create .claude or .agents.
	result := binary.RunCLI(t, []string{"skill", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "claude: skipped"),
		"expected claude skipped, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "codex: skipped"),
		"expected codex skipped, got: %s", combined)
}

func TestSkillsInstallHomeNotSet(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.HomeDir = ""

	result := binary.RunCLI(t, []string{"skill", "install"}, env, os.TempDir())
	assert.Assert(t, result.ExitCode != 0,
		"expected non-zero exit code when HOME is not set, got exit %d", result.ExitCode)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "HOME environment variable is not set"),
		"expected 'HOME environment variable is not set' error, got: %s", combined)
}

func TestSkillsListStateLabels(t *testing.T) {
	env := testenv.NewTestEnv(t)
	claudeDir := filepath.Join(env.HomeDir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	// Before install: .claude exists so skills should show "missing".
	result := binary.RunCLI(t, []string{"skill", "list"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "missing"),
		"expected 'missing' state before install, got: %s", combined)

	// .agents does not exist, so codex should show "n/a".
	assert.Assert(t, strings.Contains(combined, "n/a"),
		"expected 'n/a' for codex (not installed), got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "codex:"),
		"expected codex agent in list output, got: %s", combined)

	// Install skills.
	result = binary.RunCLI(t, []string{"skill", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "install failed: %s", result.Stderr)

	// After install: skills should show "current".
	result = binary.RunCLI(t, []string{"skill", "list"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0)
	combined = result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "current"),
		"expected 'current' state after install, got: %s", combined)

	// Tamper one skill to create "outdated" state.
	tampered := filepath.Join(claudeDir, "skills", "chunk-review", "SKILL.md")
	assert.NilError(t, os.WriteFile(tampered, []byte("tampered"), 0o644))

	result = binary.RunCLI(t, []string{"skill", "list"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0)
	combined = result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "outdated"),
		"expected 'outdated' state for tampered skill, got: %s", combined)
}

func TestSkillsListMixedStates(t *testing.T) {
	env := testenv.NewTestEnv(t)
	claudeDir := filepath.Join(env.HomeDir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	// Install all skills first.
	result := binary.RunCLI(t, []string{"skill", "install"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0, "install failed: %s", result.Stderr)

	// Tamper one skill to make it outdated.
	tampered := filepath.Join(claudeDir, "skills", "chunk-review", "SKILL.md")
	assert.NilError(t, os.WriteFile(tampered, []byte("tampered"), 0o644))

	// Delete another skill entirely to make it missing.
	assert.NilError(t, os.RemoveAll(filepath.Join(claudeDir, "skills", "debug-ci-failures")))

	// List should show current, outdated, and missing.
	result = binary.RunCLI(t, []string{"skill", "list"}, env, env.HomeDir)
	assert.Equal(t, result.ExitCode, 0)
	combined := result.Stdout + result.Stderr

	assert.Assert(t, strings.Contains(combined, "current"),
		"expected 'current' for untouched skill, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "outdated"),
		"expected 'outdated' for tampered skill, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "missing"),
		"expected 'missing' for deleted skill, got: %s", combined)
}
