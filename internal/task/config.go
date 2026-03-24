package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

var uuidRegex = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type RunDefinition struct {
	DefinitionID       string  `json:"definition_id"`
	ChunkEnvironmentID *string `json:"chunk_environment_id"`
	DefaultBranch      string  `json:"default_branch"`
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

	return "", nil, "", fmt.Errorf("Unknown definition %q. Available: %s", nameOrID, availableDefinitions(cfg))
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
