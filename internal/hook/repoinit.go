package hook

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// RunRepoInit initializes a repository with hook configuration files.
func RunRepoInit(targetDir string, force bool, streams iostream.Streams) error {
	targetDir, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("resolve target dir: %w", err)
	}

	projectName := sanitizeProjectName(filepath.Base(targetDir))

	for _, tmpl := range templateFiles {
		content := tmpl.content
		if tmpl.substituteProject {
			content = strings.ReplaceAll(content, "__PROJECT__", projectName)
		}

		dest := filepath.Join(targetDir, tmpl.relativePath)

		if !force && fileExists(dest) {
			// Write .example variant
			examplePath := makeExamplePath(dest)
			if err := writeFile(examplePath, content); err != nil {
				return fmt.Errorf("write example %s: %w", examplePath, err)
			}
			streams.ErrPrintf("%s already exists, wrote %s\n", tmpl.relativePath, filepath.Base(examplePath))
			continue
		}

		if err := writeFile(dest, content); err != nil {
			return fmt.Errorf("write %s: %w", dest, err)
		}
		streams.ErrPrintf("Created %s\n", tmpl.relativePath)
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
