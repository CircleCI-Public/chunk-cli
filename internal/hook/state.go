package hook

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// StateSave reads event JSON from stdin and stores it keyed by event name.
func StateSave(sentinelDir, projectDir string, stdin io.Reader) error {
	raw, err := readStdinJSON(stdin)
	if err != nil {
		// No valid input — exit 0
		return nil
	}

	eventName, _ := raw["hook_event_name"].(string)
	if eventName == "" {
		return nil
	}

	sessionID, _ := raw["session_id"].(string)

	state := readState(sentinelDir, projectDir)

	// Session-aware: if different session, start fresh
	if sessionID != "" {
		storedSession := getSessionID(state)
		if storedSession != "" && storedSession != sessionID {
			state = map[string]interface{}{}
		}
		state["__session"] = map[string]interface{}{"id": sessionID}
	}

	state[eventName] = map[string]interface{}{
		"__entries": []interface{}{raw},
	}

	return writeState(sentinelDir, projectDir, state)
}

// StateAppend reads event JSON from stdin and appends it to existing state.
func StateAppend(sentinelDir, projectDir string, stdin io.Reader) error {
	raw, err := readStdinJSON(stdin)
	if err != nil {
		return nil
	}

	eventName, _ := raw["hook_event_name"].(string)
	if eventName == "" {
		return nil
	}

	sessionID, _ := raw["session_id"].(string)

	state := readState(sentinelDir, projectDir)

	if sessionID != "" {
		storedSession := getSessionID(state)
		if storedSession != "" && storedSession != sessionID {
			state = map[string]interface{}{}
		}
		state["__session"] = map[string]interface{}{"id": sessionID}
	}

	existing, _ := state[eventName].(map[string]interface{})
	if existing == nil {
		existing = map[string]interface{}{}
	}
	entries, _ := existing["__entries"].([]interface{})
	entries = append(entries, raw)
	existing["__entries"] = entries
	state[eventName] = existing

	return writeState(sentinelDir, projectDir, state)
}

// StateLoad outputs stored state as JSON.
func StateLoad(sentinelDir, projectDir, field string, streams iostream.Streams) error {
	state := readState(sentinelDir, projectDir)
	if field != "" {
		data, _ := json.Marshal(state)
		streams.Println(string(data))
		return nil
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	streams.Println(string(data))
	return nil
}

// StateClear clears state for the project.
func StateClear(sentinelDir, projectDir string, stdin io.Reader) error {
	raw, _ := readStdinJSON(stdin)
	sessionID, _ := raw["session_id"].(string)

	if sessionID != "" {
		state := readState(sentinelDir, projectDir)
		storedSession := getSessionID(state)
		if storedSession != "" && storedSession != sessionID {
			return nil // Different session, skip
		}
	}

	path := statePath(sentinelDir, projectDir)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func stateHash(projectDir string) string {
	h := sha256.Sum256([]byte(projectDir))
	return fmt.Sprintf("%x", h[:8])
}

func statePath(sentinelDir, projectDir string) string {
	return filepath.Join(sentinelDir, fmt.Sprintf("state-%s.json", stateHash(projectDir)))
}

func readState(sentinelDir, projectDir string) map[string]interface{} {
	path := statePath(sentinelDir, projectDir)
	data, err := os.ReadFile(path)
	if err != nil {
		return map[string]interface{}{}
	}
	var state map[string]interface{}
	if err := json.Unmarshal(data, &state); err != nil {
		return map[string]interface{}{}
	}
	return state
}

func writeState(sentinelDir, projectDir string, state map[string]interface{}) error {
	if err := os.MkdirAll(sentinelDir, 0o755); err != nil {
		return err
	}
	path := statePath(sentinelDir, projectDir)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

func getSessionID(state map[string]interface{}) string {
	session, ok := state["__session"].(map[string]interface{})
	if !ok {
		return ""
	}
	id, _ := session["id"].(string)
	return id
}
