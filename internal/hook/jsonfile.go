package hook

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// readJSONFile reads a JSON file into dest. Returns false if the file is
// missing or malformed.
func readJSONFile(path string, dest any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, dest) == nil
}

// writeJSONFile atomically writes dest as indented JSON to path,
// creating parent directories as needed.
func writeJSONFile(path string, data any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(jsonData, '\n'), 0o644)
}

// hashID produces a short hex identifier by hashing the given parts
// joined with colons.
func hashID(parts ...string) string {
	joined := ""
	for i, p := range parts {
		if i > 0 {
			joined += ":"
		}
		joined += p
	}
	h := sha256.Sum256([]byte(joined))
	return fmt.Sprintf("%x", h[:8])
}
