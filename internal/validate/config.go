package validate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type ProjectConfig struct {
	InstallCommand string `json:"installCommand,omitempty"`
	TestCommand    string `json:"testCommand,omitempty"`
}

func LoadProjectConfig(workDir string) (*ProjectConfig, error) {
	path := filepath.Join(workDir, ".chunk", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read config.json: %w", err)
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config.json: %w", err)
	}
	return &cfg, nil
}

func (c *ProjectConfig) HasCommands() bool {
	return c.InstallCommand != "" || c.TestCommand != ""
}
