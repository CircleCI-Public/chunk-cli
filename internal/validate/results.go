package validate

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// CommandResult records whether a named validate command passed or failed.
type CommandResult struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
}

func resultsPath(treeSHA string) string {
	return filepath.Join(os.TempDir(), "chunk-run", "trees", treeSHA+".json")
}

// SaveResults persists per-command results keyed to the given tree SHA.
func SaveResults(treeSHA string, results []CommandResult) error {
	p := resultsPath(treeSHA)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

// LoadResults loads previously saved results for the given tree SHA.
// Returns (nil, false, nil) when no results exist for that SHA.
func LoadResults(treeSHA string) ([]CommandResult, bool, error) {
	data, err := os.ReadFile(resultsPath(treeSHA))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	var results []CommandResult
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, false, err
	}
	return results, true, nil
}
