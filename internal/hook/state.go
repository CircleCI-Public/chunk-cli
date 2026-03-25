package hook

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// StateSave reads event JSON from stdin and stores it keyed by event name.
func StateSave(sentinelDir, projectDir string, stdin io.Reader) error {
	raw, err := ReadStdinJSON(stdin)
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
	raw, err := ReadStdinJSON(stdin)
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

// StateLoad outputs stored state as JSON. If field is set, resolves a
// dot-separated path (e.g. "UserPromptSubmit.prompt") into the state tree.
func StateLoad(sentinelDir, projectDir, field string, streams iostream.Streams) error {
	state := readState(sentinelDir, projectDir)
	if field != "" {
		val := resolveField(state, field)
		if val == nil {
			return nil
		}
		switch v := val.(type) {
		case string:
			streams.Println(v)
		default:
			data, _ := json.Marshal(v)
			streams.Println(string(data))
		}
		return nil
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	streams.Println(string(data))
	return nil
}

// resolveField walks a field path into nested maps/arrays.
// Supports dot notation and bracket-index notation:
//   - "Event.field" is sugar for "Event[0].field" when Event has __entries
//   - "Event[0].field" accesses __entries[0].field
func resolveField(state map[string]interface{}, field string) interface{} {
	segments := parsePath(field)
	var current interface{} = state
	for _, seg := range segments {
		if current == nil {
			return nil
		}
		switch s := seg.(type) {
		case string:
			m, ok := current.(map[string]interface{})
			if !ok {
				return nil
			}
			val, exists := m[s]
			if !exists {
				return nil
			}
			// __entries sugar: if value is a map with __entries and
			// we're not explicitly requesting __entries, redirect through __entries[0]
			if obj, ok := val.(map[string]interface{}); ok {
				if entries, ok := obj["__entries"].([]interface{}); ok {
					if len(entries) > 0 {
						current = entries[0]
						continue
					}
				}
			}
			current = val
		case int:
			switch v := current.(type) {
			case []interface{}:
				if s < 0 || s >= len(v) {
					return nil
				}
				current = v[s]
			case map[string]interface{}:
				// Bracket access on a map with __entries
				if entries, ok := v["__entries"].([]interface{}); ok {
					if s < 0 || s >= len(entries) {
						return nil
					}
					current = entries[s]
				} else {
					return nil
				}
			default:
				return nil
			}
		}
	}
	return current
}

// parsePath parses "Event[0].field.sub" into ["Event", 0, "field", "sub"].
func parsePath(path string) []interface{} {
	var segments []interface{}
	i := 0
	for i < len(path) {
		switch path[i] {
		case '[':
			close := strings.IndexByte(path[i:], ']')
			if close == -1 {
				return segments
			}
			idx := 0
			for _, c := range path[i+1 : i+close] {
				if c >= '0' && c <= '9' {
					idx = idx*10 + int(c-'0')
				}
			}
			segments = append(segments, idx)
			i += close + 1
			if i < len(path) && path[i] == '.' {
				i++
			}
		case '.':
			i++
		default:
			end := i
			for end < len(path) && path[end] != '.' && path[end] != '[' {
				end++
			}
			segments = append(segments, path[i:end])
			i = end
		}
	}
	return segments
}

// StateClear clears state for the project.
func StateClear(sentinelDir, projectDir string, stdin io.Reader) error {
	raw, _ := ReadStdinJSON(stdin)
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
	return hashID(projectDir)
}

func statePath(sentinelDir, projectDir string) string {
	return filepath.Join(sentinelDir, fmt.Sprintf("state-%s.json", stateHash(projectDir)))
}

func readState(sentinelDir, projectDir string) map[string]interface{} {
	path := statePath(sentinelDir, projectDir)
	var state map[string]interface{}
	if !readJSONFile(path, &state) {
		return map[string]interface{}{}
	}
	return state
}

func writeState(sentinelDir, projectDir string, state map[string]interface{}) error {
	return writeJSONFile(statePath(sentinelDir, projectDir), state)
}

func getSessionID(state map[string]interface{}) string {
	session, ok := state["__session"].(map[string]interface{})
	if !ok {
		return ""
	}
	id, _ := session["id"].(string)
	return id
}
