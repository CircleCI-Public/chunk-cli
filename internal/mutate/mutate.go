// Package mutate implements mutation testing stages for chunk mutate.
package mutate

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// Mutation is a single candidate change to production code.
type Mutation struct {
	ID       string `json:"id"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Col      int    `json:"col"`
	Operator string `json:"operator"`
	Status   string `json:"status"` // RUNNABLE or NOT COVERED
	Before   string `json:"before"` // original token, e.g. "<"
	After    string `json:"after"`  // mutated token, e.g. "<="
}

// EnumeratorFor returns the GoEnumerator for the project.
// It consults cfg.Environment.Stack first, then falls back to file
// pattern detection in dir.
func EnumeratorFor(dir string, cfg *config.ProjectConfig) (*GoEnumerator, error) {
	stack := detectedStack(dir, cfg)
	switch stack {
	case "go":
		return &GoEnumerator{}, nil
	default:
		return nil, fmt.Errorf("no mutation enumerator for stack %q — supported: go", stack)
	}
}

// detectedStack returns the stack name from config if set, otherwise
// detects it from well-known marker files in dir.
func detectedStack(dir string, cfg *config.ProjectConfig) string {
	if cfg != nil && cfg.Environment != nil && cfg.Environment.Stack != "" {
		return cfg.Environment.Stack
	}
	markers := map[string]string{
		"go.mod":         "go",
		"package.json":   "node",
		"Cargo.toml":     "rust",
		"pom.xml":        "java",
		"setup.py":       "python",
		"pyproject.toml": "python",
	}
	for file, stack := range markers {
		if _, err := os.Stat(filepath.Join(dir, file)); err == nil {
			return stack
		}
	}
	return ""
}

// DetectedStack is exported for use in the cmd layer.
func DetectedStack(dir string, cfg *config.ProjectConfig) string {
	return detectedStack(dir, cfg)
}
