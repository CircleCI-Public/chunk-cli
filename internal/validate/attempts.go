package validate

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ForceHookFilePath is the sentinel file that, when present, bypasses the
// max-attempts guard so the Stop hook always re-signals the agent.
// Create with: touch .chunk/force-validate
// Remove when done debugging.
const ForceHookFilePath = ".chunk/force-validate"

// ForceHookFileExists reports whether the force-validate sentinel is present.
func ForceHookFileExists(workDir string) bool {
	_, err := os.Stat(filepath.Join(workDir, ForceHookFilePath))
	return err == nil
}

// DefaultMaxAttempts is the default number of consecutive Stop hook failures
// before chunk stops re-signaling the agent for a given set of changes.
const DefaultMaxAttempts = 3

type attemptsState struct {
	ContentHash string `json:"contentHash"`
	Count       int    `json:"count"`
}

func attemptsPath(workDir string) string {
	return filepath.Join(cacheDir(workDir), "stop-hook-attempts.json")
}

// TrackFailedAttempt increments the consecutive failure count for workDir.
// It resets to 1 if the content hash has changed since the last failure.
// Returns the new failure count.
func TrackFailedAttempt(workDir string) int {
	hash := computeContentHash(workDir)

	var state attemptsState
	if data, err := os.ReadFile(attemptsPath(workDir)); err == nil {
		_ = json.Unmarshal(data, &state)
	}

	if state.ContentHash == hash {
		state.Count++
	} else {
		state = attemptsState{ContentHash: hash, Count: 1}
	}

	_ = writeAttemptsState(workDir, state)
	return state.Count
}

// ResetAttempts clears the consecutive failure count for workDir.
// Called on successful validation so future failures start fresh.
func ResetAttempts(workDir string) {
	_ = os.Remove(attemptsPath(workDir))
}

func writeAttemptsState(workDir string, state attemptsState) error {
	dir := cacheDir(workDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(attemptsPath(workDir), data, 0o644)
}
