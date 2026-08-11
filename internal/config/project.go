package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/envspec"
)

// FixCommand is a command that rewrites files in place (formatters, import
// organizers, linter auto-fix modes). These run locally after every file edit
// via a PostToolUse hook — never on a remote sidecar.
type FixCommand struct {
	Name    string `json:"name"`
	Run     string `json:"run"`
	Timeout int    `json:"timeout,omitempty"`
}

// ValidateCommand is a pass/fail check that gates pushes. These run via a
// pre-push git hook and optionally on a remote sidecar.
type ValidateCommand struct {
	Name         string `json:"name"`
	Run          string `json:"run"`
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
	Fix         []FixCommand         `json:"fix,omitempty"`
	Validate    []ValidateCommand    `json:"validate,omitempty"`
	VCS         *VCSConfig           `json:"vcs,omitempty"`
	Validation  *ValidationConfig    `json:"validation,omitempty"`
	OrgID       string               `json:"orgID,omitempty"`
	Environment *envspec.Environment `json:"environment,omitempty"`
}

// legacyCommand is used only for migrating configs written before the
// fix/validate split. Role "autofix" maps to Fix; everything else maps to Validate.
type legacyCommand struct {
	Name         string `json:"name"`
	Run          string `json:"run"`
	Role         string `json:"role,omitempty"`
	Timeout      int    `json:"timeout,omitempty"`
	Remote       bool   `json:"remote,omitempty"`
	SidecarImage string `json:"sidecarImage,omitempty"`
}

const legacyRoleAutofix = "autofix"

// rawProjectConfig is used for parsing both old and new config formats.
type rawProjectConfig struct {
	Fix         []FixCommand         `json:"fix,omitempty"`
	Validate    []ValidateCommand    `json:"validate,omitempty"`
	Commands    []legacyCommand      `json:"commands,omitempty"` // legacy
	VCS         *VCSConfig           `json:"vcs,omitempty"`
	Validation  *ValidationConfig    `json:"validation,omitempty"`
	OrgID       string               `json:"orgID,omitempty"`
	Environment *envspec.Environment `json:"environment,omitempty"`

	// Ignored legacy field — present in old configs, dropped on save.
	StopHookMaxAttempts int `json:"stopHookMaxAttempts,omitempty"`
}

// LoadProjectConfig reads .chunk/config.json from workDir.
func LoadProjectConfig(workDir string) (*ProjectConfig, error) {
	path := filepath.Join(workDir, ".chunk", "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read config.json: %w", err)
	}
	var raw rawProjectConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config.json: %w", err)
	}

	cfg := &ProjectConfig{
		Fix:         raw.Fix,
		Validate:    raw.Validate,
		VCS:         raw.VCS,
		Validation:  raw.Validation,
		OrgID:       raw.OrgID,
		Environment: raw.Environment,
	}

	// Migrate from legacy unified commands list.
	if len(raw.Commands) > 0 && len(raw.Fix) == 0 && len(raw.Validate) == 0 {
		for _, cmd := range raw.Commands {
			if cmd.Role == legacyRoleAutofix {
				cfg.Fix = append(cfg.Fix, FixCommand{
					Name:    cmd.Name,
					Run:     cmd.Run,
					Timeout: cmd.Timeout,
				})
			} else {
				cfg.Validate = append(cfg.Validate, ValidateCommand{
					Name:         cmd.Name,
					Run:          cmd.Run,
					Timeout:      cmd.Timeout,
					Remote:       cmd.Remote,
					SidecarImage: cmd.SidecarImage,
				})
			}
		}
	}

	// Configs written by earlier versions may carry a "test" step in the saved
	// environment. Drop it on load so it is neither run as a setup step nor
	// written back out on the next save.
	cfg.Environment = cfg.Environment.ForConfig()
	return cfg, nil
}

// HasFixCommands reports whether any fix commands are configured.
func (c *ProjectConfig) HasFixCommands() bool {
	return len(c.Fix) > 0
}

// HasValidateCommands reports whether any validate commands are configured.
func (c *ProjectConfig) HasValidateCommands() bool {
	return len(c.Validate) > 0
}

// HasCommands reports whether any commands (fix or validate) are configured.
func (c *ProjectConfig) HasCommands() bool {
	return c.HasFixCommands() || c.HasValidateCommands()
}

// HasRemoteCommands reports whether any validate commands are marked for remote execution.
func (c *ProjectConfig) HasRemoteCommands() bool {
	if c == nil {
		return false
	}
	for _, cmd := range c.Validate {
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

// MarkRemoteCommandsForSidecarSetup marks validate commands for remote
// execution after a successful sidecar setup. Returns true when any command
// was updated. Fix commands are never marked remote.
func (c *ProjectConfig) MarkRemoteCommandsForSidecarSetup() bool {
	if c == nil {
		return false
	}
	changed := false
	for i := range c.Validate {
		if !c.Validate[i].Remote {
			c.Validate[i].Remote = true
			changed = true
		}
	}
	return changed
}

// FindValidateCommand returns the validate command with the given name, or nil.
func (c *ProjectConfig) FindValidateCommand(name string) *ValidateCommand {
	for i := range c.Validate {
		if c.Validate[i].Name == name {
			return &c.Validate[i]
		}
	}
	return nil
}

// FindFixCommand returns the fix command with the given name, or nil.
func (c *ProjectConfig) FindFixCommand(name string) *FixCommand {
	for i := range c.Fix {
		if c.Fix[i].Name == name {
			return &c.Fix[i]
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
	data, err := marshalIndent(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), append(data, '\n'), 0o644)
}

// SaveValidateCommand upserts a validate command in .chunk/config.json.
func SaveValidateCommand(workDir, name, command string) error {
	cfg, err := LoadProjectConfig(workDir)
	if err != nil {
		cfg = &ProjectConfig{}
	}

	for i := range cfg.Validate {
		if cfg.Validate[i].Name == name {
			cfg.Validate[i].Run = command
			return SaveProjectConfig(workDir, cfg)
		}
	}
	cfg.Validate = append(cfg.Validate, ValidateCommand{Name: name, Run: command})
	return SaveProjectConfig(workDir, cfg)
}
