package sidecar

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// ActiveSnapshot holds the most recently created snapshot for a project.
type ActiveSnapshot struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

func snapshotFileName() string {
	if id := os.Getenv(config.EnvClaudeSession); id != "" {
		return "snapshot." + id + ".json"
	}
	return "snapshot.json"
}

// LoadActiveSnapshot walks up from cwd looking for .chunk/snapshot.json. Returns nil if not found.
func LoadActiveSnapshot() (*ActiveSnapshot, error) {
	path, err := findSnapshotFile()
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
	var a ActiveSnapshot
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// SaveActiveSnapshot writes .chunk/snapshot.json into the same directory as the active sidecar file.
func SaveActiveSnapshot(a ActiveSnapshot) error {
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
	return os.WriteFile(filepath.Join(dir, snapshotFileName()), data, 0o644)
}

// ClearActiveSnapshot removes the .chunk/snapshot.json file found by walking up from cwd.
func ClearActiveSnapshot() error {
	path, err := findSnapshotFile()
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	return os.Remove(path)
}

// findSnapshotFile walks up from cwd looking for .chunk/snapshot.json, returning the path or "".
// Bounded by the git root, same as findSidecarFile.
func findSnapshotFile() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	gitRoot, _ := findGitRoot()
	for {
		candidate := filepath.Join(dir, ".chunk", snapshotFileName())
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
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
