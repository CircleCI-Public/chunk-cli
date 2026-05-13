package sidecar

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/session"
)

// ActiveSnapshot holds the most recently created snapshot for a project.
type ActiveSnapshot struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

func snapshotFileName(sessionID string) string {
	if sessionID != "" {
		return "snapshot." + sessionID + ".json"
	}
	return "snapshot.json"
}

// LoadActiveSnapshot reads the active snapshot for the current project from XDG_DATA_HOME.
// Returns nil if not found.
func LoadActiveSnapshot(ctx context.Context) (*ActiveSnapshot, error) {
	dir, err := StateDir()
	if err != nil {
		return nil, err
	}
	return LoadSnapshotFrom(ctx, dir)
}

// LoadSnapshotFrom reads the active snapshot from dir.
func LoadSnapshotFrom(ctx context.Context, dir string) (*ActiveSnapshot, error) {
	path, err := findSnapshotFile(dir, session.IDFromCtx(ctx))
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
func SaveActiveSnapshot(ctx context.Context, a ActiveSnapshot) error {
	dir, err := StateDir()
	if err != nil {
		return err
	}
	return SaveSnapshotTo(ctx, dir, a)
}

// SaveSnapshotTo writes the active snapshot to dir.
func SaveSnapshotTo(ctx context.Context, dir string, a ActiveSnapshot) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, snapshotFileName(session.IDFromCtx(ctx))), data, 0o644)
}

// ClearActiveSnapshot removes the active snapshot state file.
func ClearActiveSnapshot(ctx context.Context) error {
	dir, err := StateDir()
	if err != nil {
		return err
	}
	return ClearSnapshotFrom(ctx, dir)
}

// ClearSnapshotFrom removes the active snapshot state file in dir.
func ClearSnapshotFrom(ctx context.Context, dir string) error {
	path, err := findSnapshotFile(dir, session.IDFromCtx(ctx))
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	return os.Remove(path)
}

// findSnapshotFile returns the snapshot state file path in dir, or "" if it doesn't exist.
func findSnapshotFile(dir, sessionID string) (string, error) {
	return statOrEmpty(filepath.Join(dir, snapshotFileName(sessionID)))
}
