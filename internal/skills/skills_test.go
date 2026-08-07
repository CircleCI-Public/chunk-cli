package skills_test

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/skills"
	embeddedSkills "github.com/CircleCI-Public/chunk-cli/skills"
)

var skillNames = []string{"chunk-testing-gaps", "chunk-review", "debug-ci-failures", "chunk-sidecar"}

func TestInstallBothAgents(t *testing.T) {
	home := t.TempDir()

	// Create both agent config dirs.
	for _, dir := range []string{".claude", ".agents"} {
		assert.NilError(t, os.MkdirAll(filepath.Join(home, dir), 0o755))
	}

	results := skills.Install(skills.ScopeUser, home)
	assert.Equal(t, len(results), 2)

	for _, r := range results {
		assert.Assert(t, !r.Skipped, "agent %s should not be skipped", r.Agent)
		assert.Equal(t, len(r.Installed), len(skillNames),
			"agent %s: expected %d installed, got %d", r.Agent, len(skillNames), len(r.Installed))
		assert.Equal(t, len(r.Updated), 0)
	}

	// Verify files exist.
	for _, dir := range []string{".claude", ".agents"} {
		for _, name := range skillNames {
			path := filepath.Join(home, dir, "skills", name, "SKILL.md")
			info, err := os.Stat(path)
			assert.NilError(t, err, "expected %s to exist", path)
			assert.Assert(t, info.Size() > 0, "expected %s to be non-empty", path)
		}
	}
}

func TestInstallSkipsAgentWithoutConfigDir(t *testing.T) {
	home := t.TempDir()

	// Only create .claude, not .agents.
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	results := skills.Install(skills.ScopeUser, home)
	assert.Equal(t, len(results), 2)

	var claude, codex skills.AgentInstallResult
	for _, r := range results {
		switch r.Agent {
		case "claude":
			claude = r
		case "codex":
			codex = r
		}
	}

	assert.Assert(t, !claude.Skipped)
	assert.Equal(t, len(claude.Installed), len(skillNames))
	assert.Assert(t, codex.Skipped, "codex should be skipped when .agents dir missing")
	assert.Equal(t, len(codex.Installed), 0)

	// Verify .agents skills dir was not created.
	_, err := os.Stat(filepath.Join(home, ".agents", "skills"))
	assert.Assert(t, os.IsNotExist(err), "should not create .agents/skills when .agents missing")
}

func TestInstallIdempotent(t *testing.T) {
	home := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	results1 := skills.Install(skills.ScopeUser, home)
	assert.Equal(t, len(results1[0].Installed), len(skillNames))

	// Second install should report all up to date.
	results2 := skills.Install(skills.ScopeUser, home)
	assert.Equal(t, len(results2[0].Installed), 0, "should have no new installs")
	assert.Equal(t, len(results2[0].Updated), 0, "should have no updates")
}

func TestInstallDetectsOutdated(t *testing.T) {
	home := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	// First install.
	skills.Install(skills.ScopeUser, home)

	// Tamper with one skill file to make it outdated.
	path := filepath.Join(home, ".claude", "skills", "chunk-review", "SKILL.md")
	assert.NilError(t, os.WriteFile(path, []byte("old content"), 0o644))

	results := skills.Install(skills.ScopeUser, home)
	claude := results[0]
	assert.Equal(t, len(claude.Installed), 0)
	assert.Equal(t, len(claude.Updated), 1)
	assert.Equal(t, claude.Updated[0], "chunk-review")
}

func TestInstallContentMatchesEmbedded(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{".claude", ".agents"} {
		assert.NilError(t, os.MkdirAll(filepath.Join(home, dir), 0o755))
	}

	skills.Install(skills.ScopeUser, home)

	for _, name := range skillNames {
		claudePath := filepath.Join(home, ".claude", "skills", name, "SKILL.md")
		codexPath := filepath.Join(home, ".agents", "skills", name, "SKILL.md")

		claudeData, err := os.ReadFile(claudePath)
		assert.NilError(t, err)
		codexData, err := os.ReadFile(codexPath)
		assert.NilError(t, err)

		assert.Equal(t, string(claudeData), string(codexData),
			"content mismatch for skill %s between .claude and .agents", name)
	}
}

func TestInstallProjectScope(t *testing.T) {
	projectDir := t.TempDir()

	// Project scope does not require pre-existing agent dirs.
	results := skills.Install(skills.ScopeProject, projectDir)
	assert.Equal(t, len(results), 2)

	for _, r := range results {
		assert.Assert(t, !r.Skipped, "agent %s should not be skipped for project scope", r.Agent)
		assert.Equal(t, len(r.Installed), len(skillNames),
			"agent %s: expected %d installed, got %d", r.Agent, len(skillNames), len(r.Installed))
	}

	// Verify files exist under project-relative dirs.
	for _, dir := range []string{".claude", ".agents"} {
		for _, name := range skillNames {
			path := filepath.Join(projectDir, dir, "skills", name, "SKILL.md")
			info, err := os.Stat(path)
			assert.NilError(t, err, "expected %s to exist for project scope", path)
			assert.Assert(t, info.Size() > 0, "expected %s to be non-empty", path)
		}
	}
}

func TestInstallProjectScopeIdempotent(t *testing.T) {
	projectDir := t.TempDir()

	skills.Install(skills.ScopeProject, projectDir)

	results := skills.Install(skills.ScopeProject, projectDir)
	for _, r := range results {
		assert.Equal(t, len(r.Installed), 0, "agent %s: should have no new installs on second run", r.Agent)
		assert.Equal(t, len(r.Updated), 0, "agent %s: should have no updates on second run", r.Agent)
	}
}

func TestStatusNotInstalled(t *testing.T) {
	home := t.TempDir()

	statuses := skills.Status(skills.ScopeUser, home)
	assert.Equal(t, len(statuses), 2)

	for _, agent := range statuses {
		assert.Assert(t, !agent.Available, "agent %s should not be available", agent.Agent)
		for _, s := range agent.Skills {
			assert.Equal(t, s.State, skills.StateMissing,
				"skill %s for %s should be missing", s.Name, agent.Agent)
		}
	}
}

func TestStatusCurrent(t *testing.T) {
	home := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	skills.Install(skills.ScopeUser, home)

	statuses := skills.Status(skills.ScopeUser, home)
	var claude skills.AgentStatus
	for _, s := range statuses {
		if s.Agent == "claude" {
			claude = s
		}
	}

	assert.Assert(t, claude.Available)
	for _, s := range claude.Skills {
		assert.Equal(t, s.State, skills.StateCurrent,
			"skill %s should be current after install", s.Name)
	}
}

func TestStatusOutdated(t *testing.T) {
	home := t.TempDir()
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	skills.Install(skills.ScopeUser, home)

	// Tamper with a skill.
	path := filepath.Join(home, ".claude", "skills", "chunk-review", "SKILL.md")
	assert.NilError(t, os.WriteFile(path, []byte("tampered"), 0o644))

	statuses := skills.Status(skills.ScopeUser, home)
	var claude skills.AgentStatus
	for _, s := range statuses {
		if s.Agent == "claude" {
			claude = s
		}
	}

	for _, s := range claude.Skills {
		if s.Name == "chunk-review" {
			assert.Equal(t, s.State, skills.StateOutdated)
		} else {
			assert.Equal(t, s.State, skills.StateCurrent)
		}
	}
}

func TestStatusIncludesDescriptions(t *testing.T) {
	home := t.TempDir()

	statuses := skills.Status(skills.ScopeUser, home)
	for _, agent := range statuses {
		for _, s := range agent.Skills {
			assert.Assert(t, s.Description != "",
				"skill %s should have a description", s.Name)
		}
	}
}

func TestStatusAgentNotAvailable(t *testing.T) {
	home := t.TempDir()
	// Only create .claude.
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	statuses := skills.Status(skills.ScopeUser, home)
	for _, agent := range statuses {
		if agent.Agent == "claude" {
			assert.Assert(t, agent.Available)
		} else {
			assert.Assert(t, !agent.Available, "codex should not be available")
			for _, s := range agent.Skills {
				assert.Equal(t, s.State, skills.StateMissing)
			}
		}
	}
}

func TestStatusProjectScopeAlwaysAvailable(t *testing.T) {
	projectDir := t.TempDir()

	// No dirs created — project scope agents should still be "available".
	statuses := skills.Status(skills.ScopeProject, projectDir)
	assert.Equal(t, len(statuses), 2)
	for _, agent := range statuses {
		assert.Assert(t, agent.Available, "agent %s should be available for project scope", agent.Agent)
		for _, s := range agent.Skills {
			assert.Equal(t, s.State, skills.StateMissing,
				"skill %s for %s should be missing before install", s.Name, agent.Agent)
		}
	}
}

func TestSkillStateDetectsStates(t *testing.T) {
	dir := t.TempDir()
	s := skills.All[0] // chunk-testing-gaps

	// Missing: no file at all.
	assert.Equal(t, skills.SkillState(dir, s), skills.StateMissing)

	// Install the file with correct content.
	skillDir := filepath.Join(dir, s.Name)
	assert.NilError(t, os.MkdirAll(skillDir, 0o755))
	content, err := embeddedSkills.Content.ReadFile(filepath.Join(s.Name, "SKILL.md"))
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), content, 0o644))

	assert.Equal(t, skills.SkillState(dir, s), skills.StateCurrent)

	// Tamper to make outdated.
	assert.NilError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("old"), 0o644))
	assert.Equal(t, skills.SkillState(dir, s), skills.StateOutdated)
}

// TestInstallSurfacesWriteErrors verifies that write failures (e.g. permission
// denied) are captured in AgentInstallResult.Errors rather than silently
// swallowed as a continue, which previously caused the caller to print
// "all skills up to date" with nothing actually written.
func TestInstallSurfacesWriteErrors(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	assert.NilError(t, os.MkdirAll(claudeDir, 0o755))

	// Make the skills dir read-only so writes fail.
	skillsDir := filepath.Join(claudeDir, "skills")
	assert.NilError(t, os.MkdirAll(skillsDir, 0o555))
	t.Cleanup(func() { _ = os.Chmod(skillsDir, 0o755) })

	results := skills.Install(skills.ScopeUser, home)
	var claude skills.AgentInstallResult
	for _, r := range results {
		if r.Agent == "claude" {
			claude = r
		}
	}

	assert.Assert(t, len(claude.Errors) > 0, "expected write errors to be surfaced, got none")
	assert.Equal(t, len(claude.Installed), 0, "no skills should be recorded as installed on write failure")
}

// TestInstallSkipIfAbsentOnPermissionError verifies that SkipIfAbsent skips an
// agent whenever os.Stat returns any error — not only ENOENT. Previously
// os.IsNotExist was used, which let EACCES pass through and the write loop
// would run (also silently failing).
func TestInstallSkipIfAbsentOnPermissionError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("cannot test permission errors as root")
	}
	home := t.TempDir()

	// Remove execute permission on home so os.Stat on home/.claude returns EACCES.
	assert.NilError(t, os.Chmod(home, 0o000))
	t.Cleanup(func() { _ = os.Chmod(home, 0o755) })

	results := skills.Install(skills.ScopeUser, home)
	for _, r := range results {
		assert.Assert(t, r.Skipped,
			"agent %s should be skipped when ConfigDir is inaccessible", r.Agent)
		assert.Equal(t, len(r.Installed), 0)
	}
}

func TestAllSkillsHaveEmbeddedContent(t *testing.T) {
	for _, s := range skills.All {
		data, err := embeddedSkills.Content.ReadFile(filepath.Join(s.Name, "SKILL.md"))
		assert.NilError(t, err, "embedded content missing for %s", s.Name)
		assert.Assert(t, len(data) > 0, "embedded content empty for %s", s.Name)
	}
}
