package sidecar

import (
	"encoding/json"
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

// LoadActiveSnapshot reads the active snapshot for the current project from XDG_DATA_HOME.
// Returns nil if not found.
func LoadActiveSnapshot() (*ActiveSnapshot, error) {
	dir, err := StateDir()
	if err != nil {
		return nil, err
	}
	return LoadSnapshotFrom(dir)
}

// LoadSnapshotFrom reads the active snapshot from dir.
func LoadSnapshotFrom(dir string) (*ActiveSnapshot, error) {
	path, err := findSnapshotFile(dir)
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

// SaveActiveSnapshot writes the active snapshot to XDG_DATA_HOME for the current project.
func SaveActiveSnapshot(a ActiveSnapshot) error {
	dir, err := StateDir()
	if err != nil {
		return err
	}
	return SaveSnapshotTo(dir, a)
}

// SaveSnapshotTo writes the active snapshot to dir.
func SaveSnapshotTo(dir string, a ActiveSnapshot) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, snapshotFileName()), data, 0o644)
}

// ClearActiveSnapshot removes the active snapshot state file.
func ClearActiveSnapshot() error {
	dir, err := StateDir()
	if err != nil {
		return err
	}
	return ClearSnapshotFrom(dir)
}

// ClearSnapshotFrom removes the active snapshot state file in dir.
func ClearSnapshotFrom(dir string) error {
	path, err := findSnapshotFile(dir)
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	return os.Remove(path)
}

// findSnapshotFile returns the snapshot state file path in dir, or "" if it doesn't exist.
func findSnapshotFile(dir string) (string, error) {
	return statOrEmpty(filepath.Join(dir, snapshotFileName()))
}
