package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// MarkerRel is the scope marker file path relative to project root.
const MarkerRel = ".chunk/hook/.chunk-hook-active"

// defaultMarkerTTLMs is the default TTL for scope markers (5 minutes).
const defaultMarkerTTLMs = 5 * 60 * 1000

// MarkerContent is stored in the scope marker file.
type MarkerContent struct {
	SessionID string `json:"sessionId"`
	Timestamp int64  `json:"timestamp"`
}

// markerTTLMs returns the effective marker TTL from env or default.
func markerTTLMs() int64 {
	val := os.Getenv("CHUNK_HOOK_MARKER_TTL_MS")
	if val == "" {
		return defaultMarkerTTLMs
	}
	n, err := strconv.ParseInt(val, 10, 64)
	if err != nil || n < 0 {
		return defaultMarkerTTLMs
	}
	return n
}

func isExpired(marker *MarkerContent) bool {
	return time.Now().UnixMilli()-marker.Timestamp > markerTTLMs()
}

// pathKeys are keys in tool_input that commonly hold absolute file paths.
var pathKeys = []string{"file_path", "filePath", "path", "file", "directory", "dir", "command"}

// extractFilePaths extracts absolute file paths from the raw stdin JSON payload.
func extractFilePaths(raw map[string]interface{}) []string {
	toolInput, ok := raw["tool_input"].(map[string]interface{})
	if !ok {
		return nil
	}

	var paths []string
	for _, key := range pathKeys {
		val, ok := toolInput[key].(string)
		if !ok || val == "" {
			continue
		}

		if key == "command" {
			// Extract first absolute-path token, stopping at shell operators
			idx := strings.Index(val, "/")
			if idx == -1 {
				continue
			}
			rest := val[idx:]
			// Stop at shell metacharacters
			end := len(rest)
			for i, c := range rest {
				if c == ' ' || c == ';' || c == '|' || c == '>' || c == '&' || c == ')' {
					end = i
					break
				}
			}
			if end > 0 {
				paths = append(paths, rest[:end])
			}
		} else if strings.HasPrefix(val, "/") {
			paths = append(paths, val)
		}
	}

	return paths
}

// matchesProject checks if any extracted paths reference the project directory.
// Returns "match", "no-paths", or "mismatch".
func matchesProject(projectDir string, raw map[string]interface{}) string {
	paths := extractFilePaths(raw)
	if len(paths) == 0 {
		return "no-paths"
	}
	prefix := projectDir + "/"
	for _, p := range paths {
		if strings.HasPrefix(p, prefix) || p == projectDir {
			return "match"
		}
	}
	return "mismatch"
}

// writeMarker writes the marker file with session ID and timestamp.
func writeMarker(projectDir, sessionID string) error {
	markerPath := filepath.Join(projectDir, MarkerRel)
	dir := filepath.Dir(markerPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	content := MarkerContent{SessionID: sessionID, Timestamp: time.Now().UnixMilli()}
	data, _ := json.Marshal(content)
	return os.WriteFile(markerPath, append(data, '\n'), 0o644)
}

// ReadMarker reads the marker file. Returns nil if absent or malformed.
func ReadMarker(projectDir string) *MarkerContent {
	markerPath := filepath.Join(projectDir, MarkerRel)
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return nil
	}
	var marker MarkerContent
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil
	}
	if marker.SessionID == "" {
		return nil
	}
	return &marker
}

// ActivateScope reads stdin JSON and activates scope if file paths reference the project.
// Returns nil on success (always exits 0 for the hook).
func ActivateScope(projectDir string, stdin io.Reader) error {
	raw, err := readStdinJSON(stdin)
	if err != nil {
		return nil // No valid JSON, exit 0
	}

	sessionID, _ := raw["session_id"].(string)
	match := matchesProject(projectDir, raw)
	shouldActivate := sessionID != "" && match == "match"

	if shouldActivate {
		// Subagent safety: preserve existing marker from different session unless expired
		existing := ReadMarker(projectDir)
		if existing != nil && existing.SessionID != sessionID {
			if !isExpired(existing) {
				return nil // Existing marker still valid, keep it
			}
			// Expired marker from dead session, reclaim it
		}
		return writeMarker(projectDir, sessionID)
	}

	// Fallback: check existing marker
	existing := ReadMarker(projectDir)
	if existing == nil {
		return nil // Not active
	}

	// Session validation if we have a session ID
	if sessionID != "" && existing.SessionID != "" && sessionID != existing.SessionID {
		if !isExpired(existing) {
			return nil // Different session, marker still valid for owner
		}
		// Expired marker, reclaim
		return writeMarker(projectDir, sessionID)
	}

	// Same session or no session to compare: refresh timestamp
	if sessionID != "" && existing.SessionID == sessionID {
		return writeMarker(projectDir, sessionID)
	}

	return nil
}

// DeactivateScope removes the scope marker. Session-aware: only removes if same session.
func DeactivateScope(projectDir string, stdin io.Reader) error {
	raw, err := readStdinJSON(stdin)
	if err != nil {
		return fmt.Errorf("failed to read stdin: session information required")
	}

	sessionID, _ := raw["session_id"].(string)
	if sessionID == "" {
		return fmt.Errorf("session_id required for deactivate: no session found in input")
	}

	// Session-aware: only remove if marker belongs to same session
	marker := ReadMarker(projectDir)
	if marker != nil && marker.SessionID != sessionID {
		return nil // Different session, skip
	}

	markerPath := filepath.Join(projectDir, MarkerRel)
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
