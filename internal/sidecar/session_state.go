package sidecar

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

type sessionState struct {
	SessionID string `json:"session_id"`
}

// SaveSessionID persists the Claude Code session ID for the current project.
// workDir is any directory within the project; the git root is resolved from it.
func SaveSessionID(workDir, sessionID string) error {
	dir, err := projectDataDirFor(workDir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(sessionState{SessionID: sessionID})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "session.json"), data, 0o644)
}

// LoadSessionID returns the stored Claude Code session ID for the project at workDir.
// Returns "" if no session has been recorded or on any error.
func LoadSessionID(workDir string) string {
	dir, err := projectDataDirFor(workDir)
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(dir, "session.json"))
	if err != nil {
		return ""
	}
	var s sessionState
	if err := json.Unmarshal(data, &s); err != nil {
		return ""
	}
	return s.SessionID
}

// projectDataDirFor returns the per-project data directory, resolving the git
// root from workDir (or using workDir itself when not inside a git repo).
func projectDataDirFor(workDir string) (string, error) {
	root := workDir
	if r, err := findGitRootFrom(workDir); err == nil && r != "" {
		root = r
	}
	return config.ProjectDataDir(root)
}

// findGitRootFrom walks up from start and returns the first directory
// containing .git, or "" if none is found.
func findGitRootFrom(start string) (string, error) {
	dir := filepath.Clean(start)
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
