package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/envspec"
	"github.com/CircleCI-Public/chunk-cli/internal/jsonmerge"
)

// Command roles describe what a command does. Only RoleGate is acted on:
// sidecar setup marks gate commands for remote execution.
const (
	RoleGate    = "gate"    // pass/fail check
	RoleAutofix = "autofix" // rewrites files (formatters)
)

// Command is a single validation command.
type Command struct {
	Name         string `json:"name"`
	Run          string `json:"run"`
	Role         string `json:"role,omitempty"`
	Timeout      int    `json:"timeout,omitempty"`
	Remote       bool   `json:"remote,omitempty"`
	SidecarImage string `json:"sidecarImage,omitempty"`
}

// VCSConfig holds VCS configuration for the project.
type VCSConfig struct {
	Org  string `json:"org,omitempty"`
	Repo string `json:"repo,omitempty"`
}

// ValidationConfig holds project-level defaults for validation behaviour.
type ValidationConfig struct {
	SidecarImage string `json:"sidecarImage,omitempty"`
}

// ProjectConfig is the per-repo configuration stored in .chunk/config.json.
type ProjectConfig struct {
	Commands            []Command            `json:"commands,omitempty"`
	VCS                 *VCSConfig           `json:"vcs,omitempty"`
	Validation          *ValidationConfig    `json:"validation,omitempty"`
	OrgID               string               `json:"orgID,omitempty"`
	StopHookMaxAttempts int                  `json:"stopHookMaxAttempts,omitempty"`
	Environment         *envspec.Environment `json:"environment,omitempty"`
}

// ErrParseProjectConfig marks a .chunk/config.json that exists but is not valid
// JSON. Telling that apart from a missing file lets write paths refuse to
// overwrite a config they could not read.
var ErrParseProjectConfig = errors.New("parse config.json")

func projectConfigPath(workDir string) string {
	return filepath.Join(workDir, ".chunk", "config.json")
}

// LoadProjectConfig reads .chunk/config.json from workDir. A missing file
// reports fs.ErrNotExist and a malformed one ErrParseProjectConfig, both via
// errors.Is; callers that write the config back must distinguish them — see
// LoadProjectConfigForUpdate.
func LoadProjectConfig(workDir string) (*ProjectConfig, error) {
	data, err := os.ReadFile(projectConfigPath(workDir))
	if err != nil {
		return nil, fmt.Errorf("could not read config.json: %w", err)
	}
	var cfg ProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParseProjectConfig, err)
	}
	// Configs written by earlier versions may carry a "test" step in the saved
	// environment. Drop it on load so it is neither run as a setup step nor
	// written back out on the next save.
	cfg.Environment = cfg.Environment.ForConfig()
	return &cfg, nil
}

// LoadProjectConfigForUpdate loads the project config for a read-modify-write
// cycle. A missing .chunk/config.json yields an empty config, but one that
// exists and does not parse is an error: saving on top of it would discard
// everything the user has in it.
func LoadProjectConfigForUpdate(workDir string) (*ProjectConfig, error) {
	cfg, err := LoadProjectConfig(workDir)
	switch {
	case err == nil:
		return cfg, nil
	case errors.Is(err, fs.ErrNotExist):
		return &ProjectConfig{}, nil
	default:
		return nil, err
	}
}

// UnknownProjectConfigKeys returns the paths in .chunk/config.json that chunk
// does not recognize. They are preserved on save; reporting them lets a caller
// point out a typo instead of keeping it forever. A missing or malformed file
// has nothing to report.
func UnknownProjectConfigKeys(workDir string) []string {
	data, err := os.ReadFile(projectConfigPath(workDir))
	if err != nil {
		return nil
	}
	return jsonmerge.UnknownKeys(data, &ProjectConfig{})
}

// HasCommands reports whether any commands are configured.
func (c *ProjectConfig) HasCommands() bool {
	return len(c.Commands) > 0
}

// HasRemoteCommands reports whether any commands are marked for remote execution.
func (c *ProjectConfig) HasRemoteCommands() bool {
	if c == nil {
		return false
	}
	for _, cmd := range c.Commands {
		if cmd.Remote {
			return true
		}
	}
	return false
}

// HasSidecarImage reports whether a project-level sidecar snapshot image is configured.
func (c *ProjectConfig) HasSidecarImage() bool {
	return c != nil && c.Validation != nil && c.Validation.SidecarImage != ""
}

func commandEligibleForSidecarRemote(cmd Command) bool {
	if cmd.Remote {
		return false
	}
	if cmd.Name == "install" {
		return true
	}
	return cmd.Role == RoleGate
}

// MarkRemoteCommandsForSidecarSetup marks install and gate commands for remote
// execution after a successful sidecar setup. Returns true when any command
// was updated.
func (c *ProjectConfig) MarkRemoteCommandsForSidecarSetup() bool {
	if c == nil {
		return false
	}
	changed := false
	for i := range c.Commands {
		if commandEligibleForSidecarRemote(c.Commands[i]) {
			c.Commands[i].Remote = true
			changed = true
		}
	}
	return changed
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

// SaveProjectConfig writes the config back to .chunk/config.json, preserving any
// keys in the existing file that ProjectConfig does not model. Every key it does
// model comes from cfg, so callers must load before saving.
//
// A file that does not parse is replaced, because `chunk init --force` has to be
// able to overwrite one. Callers that must not do that use
// LoadProjectConfigForUpdate, which refuses before the write.
func SaveProjectConfig(workDir string, cfg *ProjectConfig) error {
	dir := filepath.Join(workDir, ".chunk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := projectConfigPath(workDir)
	data, err := marshalOverwriting(path, cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// SaveCommand upserts a command in .chunk/config.json.
func SaveCommand(workDir, name, command string) error {
	cfg, err := LoadProjectConfigForUpdate(workDir)
	if err != nil {
		return err
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
