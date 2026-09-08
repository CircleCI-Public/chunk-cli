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
	"slices"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
)

// ActiveSidecar holds the currently active sidecar(s) for a project. A single
// sidecar is just a group of length one, so every consumer works with the same
// SidecarIDs slice regardless of how many sidecars are in play.
type ActiveSidecar struct {
	SidecarIDs []string `json:"sidecar_ids,omitempty"`
	Name       string   `json:"name,omitempty"`
	// OrgID records which org the sidecar belongs to, so Reap can tell a sidecar
	// that has been deleted from one that simply lives in an org it is not
	// listing. Empty on state written before this field existed.
	OrgID     string `json:"org_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

// ID returns the primary sidecar ID (the first in the group), or "" when no
// sidecar is set.
func (a *ActiveSidecar) ID() string {
	if a == nil || len(a.SidecarIDs) == 0 {
		return ""
	}
	return a.SidecarIDs[0]
}

// UnmarshalJSON reads active-sidecar state, folding the legacy single-valued
// "sidecar_id" field into SidecarIDs so state files written by older versions
// keep working.
func (a *ActiveSidecar) UnmarshalJSON(data []byte) error {
	type alias ActiveSidecar
	aux := struct {
		LegacyID string `json:"sidecar_id"`
		*alias
	}{alias: (*alias)(a)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if len(a.SidecarIDs) == 0 && aux.LegacyID != "" {
		a.SidecarIDs = []string{aux.LegacyID}
	}
	return nil
}

// CurrentBranch returns the current git branch for the repo rooted at root.
// Returns "" on any error (no git, detached HEAD, etc.).
func CurrentBranch(root string) string {
	var out bytes.Buffer
	cmd := exec.Command("git", "-C", root, "rev-parse", "--abbrev-ref", gitHeadRef)
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	b := strings.TrimSpace(out.String())
	if b == gitHeadRef {
		return "" // detached HEAD
	}
	return b
}

const defaultSidecarFile = "sidecar.json"
const gitHeadRef = "HEAD"

// sidecarFileName returns the name of the sidecar state file.
//   - Both empty → "sidecar.json" (legacy fallback)
//   - Session only → "sidecar.<sessionID>.json" (unchanged behaviour)
//   - Both present → "sidecar.<sessionID>-<hash8>.json" where hash8 is the first
//     8 hex chars of sha256(sessionID + ":" + branch), encoding the branch uniquely.
func sidecarFileName(sessionID, branch string) string {
	if sessionID == "" {
		return defaultSidecarFile
	}
	if branch == "" {
		return "sidecar." + sessionID + ".json"
	}
	sum := sha256.Sum256([]byte(sessionID + ":" + branch))
	hash8 := fmt.Sprintf("%x", sum[:4])
	return "sidecar." + sessionID + "-" + hash8 + ".json"
}

// StateFileName returns the sidecar state file name for the given session ID
// and git branch. Exposed so tests can construct expected paths.
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
	branch := CurrentBranch(root)
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
	a.SessionID = session.IDFromCtx(ctx)
	data, err := json.Marshal(a)
	if err != nil {
		return err
	}
	root, _ := projectRoot()
	branch := CurrentBranch(root)
	path := filepath.Join(dir, sidecarFileName(session.IDFromCtx(ctx), branch))
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	pruneRekeyedState(dir, path, a.SidecarIDs)
	// Write a breadcrumb so chunk watch can discover this project.
	_ = os.WriteFile(filepath.Join(dir, "project-root"), []byte(root), 0o644)
	return nil
}

// pruneRekeyedState removes state files other than keep that name any of the
// sidecarIDs in the active group.
//
// A sidecar is re-keyed under the current session and branch when it is adopted,
// or when the same session switches branch. Without this the file it came from
// is left behind holding an out-of-date synced ref, so readers that pick the
// wrong file report a sidecar as permanently needing a sync.
//
// A file belonging to a live session goes too, deliberately. Adoption moves
// state rather than copying it, so another file naming this sidecar means two
// sessions are pointed at one — the state this whole mechanism exists to prevent.
// Dropping it costs that session a re-sync onto a sidecar of its own, which is
// the outcome we want, where keeping it would leave both sessions sharing.
// Best-effort: a file that cannot be removed is left for Reap to clean up.
func pruneRekeyedState(dir, keep string, sidecarIDs []string) {
	if len(sidecarIDs) == 0 {
		return
	}
	entries, err := loadStateEntries(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.path == keep || !slices.ContainsFunc(e.ids(), func(id string) bool {
			return slices.Contains(sidecarIDs, id)
		}) {
			continue
		}
		_ = removeState(e.path)
	}
}

// AllProjectRoots returns the roots of all projects that have ever saved a
// sidecar state, by reading the breadcrumb files written by SaveActiveTo.
func AllProjectRoots() ([]string, error) {
	base, err := config.AppData()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var roots []string
	seen := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		crumb := filepath.Join(base, e.Name(), "project-root")
		data, readErr := os.ReadFile(crumb)
		if readErr != nil {
			continue
		}
		root := strings.TrimSpace(string(data))
		if root == "" || seen[root] {
			continue
		}
		// Only include projects where the root still exists.
		if _, statErr := os.Stat(root); statErr != nil {
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}
	return roots, nil
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

// ClearActiveByOrg removes all sidecar state files across every known project
// whose OrgID matches orgID. It is called after a bulk prune so that the TUI
// does not show deleted sidecars on the next watch tick.
// Returns the number of files removed. Per-project and per-file failures do not
// stop the sweep; they are accumulated into the returned error so the caller can
// warn about a prune that only partially cleared state.
func ClearActiveByOrg(orgID string) (int, error) {
	base, err := config.AppData()
	if err != nil {
		return 0, err
	}
	dirs, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	var errs []error
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		dir := filepath.Join(base, d.Name())
		entries, lerr := loadStateEntries(dir)
		if lerr != nil {
			errs = append(errs, fmt.Errorf("read sidecar state in %s: %w", d.Name(), lerr))
			continue
		}
		for _, e := range entries {
			if e.active.OrgID != orgID {
				continue
			}
			if rerr := removeState(e.path); rerr != nil {
				errs = append(errs, rerr)
				continue
			}
			removed++
		}
	}
	return removed, errors.Join(errs...)
}

// RemoveActiveSidecar removes sidecarID from the active sidecar group. If the
// removed sidecar was the last remaining member, the active state file is
// cleared entirely. Returns true when the active state changed.
func RemoveActiveSidecar(ctx context.Context, sidecarID string) (bool, error) {
	active, err := LoadActive(ctx)
	if err != nil {
		return false, err
	}
	if active == nil || !slices.Contains(active.SidecarIDs, sidecarID) {
		return false, nil
	}

	filtered := slices.DeleteFunc(slices.Clone(active.SidecarIDs), func(id string) bool {
		return id == sidecarID
	})
	if len(filtered) == 0 {
		return true, ClearActive(ctx)
	}

	active.SidecarIDs = filtered
	return true, SaveActive(ctx, *active)
}

// ClearActiveFrom removes the active sidecar state file in dir.
func ClearActiveFrom(ctx context.Context, dir string) error {
	root, _ := projectRoot()
	branch := CurrentBranch(root)
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
