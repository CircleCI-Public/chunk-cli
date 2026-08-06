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

// inDir changes the working directory to dir for the duration of the test.
func inDir(t *testing.T, dir string) {
	t.Helper()
	orig, err := os.Getwd()
	assert.NilError(t, err)
	assert.NilError(t, os.Chdir(dir))
	t.Cleanup(func() { os.Chdir(orig) })
}

// withHome sets HOME to dir for the duration of the test.
func withHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)
}

// --- ProjectScope tests ---

func TestProjectScopeDetectsDotDirs(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)

	results, err := skills.Install(skills.ProjectScope)
	assert.NilError(t, err)
	assert.Equal(t, len(results), 0, "expected no results before any dot dirs exist")

	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))
	results, err = skills.Install(skills.ProjectScope)
	assert.NilError(t, err)
	assert.Equal(t, len(results), 1)
	assert.Equal(t, results[0].Agent, "claude")

	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".codex"), 0o755))
	statuses, err := skills.Status(skills.ProjectScope)
	assert.NilError(t, err)
	assert.Equal(t, len(statuses), 2)
}

func TestInstallInDir(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))

	results, err := skills.Install(skills.ProjectScope)
	assert.NilError(t, err)
	assert.Equal(t, len(results), 1)
	assert.Equal(t, results[0].Agent, "claude")
	assert.Assert(t, !results[0].Skipped)
	assert.Equal(t, len(results[0].Installed), len(skillNames))

	for _, name := range skillNames {
		path := filepath.Join(dir, ".claude", "skills", name, "SKILL.md")
		info, err := os.Stat(path)
		assert.NilError(t, err, "expected %s to exist", path)
		assert.Assert(t, info.Size() > 0)
	}
}

func TestInstallInDirNoDirs(t *testing.T) {
	inDir(t, t.TempDir())
	results, err := skills.Install(skills.ProjectScope)
	assert.NilError(t, err)
	assert.Equal(t, len(results), 0)
}

func TestInstallInDirBothAgents(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".codex"), 0o755))

	results, err := skills.Install(skills.ProjectScope)
	assert.NilError(t, err)
	assert.Equal(t, len(results), 2)
	for _, r := range results {
		assert.Equal(t, len(r.Installed), len(skillNames))
	}
}

func TestInstallByNameInDir(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))

	results, err := skills.InstallByName(skills.ProjectScope, "chunk-review")
	assert.NilError(t, err)
	assert.Equal(t, len(results), 1)
	assert.Equal(t, len(results[0].Installed), 1)
	assert.Equal(t, results[0].Installed[0], "chunk-review")

	_, err = os.Stat(filepath.Join(dir, ".claude", "skills", "chunk-sidecar", "SKILL.md"))
	assert.Assert(t, os.IsNotExist(err), "only the named skill should be installed")
}

func TestInstallByNameInDirUnknownSkill(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))
	results, err := skills.InstallByName(skills.ProjectScope, "does-not-exist")
	assert.NilError(t, err)
	assert.Assert(t, results == nil)
}

func TestStatusInDir(t *testing.T) {
	dir := t.TempDir()
	inDir(t, dir)

	statuses, err := skills.Status(skills.ProjectScope)
	assert.NilError(t, err)
	assert.Equal(t, len(statuses), 0)

	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".claude"), 0o755))
	_, err = skills.Install(skills.ProjectScope)
	assert.NilError(t, err)

	statuses, err = skills.Status(skills.ProjectScope)
	assert.NilError(t, err)
	assert.Equal(t, len(statuses), 1)
	assert.Equal(t, statuses[0].Agent, "claude")
	assert.Assert(t, statuses[0].Available)
	for _, s := range statuses[0].Skills {
		assert.Equal(t, s.State, skills.StateCurrent, "skill %s should be current", s.Name)
	}
}

func TestSupportedProjectDotDirs(t *testing.T) {
	dirs := skills.SupportedProjectDotDirs()
	found := map[string]bool{}
	for _, d := range dirs {
		found[d] = true
	}
	assert.Assert(t, found[".claude"], "expected .claude in supported dirs")
	assert.Assert(t, found[".codex"], "expected .codex in supported dirs")
}

// --- UserScope tests ---

func TestInstallBothAgents(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	for _, dir := range []string{".claude", ".agents"} {
		assert.NilError(t, os.MkdirAll(filepath.Join(home, dir), 0o755))
	}

	results, err := skills.Install(skills.UserScope)
	assert.NilError(t, err)
	assert.Equal(t, len(results), 2)

	for _, r := range results {
		assert.Assert(t, !r.Skipped, "agent %s should not be skipped", r.Agent)
		assert.Equal(t, len(r.Installed), len(skillNames),
			"agent %s: expected %d installed, got %d", r.Agent, len(skillNames), len(r.Installed))
		assert.Equal(t, len(r.Updated), 0)
	}

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
	withHome(t, home)
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	results, err := skills.Install(skills.UserScope)
	assert.NilError(t, err)
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

	_, err = os.Stat(filepath.Join(home, ".agents", "skills"))
	assert.Assert(t, os.IsNotExist(err), "should not create .agents/skills when .agents missing")
}

func TestInstallIdempotent(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	results1, err := skills.Install(skills.UserScope)
	assert.NilError(t, err)
	assert.Equal(t, len(results1[0].Installed), len(skillNames))

	results2, err := skills.Install(skills.UserScope)
	assert.NilError(t, err)
	assert.Equal(t, len(results2[0].Installed), 0, "should have no new installs")
	assert.Equal(t, len(results2[0].Updated), 0, "should have no updates")
}

func TestInstallDetectsOutdated(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	_, err := skills.Install(skills.UserScope)
	assert.NilError(t, err)

	path := filepath.Join(home, ".claude", "skills", "chunk-review", "SKILL.md")
	assert.NilError(t, os.WriteFile(path, []byte("old content"), 0o644))

	results, err := skills.Install(skills.UserScope)
	assert.NilError(t, err)
	claude := results[0]
	assert.Equal(t, len(claude.Installed), 0)
	assert.Equal(t, len(claude.Updated), 1)
	assert.Equal(t, claude.Updated[0], "chunk-review")
}

func TestInstallContentMatchesEmbedded(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	for _, dir := range []string{".claude", ".agents"} {
		assert.NilError(t, os.MkdirAll(filepath.Join(home, dir), 0o755))
	}

	_, err := skills.Install(skills.UserScope)
	assert.NilError(t, err)

	for _, name := range skillNames {
		claudeData, err := os.ReadFile(filepath.Join(home, ".claude", "skills", name, "SKILL.md"))
		assert.NilError(t, err)
		codexData, err := os.ReadFile(filepath.Join(home, ".agents", "skills", name, "SKILL.md"))
		assert.NilError(t, err)
		assert.Equal(t, string(claudeData), string(codexData),
			"content mismatch for skill %s between .claude and .agents", name)
	}
}

func TestStatusNotInstalled(t *testing.T) {
	withHome(t, t.TempDir())
	statuses, err := skills.Status(skills.UserScope)
	assert.NilError(t, err)
	assert.Equal(t, len(statuses), 2)
	for _, agent := range statuses {
		assert.Assert(t, !agent.Available, "agent %s should not be available", agent.Agent)
		for _, s := range agent.Skills {
			assert.Equal(t, s.State, skills.StateMissing, "skill %s for %s should be missing", s.Name, agent.Agent)
		}
	}
}

func TestStatusCurrent(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	_, err := skills.Install(skills.UserScope)
	assert.NilError(t, err)

	statuses, err := skills.Status(skills.UserScope)
	assert.NilError(t, err)
	var claude skills.AgentStatus
	for _, s := range statuses {
		if s.Agent == "claude" {
			claude = s
		}
	}
	assert.Assert(t, claude.Available)
	for _, s := range claude.Skills {
		assert.Equal(t, s.State, skills.StateCurrent, "skill %s should be current after install", s.Name)
	}
}

func TestStatusOutdated(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	_, err := skills.Install(skills.UserScope)
	assert.NilError(t, err)

	path := filepath.Join(home, ".claude", "skills", "chunk-review", "SKILL.md")
	assert.NilError(t, os.WriteFile(path, []byte("tampered"), 0o644))

	statuses, err := skills.Status(skills.UserScope)
	assert.NilError(t, err)
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
	withHome(t, t.TempDir())
	statuses, err := skills.Status(skills.UserScope)
	assert.NilError(t, err)
	for _, agent := range statuses {
		for _, s := range agent.Skills {
			assert.Assert(t, s.Description != "", "skill %s should have a description", s.Name)
		}
	}
}

func TestStatusAgentNotAvailable(t *testing.T) {
	home := t.TempDir()
	withHome(t, home)
	assert.NilError(t, os.MkdirAll(filepath.Join(home, ".claude"), 0o755))

	statuses, err := skills.Status(skills.UserScope)
	assert.NilError(t, err)
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

// --- SkillState (independent of scope) ---

func TestSkillStateDetectsStates(t *testing.T) {
	dir := t.TempDir()
	s := skills.All[0] // chunk-testing-gaps

	assert.Equal(t, skills.SkillState(dir, s), skills.StateMissing)

	skillDir := filepath.Join(dir, s.Name)
	assert.NilError(t, os.MkdirAll(skillDir, 0o755))
	content, err := embeddedSkills.Content.ReadFile(filepath.Join(s.Name, "SKILL.md"))
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), content, 0o644))
	assert.Equal(t, skills.SkillState(dir, s), skills.StateCurrent)

	assert.NilError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("old"), 0o644))
	assert.Equal(t, skills.SkillState(dir, s), skills.StateOutdated)
}

func TestAllSkillsHaveEmbeddedContent(t *testing.T) {
	for _, s := range skills.All {
		data, err := embeddedSkills.Content.ReadFile(filepath.Join(s.Name, "SKILL.md"))
		assert.NilError(t, err, "embedded content missing for %s", s.Name)
		assert.Assert(t, len(data) > 0, "embedded content empty for %s", s.Name)
	}
}
