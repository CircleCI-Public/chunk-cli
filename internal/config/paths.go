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
// The directory name is the hex-encoded SHA-256 of the cleaned absolute path,
// which is guaranteed collision-free across all valid path strings.
func ProjectDataDir(projectRoot string) (string, error) {
	base, err := AppData()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(filepath.Clean(projectRoot)))
	return filepath.Join(base, fmt.Sprintf("%x", sum)), nil
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
