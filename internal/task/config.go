package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const orgTypeGitHub = "github"

var uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type RunDefinition struct {
	DefinitionID       string  `json:"definition_id"`
	Description        string  `json:"description,omitempty"`
	ChunkEnvironmentID *string `json:"chunk_environment_id"`
	DefaultBranch      string  `json:"default_branch"`
}

// IsValidUUID checks if a string is a valid UUID format.
func IsValidUUID(s string) bool {
	return uuidRegex.MatchString(s)
}

type RunConfig struct {
	OrgID       string                   `json:"org_id"`
	ProjectID   string                   `json:"project_id"`
	OrgType     string                   `json:"org_type"`
	Definitions map[string]RunDefinition `json:"definitions"`
}

func LoadRunConfig(workDir string) (*RunConfig, error) {
	path := filepath.Join(workDir, ".chunk", "run.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("could not read run.json configuration: %w", err)
	}
	var cfg RunConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse run.json: %w", err)
	}
	return &cfg, nil
}

// GetDefinitionByNameOrID looks up a definition by name first, then checks
// if the input is a raw UUID. Returns the definition ID, the chunk environment ID,
// and the default branch.
func GetDefinitionByNameOrID(cfg *RunConfig, nameOrID string) (string, *string, string, error) {
	// Try name lookup first
	if def, ok := cfg.Definitions[nameOrID]; ok {
		return def.DefinitionID, def.ChunkEnvironmentID, def.DefaultBranch, nil
	}

	// Check if it's a raw UUID
	if uuidRegex.MatchString(nameOrID) {
		return nameOrID, nil, "main", nil
	}

	return "", nil, "", fmt.Errorf("unknown definition %q, available: %s", nameOrID, availableDefinitions(cfg))
}

// SaveRunConfig writes run config to .chunk/run.json.
func SaveRunConfig(workDir string, cfg *RunConfig) error {
	dir := filepath.Join(workDir, ".chunk")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create .chunk directory: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run config: %w", err)
	}
	path := filepath.Join(dir, "run.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write run config: %w", err)
	}
	return nil
}

// MapVcsTypeToOrgType maps a VCS type string to the org type used in run config.
// "github" and "gh" map to "github"; everything else maps to "circleci".
func MapVcsTypeToOrgType(vcsType string) string {
	lower := strings.ToLower(vcsType)
	if lower == orgTypeGitHub || lower == "gh" {
		return orgTypeGitHub
	}
	return "circleci"
}

// ConfigExists checks whether .chunk/run.json exists at the given root directory.
func ConfigExists(rootDir string) bool {
	path := filepath.Join(rootDir, ".chunk", "run.json")
	_, err := os.Stat(path)
	return err == nil
}

func availableDefinitions(cfg *RunConfig) string {
	names := ""
	for name := range cfg.Definitions {
		if names != "" {
			names += ", "
		}
		names += name
	}
	return names
}
