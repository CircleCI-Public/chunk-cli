package config

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
)

const appName = "chunk"

// AppState returns XDG_STATE_HOME or ~/.local/state.
func AppState() (string, error) {
	sh, err := stateHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(sh, appName), nil
}

// Dir returns the chunk config directory, respecting XDG_CONFIG_HOME.
func Dir() (string, error) {
	ch, err := configHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(ch, appName), nil
}

// Path returns the full path to config.json.
func Path() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

// AppData returns the chunk data directory, respecting XDG_DATA_HOME.
func AppData() (string, error) {
	dh, err := dataHome()
	if err != nil {
		return "", err
	}
	return filepath.Join(dh, appName), nil
}

// ProjectDataDir returns the per-project data directory keyed by projectRoot.
// The directory name is the hex-encoded SHA-256 of the real absolute path
// (symlinks resolved via EvalSymlinks, falling back to filepath.Clean), which
// ensures callers that discover the root via different means — git's
// --show-toplevel vs. a manual filesystem walk — always hash the same string.
//
// One-time migration: if the resolved path yields an empty directory but the
// old unresolved path has existing data, the old directory is renamed to the
// new location so users with symlinked project roots don't silently lose their
// sidecar/snapshot/event-log state.
func ProjectDataDir(projectRoot string) (string, error) {
	base, err := AppData()
	if err != nil {
		return "", err
	}
	clean := filepath.Clean(projectRoot)
	resolved := clean
	if r, err := filepath.EvalSymlinks(clean); err == nil {
		resolved = r
	}
	newDir := filepath.Join(base, fmt.Sprintf("%x", sha256.Sum256([]byte(resolved))))

	// Only attempt migration when the symlink actually changed the path.
	if resolved != clean {
		oldDir := filepath.Join(base, fmt.Sprintf("%x", sha256.Sum256([]byte(clean))))
		if _, statErr := os.Stat(oldDir); statErr == nil {
			if entries, readErr := os.ReadDir(newDir); readErr != nil || len(entries) == 0 {
				// Best-effort rename; ignore errors so callers always get a usable path.
				_ = os.Rename(oldDir, newDir)
			}
		}
	}

	return newDir, nil
}

func configHome() (string, error) {
	if v := os.Getenv(EnvXDGConfigHome); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve config home: %w", err)
	}
	return filepath.Join(home, ".config"), nil
}

func stateHome() (string, error) {
	if v := os.Getenv(EnvXDGStateHome); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve state home: %w", err)
	}
	return filepath.Join(home, ".local", "state"), nil
}

func dataHome() (string, error) {
	if v := os.Getenv(EnvXDGDataHome); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve data home: %w", err)
	}
	return filepath.Join(home, ".local", "share"), nil
}
