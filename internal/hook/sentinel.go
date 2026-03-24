package hook

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// SentinelData is the shape of a sentinel JSON file.
type SentinelData struct {
	Status     string `json:"status"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
	Command    string `json:"command,omitempty"`
	Output     string `json:"output,omitempty"`
	Project    string `json:"project,omitempty"`
	Skipped    bool   `json:"skipped,omitempty"`
	SessionID  string `json:"sessionId,omitempty"`
}

var safeName = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func sentinelID(projectDir, name string) string {
	h := sha256.Sum256([]byte(projectDir + ":" + name))
	safe := safeName.ReplaceAllString(name, "-")
	return fmt.Sprintf("%s-%x", safe, h[:8])
}

func sentinelPath(sentinelDir, projectDir, name string) string {
	return filepath.Join(sentinelDir, sentinelID(projectDir, name)+".json")
}

func writeSentinel(sentinelDir, projectDir, name string, data SentinelData) error {
	if err := os.MkdirAll(sentinelDir, 0o755); err != nil {
		return err
	}
	path := sentinelPath(sentinelDir, projectDir, name)
	jsonData, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(jsonData, '\n'), 0o644)
}
