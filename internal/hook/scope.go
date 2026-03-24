package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// ActivateScope reads stdin JSON and writes a scope marker if a session is present.
func ActivateScope(projectDir string, stdin io.Reader) error {
	raw, err := readStdinJSON(stdin)
	if err != nil {
		// No valid JSON — still exit 0
		return nil
	}

	sessionID, _ := raw["session_id"].(string)
	if sessionID == "" {
		// No session — exit 0 (no-op)
		return nil
	}

	markerPath := filepath.Join(projectDir, ".chunk", "hook", ".chunk-hook-active")
	dir := filepath.Dir(markerPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	data, _ := json.Marshal(map[string]interface{}{
		"sessionId": sessionID,
		"timestamp": time.Now().UnixMilli(),
	})
	return os.WriteFile(markerPath, append(data, '\n'), 0o644)
}

// DeactivateScope removes the scope marker. Requires session_id in stdin.
func DeactivateScope(projectDir string, stdin io.Reader) error {
	raw, err := readStdinJSON(stdin)
	if err != nil {
		return fmt.Errorf("failed to read stdin: session information required")
	}

	sessionID, _ := raw["session_id"].(string)
	if sessionID == "" {
		return fmt.Errorf("session_id required for deactivate: no session found in input")
	}

	markerPath := filepath.Join(projectDir, ".chunk", "hook", ".chunk-hook-active")
	if err := os.Remove(markerPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func readStdinJSON(r io.Reader) (map[string]interface{}, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result, nil
}
