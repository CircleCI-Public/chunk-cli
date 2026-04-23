package validate

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// DefaultMaxAttempts is the default number of consecutive Stop hook re-signals
// before chunk gives up and tells the agent to ask the user for help.
const DefaultMaxAttempts = 3

type attemptsState struct {
	Count int `json:"count"`
}

func sessionPath(sessionID string) string {
	return filepath.Join(os.TempDir(), "chunk-run", "sessions", sessionID+".json")
}

// TrackFailedAttempt increments the failure counter for the given session and
// returns the new count.
// warn is an optional writer for diagnostic messages (pass nil to suppress).
func TrackFailedAttempt(sessionID string, warn io.Writer) int {
	var state attemptsState
	if data, err := os.ReadFile(sessionPath(sessionID)); err == nil {
		_ = json.Unmarshal(data, &state)
	}
	state.Count++
	if err := writeAttemptsState(sessionID, state); err != nil && warn != nil {
		_, _ = fmt.Fprintf(warn, "chunk validate: could not persist attempt count: %v\n", err)
	}
	return state.Count
}

// ResetAttempts clears the failure counter for the given session.
func ResetAttempts(sessionID string) {
	_ = os.Remove(sessionPath(sessionID))
}

func writeAttemptsState(sessionID string, state attemptsState) error {
	p := sessionPath(sessionID)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}
