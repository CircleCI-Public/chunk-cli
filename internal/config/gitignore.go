package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// localStatePatterns are the .gitignore patterns for the machine-local state
// chunk keeps under .chunk/, such as sidecar pool IDs. .chunk/config.json and
// the rest of the project's config must stay tracked.
var localStatePatterns = []string{
	".chunk/*-pool.json",
}

// EnsureGitignore appends localStatePatterns to the .gitignore in dir, creating
// it if need be. added reports whether any pattern was missing.
func EnsureGitignore(dir string) (added bool, err error) {
	path := filepath.Join(dir, ".gitignore")

	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("read .gitignore: %w", err)
	}

	content := string(existing)
	var toAdd []string
	for _, pattern := range localStatePatterns {
		if !strings.Contains(content, pattern) {
			toAdd = append(toAdd, pattern)
		}
	}
	if len(toAdd) == 0 {
		return false, nil
	}

	var b strings.Builder
	if len(existing) > 0 && existing[len(existing)-1] != '\n' {
		b.WriteString("\n")
	}
	b.WriteString("\n# chunk local pool state\n")
	for _, pattern := range toAdd {
		b.WriteString(pattern + "\n")
	}

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, fmt.Errorf("open .gitignore: %w", err)
	}
	if _, err := f.WriteString(b.String()); err != nil {
		_ = f.Close()
		return false, fmt.Errorf("write .gitignore: %w", err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("write .gitignore: %w", err)
	}
	return true, nil
}
