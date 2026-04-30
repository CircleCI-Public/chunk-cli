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

// LoadActive reads the active sidecar for the current project from XDG_DATA_HOME.
// Returns nil if not found.
func LoadActive() (*ActiveSidecar, error) {
	dir, err := saveDir()
	if err != nil {
		return nil, err
	}
	path, err := findSidecarFile(dir)
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

// SaveActive writes the active sidecar to XDG_DATA_HOME for the current project.
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

// saveDir returns the XDG_DATA_HOME directory for the current project.
func saveDir() (string, error) {
	root, err := projectRoot()
	if err != nil {
		return "", err
	}
	return config.ProjectDataDir(root)
}

// projectRoot returns the git root when inside a git repo, otherwise cwd.
func projectRoot() (string, error) {
	if root, err := findGitRoot(); err == nil && root != "" {
		return root, nil
	}
	return os.Getwd()
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

// ClearActive removes the active sidecar state file.
func ClearActive() error {
	dir, err := saveDir()
	if err != nil {
		return err
	}
	path, err := findSidecarFile(dir)
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	return os.Remove(path)
}

// findSidecarFile returns the sidecar state file path in dir, or "" if it doesn't exist.
func findSidecarFile(dir string) (string, error) {
	return statOrEmpty(filepath.Join(dir, sidecarFileName()))
}

// statOrEmpty returns path if it exists, "" if it does not, or an error for other failures.
func statOrEmpty(path string) (string, error) {
	_, err := os.Stat(path)
	if err == nil {
		return path, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return "", err
}
