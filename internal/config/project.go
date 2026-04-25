package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Command roles determine hook placement in settings.json.
const (
	RoleGate     = "gate"     // enforce at Stop, run-and-record at PreToolUse
	RolePrecheck = "precheck" // check at PreToolUse, enforce at Stop (changed-file variants)
	RoleAutofix  = "autofix"  // run at PreToolUse, enforce at Stop (formatters)
)

// Command is a single validation command.
type Command struct {
	Name    string `json:"name"`
	Run     string `json:"run"`
	Role    string `json:"role,omitempty"`
	FileExt string `json:"fileExt,omitempty"`
	Timeout int    `json:"timeout,omitempty"`
	Limit   int    `json:"limit,omitempty"`
	Always  bool   `json:"always,omitempty"`
	Staged  bool   `json:"staged,omitempty"`
	Remote  bool   `json:"remote,omitempty"`
}

// TaskConfig holds task delegation configuration.
type TaskConfig struct {
	Instructions string `json:"instructions,omitempty"`
	Schema       string `json:"schema,omitempty"`
	Limit        int    `json:"limit,omitempty"`
	Always       bool   `json:"always,omitempty"`
	Timeout      int    `json:"timeout,omitempty"`
}

// VCSConfig holds VCS configuration for the project.
type VCSConfig struct {
	Org  string `json:"org,omitempty"`
	Repo string `json:"repo,omitempty"`
}

// ProjectConfig is the per-repo configuration stored in .chunk/config.json.
type ProjectConfig struct {
	Commands            []Command             `json:"commands,omitempty"`
	Triggers            map[string][]string   `json:"triggers,omitempty"`
	Tasks               map[string]TaskConfig `json:"tasks,omitempty"`
	VCS                 *VCSConfig            `json:"vcs,omitempty"`
	StopHookMaxAttempts int                   `json:"stopHookMaxAttempts,omitempty"`
}

// LoadProjectConfig reads .chunk/config.json from workDir.
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

// HasCommands reports whether any commands are configured.
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

// SaveProjectConfig writes the config back to .chunk/config.json.
func SaveProjectConfig(workDir string, cfg *ProjectConfig) error {
	dir := filepath.Join(workDir, ".chunk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), append(data, '\n'), 0o644)
}

// SaveCommand upserts a command in .chunk/config.json.
func SaveCommand(workDir, name, command string) error {
	cfg, err := LoadProjectConfig(workDir)
	if err != nil {
		cfg = &ProjectConfig{}
	}

	found := false
	for i := range cfg.Commands {
		if cfg.Commands[i].Name == name {
			cfg.Commands[i].Run = command
			found = true
			break
		}
	}
	if !found {
		cfg.Commands = append(cfg.Commands, Command{Name: name, Run: command})
	}

	return SaveProjectConfig(workDir, cfg)
}
