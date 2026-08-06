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

// --- Project-level install (default, uses CWD) ---

func TestSkillsInstall(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()

	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	result := binary.RunCLI(t, []string{"skill", "install"}, env, dir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "claude:"),
		"expected per-agent output for claude, got: %s", combined)

	for _, name := range []string{"chunk-review", "chunk-testing-gaps", "chunk-sidecar", "debug-ci-failures"} {
		skillFile := filepath.Join(claudeDir, "skills", name, "SKILL.md")
		info, err := os.Stat(skillFile)
		assert.NilError(t, err, "expected skill %s to exist", name)
		assert.Assert(t, info.Size() > 0, "expected skill %s to be non-empty", name)
	}
}

func TestSkillsInstallCodexPath(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()

	// .codex is the project-level config dir for codex.
	codexDir := filepath.Join(dir, ".codex")
	assert.NilError(t, os.MkdirAll(codexDir, 0o755))

	result := binary.RunCLI(t, []string{"skill", "install"}, env, dir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "codex:"),
		"expected per-agent output for codex, got: %s", combined)
	assert.Assert(t, !strings.Contains(combined, "claude:"),
		"expected no claude output when .claude absent, got: %s", combined)

	for _, name := range []string{"chunk-review", "chunk-testing-gaps", "chunk-sidecar", "debug-ci-failures"} {
		skillFile := filepath.Join(codexDir, "skills", name, "SKILL.md")
		info, err := os.Stat(skillFile)
		assert.NilError(t, err, "expected skill %s to exist under .codex", name)
		assert.Assert(t, info.Size() > 0, "expected skill %s to be non-empty", name)
	}
}

func TestSkillsInstallBothAgents(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()

	claudeDir := filepath.Join(dir, ".claude")
	codexDir := filepath.Join(dir, ".codex")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))
	assert.NilError(t, os.MkdirAll(codexDir, 0o755))

	result := binary.RunCLI(t, []string{"skill", "install"}, env, dir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "claude:"),
		"expected per-agent output for claude, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "codex:"),
		"expected per-agent output for codex, got: %s", combined)
	assert.Assert(t, !strings.Contains(combined, "skipped"),
		"expected no skipped agents, got: %s", combined)

	for _, agentDir := range []string{claudeDir, codexDir} {
		for _, name := range []string{"chunk-review", "chunk-testing-gaps", "chunk-sidecar", "debug-ci-failures"} {
			skillFile := filepath.Join(agentDir, "skills", name, "SKILL.md")
			_, err := os.Stat(skillFile)
			assert.NilError(t, err, "expected skill %s under %s", name, agentDir)
		}
	}
}

func TestSkillsInstallNoAgentDirs(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()

	result := binary.RunCLI(t, []string{"skill", "install"}, env, dir)
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "no supported agent config directories"),
		"expected 'no supported agent config directories' message, got: %s", combined)
}

func TestSkillsInstallUpToDate(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))

	binary.RunCLI(t, []string{"skill", "install"}, env, dir)

	result := binary.RunCLI(t, []string{"skill", "install"}, env, dir)
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "up to date"),
		"expected up-to-date message on second install, got: %s", combined)
}

func TestSkillsInstallOutdatedUpdate(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	result := binary.RunCLI(t, []string{"skill", "install"}, env, dir)
	assert.Equal(t, result.ExitCode, 0, "first install failed: %s", result.Stderr)

	tampered := filepath.Join(claudeDir, "skills", "chunk-review", "SKILL.md")
	assert.NilError(t, os.WriteFile(tampered, []byte("tampered content"), 0o644))

	result = binary.RunCLI(t, []string{"skill", "install"}, env, dir)
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "updated"),
		"expected 'updated' message for tampered skill, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "chunk-review"),
		"expected chunk-review in update output, got: %s", combined)

	restored, err := os.ReadFile(tampered)
	assert.NilError(t, err)
	assert.Assert(t, string(restored) != "tampered content",
		"expected content to be restored after update")
	assert.Assert(t, len(restored) > 100,
		"expected restored skill file to have substantial content, got %d bytes", len(restored))
}

// --- User-level install (--user flag) ---

func TestSkillsInstallUserFlag(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir() // workdir with no agent dirs

	claudeDir := filepath.Join(env.HomeDir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	result := binary.RunCLI(t, []string{"skill", "install", "--user"}, env, dir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "claude:"),
		"expected per-agent output for claude, got: %s", combined)

	// Skills should land in $HOME/.claude/skills/, not in dir.
	for _, name := range []string{"chunk-review", "chunk-testing-gaps", "chunk-sidecar", "debug-ci-failures"} {
		skillFile := filepath.Join(claudeDir, "skills", name, "SKILL.md")
		info, err := os.Stat(skillFile)
		assert.NilError(t, err, "expected skill %s to exist under $HOME/.claude", name)
		assert.Assert(t, info.Size() > 0)
	}
}

func TestSkillsInstallUserFlagSkipsUnavailableAgent(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()

	// Only create $HOME/.claude, not $HOME/.agents.
	assert.NilError(t, os.MkdirAll(filepath.Join(env.HomeDir, ".claude"), 0o755))

	result := binary.RunCLI(t, []string{"skill", "install", "--user"}, env, dir)
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "codex: skipped"),
		"expected codex skipped message, got: %s", combined)

	_, err := os.Stat(filepath.Join(env.HomeDir, ".agents", "skills"))
	assert.Assert(t, os.IsNotExist(err), "should not create $HOME/.agents/skills")
}

func TestSkillsInstallUserFlagCodexPath(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()

	// Only create $HOME/.agents, not $HOME/.claude.
	codexDir := filepath.Join(env.HomeDir, ".agents")
	assert.NilError(t, os.MkdirAll(codexDir, 0o755))

	result := binary.RunCLI(t, []string{"skill", "install", "--user"}, env, dir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "codex:"),
		"expected per-agent output for codex, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "claude: skipped"),
		"expected claude skipped, got: %s", combined)

	for _, name := range []string{"chunk-review", "chunk-testing-gaps", "chunk-sidecar", "debug-ci-failures"} {
		skillFile := filepath.Join(codexDir, "skills", name, "SKILL.md")
		info, err := os.Stat(skillFile)
		assert.NilError(t, err, "expected skill %s to exist under $HOME/.agents", name)
		assert.Assert(t, info.Size() > 0)
	}
}

func TestSkillsInstallUserFlagBothAgents(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()

	assert.NilError(t, os.MkdirAll(filepath.Join(env.HomeDir, ".claude"), 0o755))
	assert.NilError(t, os.MkdirAll(filepath.Join(env.HomeDir, ".agents"), 0o755))

	result := binary.RunCLI(t, []string{"skill", "install", "--user"}, env, dir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "claude:"),
		"expected per-agent output for claude, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "codex:"),
		"expected per-agent output for codex, got: %s", combined)
	assert.Assert(t, !strings.Contains(combined, "skipped"),
		"expected no skipped agents, got: %s", combined)
}

func TestSkillsInstallUserFlagNoAgentDirs(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()

	// No $HOME/.claude or $HOME/.agents.
	result := binary.RunCLI(t, []string{"skill", "install", "--user"}, env, dir)
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "claude: skipped"),
		"expected claude skipped, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "codex: skipped"),
		"expected codex skipped, got: %s", combined)
}

func TestSkillsInstallUserFlagHomeNotSet(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.HomeDir = ""

	result := binary.RunCLI(t, []string{"skill", "install", "--user"}, env, os.TempDir())
	assert.Assert(t, result.ExitCode != 0,
		"expected non-zero exit code when HOME is not set with --user, got exit %d", result.ExitCode)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "HOME environment variable is not set"),
		"expected HOME error, got: %s", combined)
}

// --- Skill list ---

func TestSkillsList(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))

	result := binary.RunCLI(t, []string{"skill", "list"}, env, dir)

	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "chunk-review"), "expected 'chunk-review' in output, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "chunk-testing-gaps"), "expected 'chunk-testing-gaps' in output, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "debug-ci-failures"), "expected 'debug-ci-failures' in output, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "chunk-sidecar"), "expected 'chunk-sidecar' in output, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "claude:"), "expected per-agent status for claude, got: %s", combined)
}

func TestSkillsListShowsDescriptions(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()

	result := binary.RunCLI(t, []string{"skill", "list"}, env, dir)
	assert.Equal(t, result.ExitCode, 0)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "mutation test"),
		"expected skill description in output, got: %s", combined)
}

func TestSkillsListStateLabels(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	// Before install: .claude exists so skills should show "missing".
	result := binary.RunCLI(t, []string{"skill", "list"}, env, dir)
	assert.Equal(t, result.ExitCode, 0)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "missing"),
		"expected 'missing' state before install, got: %s", combined)

	// Install skills.
	result = binary.RunCLI(t, []string{"skill", "install"}, env, dir)
	assert.Equal(t, result.ExitCode, 0, "install failed: %s", result.Stderr)

	// After install: skills should show "current".
	result = binary.RunCLI(t, []string{"skill", "list"}, env, dir)
	assert.Equal(t, result.ExitCode, 0)
	combined = result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "current"),
		"expected 'current' state after install, got: %s", combined)

	// Tamper one skill to create "outdated" state.
	tampered := filepath.Join(claudeDir, "skills", "chunk-review", "SKILL.md")
	assert.NilError(t, os.WriteFile(tampered, []byte("tampered"), 0o644))

	result = binary.RunCLI(t, []string{"skill", "list"}, env, dir)
	assert.Equal(t, result.ExitCode, 0)
	combined = result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "outdated"),
		"expected 'outdated' state for tampered skill, got: %s", combined)
}

func TestSkillsListMixedStates(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	result := binary.RunCLI(t, []string{"skill", "install"}, env, dir)
	assert.Equal(t, result.ExitCode, 0, "install failed: %s", result.Stderr)

	tampered := filepath.Join(claudeDir, "skills", "chunk-review", "SKILL.md")
	assert.NilError(t, os.WriteFile(tampered, []byte("tampered"), 0o644))
	assert.NilError(t, os.RemoveAll(filepath.Join(claudeDir, "skills", "debug-ci-failures")))

	result = binary.RunCLI(t, []string{"skill", "list"}, env, dir)
	assert.Equal(t, result.ExitCode, 0)
	combined := result.Stdout + result.Stderr

	assert.Assert(t, strings.Contains(combined, "current"), "expected 'current' for untouched skill, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "outdated"), "expected 'outdated' for tampered skill, got: %s", combined)
	assert.Assert(t, strings.Contains(combined, "missing"), "expected 'missing' for deleted skill, got: %s", combined)
}

func TestSkillsListUserFlag(t *testing.T) {
	env := testenv.NewTestEnv(t)
	dir := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(env.HomeDir, ".claude"), 0o755))

	result := binary.RunCLI(t, []string{"skill", "list", "--user"}, env, dir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "claude:"), "expected per-agent status for claude, got: %s", combined)
	// $HOME/.agents not present, so codex shows "n/a".
	assert.Assert(t, strings.Contains(combined, "n/a"), "expected 'n/a' for codex (not installed), got: %s", combined)
}
