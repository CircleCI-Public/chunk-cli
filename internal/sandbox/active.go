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

const sandboxFile = "sandbox"

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
	return os.WriteFile(filepath.Join(dir, sandboxFile), data, 0o644)
}

// saveDir returns the .chunk directory to write into. It prefers an existing
// .chunk/sandbox found by walking upward; falls back to cwd/.chunk.
func saveDir() (string, error) {
	existing, err := findSandboxFile()
	if err != nil {
		return "", err
	}
	if existing != "" {
		return filepath.Dir(existing), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return filepath.Join(cwd, ".chunk"), nil
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
func findSandboxFile() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(dir, ".chunk", sandboxFile)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
