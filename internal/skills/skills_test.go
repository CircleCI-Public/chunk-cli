package skills_test

import (
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/skills"
)

var skillNames = []string{"chunk-review", "chunk-testing-gaps", "debug-ci-failures"}

func TestInstall(t *testing.T) {
	home := t.TempDir()

	err := skills.Install(home)
	assert.NilError(t, err)

	// Verify files exist in both .claude and .codex directories
	for _, dir := range []string{".claude", ".codex"} {
		for _, name := range skillNames {
			path := filepath.Join(home, dir, "skills", name, "SKILL.md")
			info, err := os.Stat(path)
			assert.NilError(t, err, "expected %s to exist", path)
			assert.Assert(t, info.Size() > 0, "expected %s to be non-empty", path)
		}
	}
}

func TestInstallIdempotent(t *testing.T) {
	home := t.TempDir()

	err := skills.Install(home)
	assert.NilError(t, err)

	// Install again should succeed without error
	err = skills.Install(home)
	assert.NilError(t, err)

	// Files should still be valid
	for _, name := range skillNames {
		path := filepath.Join(home, ".claude", "skills", name, "SKILL.md")
		info, err := os.Stat(path)
		assert.NilError(t, err)
		assert.Assert(t, info.Size() > 0)
	}
}

func TestInstallContentMatchesEmbedded(t *testing.T) {
	home := t.TempDir()

	err := skills.Install(home)
	assert.NilError(t, err)

	// Verify .claude and .codex get identical content
	for _, name := range skillNames {
		claudePath := filepath.Join(home, ".claude", "skills", name, "SKILL.md")
		codexPath := filepath.Join(home, ".codex", "skills", name, "SKILL.md")

		claudeData, err := os.ReadFile(claudePath)
		assert.NilError(t, err)
		codexData, err := os.ReadFile(codexPath)
		assert.NilError(t, err)

		assert.Equal(t, string(claudeData), string(codexData),
			"content mismatch for skill %s between .claude and .codex", name)
	}
}

func TestListNotInstalled(t *testing.T) {
	home := t.TempDir()

	infos := skills.List(home)
	assert.Equal(t, len(infos), 3)
	for _, info := range infos {
		assert.Assert(t, !info.Installed, "expected %s not installed", info.Name)
	}
}

func TestListAfterInstall(t *testing.T) {
	home := t.TempDir()

	err := skills.Install(home)
	assert.NilError(t, err)

	infos := skills.List(home)
	assert.Equal(t, len(infos), 3)
	for _, info := range infos {
		assert.Assert(t, info.Installed, "expected %s installed", info.Name)
	}
}

func TestListPartialInstall(t *testing.T) {
	home := t.TempDir()

	// Install only one skill manually
	dir := filepath.Join(home, ".claude", "skills", "chunk-review")
	err := os.MkdirAll(dir, 0o755)
	assert.NilError(t, err)
	err = os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("test"), 0o644)
	assert.NilError(t, err)

	infos := skills.List(home)
	assert.Equal(t, len(infos), 3)

	installedCount := 0
	for _, info := range infos {
		if info.Installed {
			installedCount++
			assert.Equal(t, info.Name, "chunk-review")
		}
	}
	assert.Equal(t, installedCount, 1)
}

func TestListReturnsAllSkillNames(t *testing.T) {
	home := t.TempDir()

	infos := skills.List(home)
	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = info.Name
	}
	assert.DeepEqual(t, names, skillNames)
}
