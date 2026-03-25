package hook

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
)

// SentinelData is the shape of a sentinel JSON file.
type SentinelData struct {
	Status            string `json:"status"`
	StartedAt         string `json:"startedAt"`
	FinishedAt        string `json:"finishedAt,omitempty"`
	ExitCode          int    `json:"exitCode,omitempty"`
	Command           string `json:"command,omitempty"`
	ConfiguredCommand string `json:"configuredCommand,omitempty"`
	Output            string `json:"output,omitempty"`
	Details           string `json:"details,omitempty"`
	Project           string `json:"project,omitempty"`
	Skipped           bool   `json:"skipped,omitempty"`
	RawResult         string `json:"rawResult,omitempty"`
	SessionID         string `json:"sessionId,omitempty"`
	ContentHash       string `json:"contentHash,omitempty"`
}

var safeNameRe = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sentinelID(projectDir, name string) string {
	h := sha256.Sum256([]byte(projectDir + ":" + name))
	safe := safeNameRe.ReplaceAllString(name, "-")
	return fmt.Sprintf("%s-%x", safe, h[:8])
}

// SentinelPath returns the full path to a sentinel file.
func SentinelPath(sentinelDir, projectDir, name string) string {
	return filepath.Join(sentinelDir, sentinelID(projectDir, name)+".json")
}

func writeSentinel(sentinelDir, projectDir, name string, data SentinelData) error {
	if err := os.MkdirAll(sentinelDir, 0o755); err != nil {
		return fmt.Errorf("creating sentinel dir: %w", err)
	}
	path := SentinelPath(sentinelDir, projectDir, name)
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling sentinel: %w", err)
	}
	return os.WriteFile(path, append(jsonData, '\n'), 0o644)
}

// readSentinel reads a sentinel file, returning nil if missing or malformed.
func readSentinel(sentinelDir, projectDir, name string) *SentinelData {
	path := SentinelPath(sentinelDir, projectDir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s SentinelData
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	return &s
}

// removeSentinel removes a sentinel file if it exists.
func removeSentinel(sentinelDir, projectDir, name string) {
	path := SentinelPath(sentinelDir, projectDir, name)
	_ = os.Remove(path)
}

// blockCountPath returns the path to the block counter file.
func blockCountPath(sentinelDir, projectDir, name string) string {
	return filepath.Join(sentinelDir, sentinelID(projectDir, name)+".blocks")
}

// readBlockCount reads the current block count (0 if no file exists).
func readBlockCount(sentinelDir, projectDir, name string) int {
	data, err := os.ReadFile(blockCountPath(sentinelDir, projectDir, name))
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(string(data))
	if err != nil {
		return 0
	}
	return n
}

// incrementBlockCount increments and persists the block counter. Returns the new count.
func incrementBlockCount(sentinelDir, projectDir, name string) int {
	_ = os.MkdirAll(sentinelDir, 0o755)
	count := readBlockCount(sentinelDir, projectDir, name) + 1
	_ = os.WriteFile(blockCountPath(sentinelDir, projectDir, name), []byte(strconv.Itoa(count)), 0o644)
	return count
}

// resetBlockCount resets (removes) the block counter.
func resetBlockCount(sentinelDir, projectDir, name string) {
	_ = os.Remove(blockCountPath(sentinelDir, projectDir, name))
}
