package config

import (
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

func configHome() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve config home: %w", err)
	}
	return filepath.Join(home, ".config"), nil
}

func stateHome() (string, error) {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve state home: %w", err)
	}
	return filepath.Join(home, ".local", "state"), nil
}
