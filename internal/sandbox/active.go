package sandbox

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// ActiveSandbox holds the currently active sandbox for a project.
type ActiveSandbox struct {
	SandboxID string `json:"sandbox_id"`
	Name      string `json:"name,omitempty"`
}

// sandboxFileName returns the name of the sandbox state file. When
// CLAUDE_SESSION_ID is set the file is keyed to that session so concurrent
// Claude sessions in the same repo each maintain their own active sandbox.
func sandboxFileName() string {
	if id := os.Getenv("CLAUDE_SESSION_ID"); id != "" {
		return "sandbox." + id
	}
	return "sandbox"
}

// LoadActive walks up from cwd looking for .chunk/sandbox. Returns nil if not found.
func LoadActive() (*ActiveSandbox, error) {
	path, err := findSandboxFile()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a ActiveSandbox
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// SaveActive writes .chunk/sandbox. If a .chunk/sandbox already exists in a
// parent directory it is updated in place; otherwise the file is created in
// cwd's .chunk/ directory.
func SaveActive(a ActiveSandbox) error {
	dir, err := saveDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, sandboxFileName()), data, 0o644)
}

// saveDir returns the .chunk directory to write into. It prefers an existing
// .chunk/sandbox found by walking upward; otherwise walks up to find the git
// root and uses that; falls back to cwd/.chunk when not in a git repo.
func saveDir() (string, error) {
	existing, err := findSandboxFile()
	if err != nil {
		return "", err
	}
	if existing != "" {
		return filepath.Dir(existing), nil
	}
	if root, err := findGitRoot(); err == nil && root != "" {
		return filepath.Join(root, ".chunk"), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".chunk"), nil
}

// findGitRoot walks up from cwd and returns the first directory containing .git,
// or "" if none is found.
func findGitRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// ClearActive removes the .chunk/sandbox file found by walking up from cwd.
func ClearActive() error {
	path, err := findSandboxFile()
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	return os.Remove(path)
}

// findSandboxFile walks up from cwd looking for .chunk/sandbox, returning the path or "".
// When inside a git repository the walk is bounded by the repository root (the directory
// containing .git); files above that root are never considered. When not inside any git
// repository only the current directory is checked.
func findSandboxFile() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	gitRoot, _ := findGitRoot()
	for {
		candidate := filepath.Join(dir, ".chunk", sandboxFileName())
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		// Stop at the git root (or immediately if not in a git repo).
		if gitRoot == "" || dir == gitRoot {
			return "", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
