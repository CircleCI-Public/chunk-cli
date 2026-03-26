package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// RunRepoInit initializes a repository with hook configuration files.
// If projectName is empty, it falls back to the directory basename.
func RunRepoInit(targetDir, projectName string, commands []config.Command, force bool, streams iostream.Streams) error {
	targetDir, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("resolve target dir: %w", err)
	}

	if projectName == "" {
		projectName = sanitizeProjectName(filepath.Base(targetDir))
	}

	// Write static template files (e.g. .gitignore)
	for _, tmpl := range templateFiles {
		content := tmpl.content
		if tmpl.substituteProject {
			content = strings.ReplaceAll(content, "__PROJECT__", projectName)
		}

		dest := filepath.Join(targetDir, tmpl.relativePath)

		if !force && fileExists(dest) {
			examplePath := makeExamplePath(dest)
			if err := writeFile(examplePath, content); err != nil {
				return fmt.Errorf("write example %s: %w", examplePath, err)
			}
			streams.ErrPrintf("%s %s\n", ui.Warning(tmpl.relativePath+" already exists,"), ui.Dim("wrote "+filepath.Base(examplePath)))
			continue
		}

		if err := writeFile(dest, content); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		streams.ErrPrintf("%s\n", ui.Success(fmt.Sprintf("Created %s", tmpl.relativePath)))
	}

	// Generate and write settings.json dynamically from detected commands
	settingsContent := BuildSettingsJSON(projectName, commands)
	settingsPath := filepath.Join(targetDir, ".claude", "settings.json")

	if !force && fileExists(settingsPath) {
		examplePath := makeExamplePath(settingsPath)
		if err := writeFile(examplePath, settingsContent); err != nil {
			return fmt.Errorf("write example %s: %w", examplePath, err)
		}
		streams.ErrPrintf("%s %s\n", ui.Warning(".claude/settings.json already exists,"), ui.Dim("wrote "+filepath.Base(examplePath)))
	} else {
		if err := writeFile(settingsPath, settingsContent); err != nil {
			return fmt.Errorf("write %s: %w", settingsPath, err)
		}
		streams.ErrPrintf("%s\n", ui.Success("Created .claude/settings.json"))
	}

	return nil
}

var unsafeChars = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

func sanitizeProjectName(name string) string {
	return unsafeChars.ReplaceAllString(name, "_")
}

func makeExamplePath(dest string) string {
	dir := filepath.Dir(dest)
	base := filepath.Base(dest)
	ext := filepath.Ext(base)
	name := strings.TrimSuffix(base, ext)

	if ext == "" {
		return filepath.Join(dir, base+".example")
	}
	return filepath.Join(dir, name+".example"+ext)
}

func writeFile(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
