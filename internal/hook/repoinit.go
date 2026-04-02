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

	// Append chunk-managed entries to the project-root .gitignore if missing.
	if err := ensureGitignoreEntries(targetDir, []string{".chunk/sandbox*"}, streams); err != nil {
		return fmt.Errorf("update .gitignore: %w", err)
	}

	// Generate settings.json content from detected commands.
	// settings.json is scaffold-once: never overwrite an existing file since users
	// may have customized it. Always write the example so they have a current reference.
	settingsContent, err := BuildSettingsJSON(projectName, commands)
	if err != nil {
		return fmt.Errorf("build settings.json: %w", err)
	}
	settingsPath := filepath.Join(targetDir, ".claude", "settings.json")

	if fileExists(settingsPath) {
		examplePath := makeExamplePath(settingsPath)
		if err := writeFile(examplePath, settingsContent); err != nil {
			return fmt.Errorf("write example %s: %w", examplePath, err)
		}
		streams.ErrPrintf("%s %s\n", ui.Warning(".claude/settings.json already exists,"), ui.Dim("wrote "+filepath.Base(examplePath)+" for reference"))
	} else {
		if err := writeFile(settingsPath, settingsContent); err != nil {
			return fmt.Errorf("write %s: %w", settingsPath, err)
		}
		streams.ErrPrintf("%s\n", ui.Success("Created .claude/settings.json"))
	}

	return nil
}

// ensureGitignoreEntries appends any entries not already present in the
// project-root .gitignore. Existing content is never modified.
func ensureGitignoreEntries(dir string, entries []string, streams iostream.Streams) error {
	path := filepath.Join(dir, ".gitignore")

	existing, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	var missing []string
	for _, entry := range entries {
		if !strings.Contains(string(existing), entry) {
			missing = append(missing, entry)
		}
	}
	if len(missing) == 0 {
		return nil
	}

	var buf strings.Builder
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		buf.WriteString("\n")
	}
	buf.WriteString("\n# chunk\n")
	for _, e := range missing {
		buf.WriteString(e + "\n")
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(buf.String()); err != nil {
		return err
	}

	streams.ErrPrintf("%s\n", ui.Success("Updated .gitignore"))
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
