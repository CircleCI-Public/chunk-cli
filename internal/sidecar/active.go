package sidecar

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
)

// ActiveSidecar holds the currently active sidecar for a project.
type ActiveSidecar struct {
	SidecarID string `json:"sidecar_id"`
	Name      string `json:"name,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// currentBranch returns the current git branch for the repo rooted at root.
// Returns "" on any error (no git, detached HEAD, etc.).
func currentBranch(root string) string {
	var out bytes.Buffer
	cmd := exec.Command("git", "-C", root, "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	b := strings.TrimSpace(out.String())
	if b == "HEAD" {
		return "" // detached HEAD
	}
	return b
}

// sidecarFileName returns the name of the sidecar state file.
//   - Both empty → "sidecar.json" (legacy fallback)
//   - Session only → "sidecar.<sessionID>.json" (unchanged behaviour)
//   - Both present → "sidecar.<sessionID>-<hash8>.json" where hash8 is the first
//     8 hex chars of sha256(sessionID + ":" + branch), encoding the branch uniquely.
func sidecarFileName(sessionID, branch string) string {
	if sessionID == "" {
		return "sidecar.json"
	}
	if branch == "" {
		return "sidecar." + sessionID + ".json"
	}
	sum := sha256.Sum256([]byte(sessionID + ":" + branch))
	hash8 := fmt.Sprintf("%x", sum[:4])
	return "sidecar." + sessionID + "-" + hash8 + ".json"
}

// StateFileName returns the sidecar state file name for the given session ID
// and git branch. Exposed so acceptance tests can construct expected paths.
func StateFileName(sessionID, branch string) string {
	return sidecarFileName(sessionID, branch)
}

// StateDir returns the XDG_DATA_HOME directory for the current project.
// Callers performing multiple sidecar or snapshot operations can resolve once
// and pass the result to the dir-accepting variants (LoadActiveFrom, SaveActiveTo,
// ClearActiveFrom, LoadSnapshotFrom, SaveSnapshotTo, ClearSnapshotFrom) to avoid
// repeated filesystem walks.
func StateDir() (string, error) {
	return saveDir()
}

// LoadActive reads the active sidecar for the current project from XDG_DATA_HOME.
// Returns nil if not found.
func LoadActive(ctx context.Context) (*ActiveSidecar, error) {
	dir, err := saveDir()
	if err != nil {
		return nil, err
	}
	return LoadActiveFrom(ctx, dir)
}

// LoadActiveFrom reads the active sidecar from dir.
func LoadActiveFrom(ctx context.Context, dir string) (*ActiveSidecar, error) {
	root, _ := projectRoot()
	branch := currentBranch(root)
	path, err := findSidecarFile(dir, session.IDFromCtx(ctx), branch)
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var a ActiveSidecar
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// SaveActive writes the active sidecar to XDG_DATA_HOME for the current project.
func SaveActive(ctx context.Context, a ActiveSidecar) error {
	dir, err := saveDir()
	if err != nil {
		return err
	}
	return SaveActiveTo(ctx, dir, a)
}

// SaveActiveTo writes the active sidecar to dir.
func SaveActiveTo(ctx context.Context, dir string, a ActiveSidecar) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	root, _ := projectRoot()
	branch := currentBranch(root)
	return os.WriteFile(filepath.Join(dir, sidecarFileName(session.IDFromCtx(ctx), branch)), data, 0o644)
}

// saveDir returns the XDG_DATA_HOME directory for the current project.
func saveDir() (string, error) {
	root, err := projectRoot()
	if err != nil {
		return "", err
	}
	return config.ProjectDataDir(root)
}

// projectRoot returns the git root when inside a git repo, otherwise cwd.
func projectRoot() (string, error) {
	if root, err := findGitRoot(); err == nil && root != "" {
		return root, nil
	}
	return os.Getwd()
}

// findGitRoot walks up from cwd and returns the first directory containing .git,
// or "" if none is found.
func findGitRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
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

// ClearActive removes the active sidecar state file.
func ClearActive(ctx context.Context) error {
	dir, err := saveDir()
	if err != nil {
		return err
	}
	return ClearActiveFrom(ctx, dir)
}

// ClearActiveFrom removes the active sidecar state file in dir.
func ClearActiveFrom(ctx context.Context, dir string) error {
	root, _ := projectRoot()
	branch := currentBranch(root)
	path, err := findSidecarFile(dir, session.IDFromCtx(ctx), branch)
	if err != nil {
		return err
	}
	if path == "" {
		return nil
	}
	return os.Remove(path)
}

// findSidecarFile returns the sidecar state file path in dir, or "" if it doesn't exist.
func findSidecarFile(dir, sessionID, branch string) (string, error) {
	return statOrEmpty(filepath.Join(dir, sidecarFileName(sessionID, branch)))
}

// statOrEmpty returns path if it exists, "" if it does not, or an error for other failures.
func statOrEmpty(path string) (string, error) {
	_, err := os.Stat(path)
	if err == nil {
		return path, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return "", err
}
