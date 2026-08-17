package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	json "github.com/go-json-experiment/json"
	"github.com/go-json-experiment/json/jsontext"

	"github.com/CircleCI-Public/chunk-cli/internal/envspec"
)

// Command roles describe what a command does. Only RoleGate is acted on:
// sidecar setup marks gate commands for remote execution.
const (
	RoleGate    = "gate"    // pass/fail check
	RoleAutofix = "autofix" // rewrites files (formatters)
)

// Command is a single validation command.
//
// Numeric and boolean fields use omitzero rather than omitempty: under
// encoding/json/v2 omitempty only drops values that encode as an empty JSON
// value, so omitempty would write "timeout": 0 into every command.
type Command struct {
	Name         string `json:"name"`
	Run          string `json:"run"`
	Role         string `json:"role,omitempty"`
	Timeout      int    `json:"timeout,omitzero"`
	Remote       bool   `json:"remote,omitzero"`
	SidecarImage string `json:"sidecarImage,omitempty"`

	// Extra holds object members this type does not model, so a write does not
	// delete keys hand-added to an individual command. See SaveProjectConfig.
	Extra jsontext.Value `json:",embed"`
}

// VCSConfig holds VCS configuration for the project.
type VCSConfig struct {
	Org  string `json:"org,omitempty"`
	Repo string `json:"repo,omitempty"`

	// Extra holds object members this type does not model. See Command.Extra.
	Extra jsontext.Value `json:",embed"`
}

// ValidationConfig holds project-level defaults for validation behaviour.
type ValidationConfig struct {
	SidecarImage string `json:"sidecarImage,omitempty"`

	// Extra holds object members this type does not model. See Command.Extra.
	Extra jsontext.Value `json:",embed"`
}

// ProjectConfig is the per-repo configuration stored in .chunk/config.json.
type ProjectConfig struct {
	Commands            []Command            `json:"commands,omitempty"`
	VCS                 *VCSConfig           `json:"vcs,omitempty"`
	Validation          *ValidationConfig    `json:"validation,omitempty"`
	OrgID               string               `json:"orgID,omitempty"`
	StopHookMaxAttempts int                  `json:"stopHookMaxAttempts,omitzero"`
	Environment         *envspec.Environment `json:"environment,omitempty"`

	// Extra holds object members this type does not model. See Command.Extra.
	Extra jsontext.Value `json:",embed"`
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

// UnknownKeys returns the paths in the loaded config that chunk does not model.
// They are preserved on save; reporting them lets a caller point out a typo
// instead of keeping it forever.
//
// The set of types that can appear in the file is small and closed, so the walk
// is spelled out rather than derived by reflection. A new nested section with an
// Extra field needs a branch here, or its keys are preserved but never reported.
func (c *ProjectConfig) UnknownKeys() []string {
	var paths []string
	paths = appendUnknownKeys(paths, "", c.Extra)
	for _, cmd := range c.Commands {
		paths = appendUnknownKeys(paths, "commands[]", cmd.Extra)
	}
	if c.VCS != nil {
		paths = appendUnknownKeys(paths, "vcs", c.VCS.Extra)
	}
	if c.Validation != nil {
		paths = appendUnknownKeys(paths, "validation", c.Validation.Extra)
	}
	if c.Environment != nil {
		paths = appendUnknownKeys(paths, "environment", c.Environment.Extra)
		for _, s := range c.Environment.Setup {
			paths = appendUnknownKeys(paths, "environment.setup[]", s.Extra)
		}
	}
	slices.Sort(paths)
	return slices.Compact(paths)
}

// appendUnknownKeys adds each member name in extra to paths, prefixed with the
// path of the object it was found on.
func appendUnknownKeys(paths []string, prefix string, extra jsontext.Value) []string {
	if len(extra) == 0 {
		return paths
	}
	var members map[string]jsontext.Value
	if err := json.Unmarshal(extra, &members); err != nil {
		return paths
	}
	for name := range members {
		if prefix == "" {
			paths = append(paths, name)
			continue
		}
		paths = append(paths, prefix+"."+name)
	}
	return paths
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

// SaveProjectConfig writes the config back to .chunk/config.json. Keys the file
// had that ProjectConfig does not model ride along in the Extra fields and are
// written back out, so callers must load before saving: a config built from
// scratch has no unknown keys to carry and would drop the ones on disk.
//
// An existing file that does not parse is refused with ErrParseProjectConfig and
// left alone, because cfg cannot be carrying its unknown keys — nothing could
// have read them. Use OverwriteProjectConfig for the one caller whose job is to
// replace such a file.
func SaveProjectConfig(workDir string, cfg *ProjectConfig) error {
	path := projectConfigPath(workDir)
	data, err := os.ReadFile(path)
	if err == nil {
		var probe ProjectConfig
		if err := json.Unmarshal(data, &probe); err != nil {
			return fmt.Errorf("%w: %w", ErrParseProjectConfig, err)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("could not read config.json: %w", err)
	}
	return OverwriteProjectConfig(workDir, cfg)
}

// OverwriteProjectConfig writes cfg to .chunk/config.json without reading what
// is already there, replacing an unparseable file rather than refusing it.
//
// This is the destructive path and has to be asked for by name: `chunk init
// --force` exists to overwrite a config nobody can fix by hand. Every other
// writer wants SaveProjectConfig.
func OverwriteProjectConfig(workDir string, cfg *ProjectConfig) error {
	dir := filepath.Join(workDir, ".chunk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := marshalIndent(cfg)
	if err != nil {
		return err
	}
	return writeFileAtomic(projectConfigPath(workDir), append(data, '\n'), 0o644)
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
