package config

import (
	"os"
	"path/filepath"
)

// Dir returns the chunk config directory, respecting XDG_CONFIG_HOME.
func Dir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "chunk")
}

// Path returns the full path to config.json.
func Path() string {
	return filepath.Join(Dir(), "config.json")
}
