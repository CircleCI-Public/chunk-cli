package skills

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/skills"
)

var names = []string{"chunk-review", "chunk-testing-gaps", "debug-ci-failures"}

// SkillInfo describes an available skill.
type SkillInfo struct {
	Name      string
	Installed bool
}

// Install copies all embedded skill files into ~/.claude/skills/ and ~/.codex/skills/.
func Install(homeDir string) error {
	targets := []string{
		filepath.Join(homeDir, ".claude", "skills"),
		filepath.Join(homeDir, ".codex", "skills"),
	}

	for _, name := range names {
		data, err := skills.Content.ReadFile(filepath.Join(name, "SKILL.md"))
		if err != nil {
			return fmt.Errorf("read embedded skill %s: %w", name, err)
		}

		for _, target := range targets {
			dir := filepath.Join(target, name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create dir %s: %w", dir, err)
			}
			dest := filepath.Join(dir, "SKILL.md")
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", dest, err)
			}
		}
	}
	return nil
}

// List returns info about all available skills.
func List(homeDir string) []SkillInfo {
	var infos []SkillInfo
	for _, name := range names {
		installed := false
		path := filepath.Join(homeDir, ".claude", "skills", name, "SKILL.md")
		if _, err := os.Stat(path); err == nil {
			installed = true
		}
		infos = append(infos, SkillInfo{Name: name, Installed: installed})
	}
	return infos
}
