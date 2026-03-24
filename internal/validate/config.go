package validate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Command struct {
	Name string `json:"name"`
	Run  string `json:"run"`
}

type ProjectConfig struct {
	Commands []Command `json:"commands"`
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
	return len(c.Commands) > 0
}

// FindCommand returns the command with the given name, or nil if not found.
func (c *ProjectConfig) FindCommand(name string) *Command {
	for i := range c.Commands {
		if c.Commands[i].Name == name {
			return &c.Commands[i]
		}
	}
	return nil
}
