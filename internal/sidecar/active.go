package sidecar

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// ActiveSidecar holds the currently active sidecar for a project.
type ActiveSidecar struct {
	SidecarID string `json:"sidecar_id"`
	Name      string `json:"name,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// sidecarFileName returns the name of the sidecar state file. When
// CLAUDE_SESSION_ID is set the file is keyed to that session so concurrent
// Claude sessions in the same repo each maintain their own active sidecar.
func sidecarFileName() string {
	if id := os.Getenv(config.EnvClaudeSession); id != "" {
		return "sidecar." + id + ".json"
	}
	return "sidecar.json"
}

// LoadActive walks up from cwd looking for .chunk/sidecar.json. Returns nil if not found.
func LoadActive() (*ActiveSidecar, error) {
	return loadActiveNamed(sidecarFileName())
}

// LoadForSession loads the active sidecar for a specific Claude session ID,
// looking for .chunk/sidecar.<sessionID>.json directly rather than relying on
// the CLAUDE_SESSION_ID environment variable.
func LoadForSession(sessionID string) (*ActiveSidecar, error) {
	if sessionID == "" {
		return LoadActive()
	}
	return loadActiveNamed("sidecar." + sessionID + ".json")
}

func loadActiveNamed(filename string) (*ActiveSidecar, error) {
	path, err := findSidecarFileNamed(filename)
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
	var a ActiveSidecar
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// SaveActive writes .chunk/sidecar.json. If the file already exists in a
// parent directory it is updated in place; otherwise the file is created in
// cwd's .chunk/ directory.
func SaveActive(a ActiveSidecar) error {
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
	return os.WriteFile(filepath.Join(dir, sidecarFileName()), data, 0o644)
}

// saveDir returns the .chunk directory to write into. It prefers an existing
// .chunk/sidecar.json found by walking upward; otherwise walks up to find the
// git root and uses that; falls back to cwd/.chunk when not in a git repo.
func saveDir() (string, error) {
	existing, err := findSidecarFile()
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

// ClearActive removes the .chunk/sidecar.json file found by walking up from cwd.
func ClearActive() error {
	path, err := findSidecarFile()
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	return os.Remove(path)
}

// findSidecarFile walks up from cwd looking for the active sidecar file.
func findSidecarFile() (string, error) {
	return findSidecarFileNamed(sidecarFileName())
}

// findSidecarFileNamed walks up from cwd looking for .chunk/<name>, returning the path or "".
// When inside a git repository the walk is bounded by the repository root (the directory
// containing .git); files above that root are never considered. When not inside any git
// repository only the current directory is checked.
func findSidecarFileNamed(name string) (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	gitRoot, _ := findGitRoot()
	for {
		candidate := filepath.Join(dir, ".chunk", name)
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
