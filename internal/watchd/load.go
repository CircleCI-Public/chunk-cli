package watchd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

const (
	// PollInterval is how often the daemon refreshes project state from disk.
	PollInterval = 5 * time.Second
	// RecentEvents is the maximum number of events kept per project.
	RecentEvents = 300
	// RunningTimeout is how long after the last non-terminal event a sidecar is
	// considered to still be running.
	RunningTimeout = 5 * time.Minute

	levelDone  = "done"
	levelError = "error"
)

// loadSidecars reads active-sidecar and pool state files and returns one
// SidecarState per unique sidecar ID. Active state is authoritative for
// display metadata; pool state supplements it with managed members that are
// not present there.
func loadSidecars(dataDir, root, snapshotName string) []SidecarState {
	projectName := filepath.Base(root)
	repoName := projectRepoName(root)
	idx := map[string]int{}
	var result []SidecarState
	appendState := func(id, name, sessionID, workspace string, mtime time.Time) {
		if id == "" {
			return
		}
		at, duplicate := idx[id]
		if duplicate && !mtime.After(result[at].FileMtime) {
			return
		}
		state := SidecarState{
			ID:           id,
			Name:         name,
			SessionID:    sessionID,
			ProjectName:  projectName,
			RepoName:     repoName,
			SnapshotName: snapshotName,
			FileMtime:    mtime,
			Workspace:    workspace,
		}
		if duplicate {
			result[at] = state
			return
		}
		idx[id] = len(result)
		result = append(result, state)
	}

	matches, _ := filepath.Glob(filepath.Join(dataDir, "sidecar*.json"))
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var as sidecar.ActiveSidecar
		if json.Unmarshal(data, &as) != nil {
			continue
		}
		var mtime time.Time
		if fi, err := os.Stat(path); err == nil {
			mtime = fi.ModTime()
		}
		for i, id := range as.SidecarIDs {
			appendState(id, sidecarName(as.Name, i, len(as.SidecarIDs)), as.SessionID, as.Workspace, mtime)
		}
	}

	poolMatches, _ := filepath.Glob(filepath.Join(root, ".chunk", "*-pool.json"))
	for _, path := range poolMatches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var pool struct {
			SidecarIDs []string `json:"sidecar_ids"`
			RepoPath   string   `json:"repo_path"`
		}
		if json.Unmarshal(data, &pool) != nil {
			continue
		}
		var mtime time.Time
		if fi, err := os.Stat(path); err == nil {
			mtime = fi.ModTime()
		}
		name := strings.TrimSuffix(filepath.Base(path), "-pool.json")
		for i, id := range pool.SidecarIDs {
			if at, exists := idx[id]; exists {
				if result[at].Workspace == "" {
					result[at].Workspace = pool.RepoPath
				}
				continue
			}
			appendState(id, sidecarName(name, i, len(pool.SidecarIDs)), "", pool.RepoPath, mtime)
		}
	}
	return result
}

func sidecarName(name string, index, total int) string {
	// Preserve unnamed legacy state and avoid adding a redundant suffix to a
	// pool of one; only named multi-member pools need distinct display names.
	if name == "" || total == 1 {
		return name
	}
	return name + "-" + strconv.Itoa(index+1)
}

// loadSnapshotName returns the Name field from any snapshot*.json in dataDir,
// or "" if none is found.
func loadSnapshotName(dataDir string) string {
	matches, _ := filepath.Glob(filepath.Join(dataDir, "snapshot*.json"))
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var snap struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(data, &snap) == nil && snap.Name != "" {
			return snap.Name
		}
	}
	return ""
}

// headRef returns the full HEAD SHA for the git repo at dir.
func headRef(dir string) string {
	if dir == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sha, _ := gitutil.HeadRefCtx(ctx, dir)
	return sha
}

// projectRepoName returns the basename of the main git worktree for root. For
// a linked worktree (.git is a file) it traces the gitdir pointer back to the
// main checkout so all worktrees of the same repo share one group header.
func projectRepoName(root string) string {
	gitPath := filepath.Join(root, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil || fi.IsDir() {
		return filepath.Base(root)
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return filepath.Base(root)
	}
	line := strings.TrimSpace(string(data))
	const pfx = "gitdir: "
	if !strings.HasPrefix(line, pfx) {
		return filepath.Base(root)
	}
	// Navigate up 3 levels: <name> → worktrees → .git → main root
	mainRoot := filepath.Dir(filepath.Dir(filepath.Dir(strings.TrimPrefix(line, pfx))))
	if mainRoot == "" || mainRoot == "." {
		return filepath.Base(root)
	}
	return filepath.Base(mainRoot)
}

// annotateActivity fills LastActivity, LastOp, LastLevel, and Running on each
// sidecar from the most recent matching event.
func annotateActivity(sidecars []SidecarState, events []eventlog.Event) {
	for i := range sidecars {
		sc := &sidecars[i]
		for j := len(events) - 1; j >= 0; j-- {
			e := events[j]
			if e.SidecarID != sc.ID {
				continue
			}
			sc.LastActivity = e.Ts
			sc.LastOp = e.Op
			sc.LastLevel = e.Level
			if e.Level != levelDone && e.Level != levelError && time.Since(e.Ts) < RunningTimeout {
				sc.Running = true
			}
			break
		}
	}
}

// capEvents appends fresh to prior, keeping at most max entries (newest survive).
func capEvents(prior, fresh []eventlog.Event, limit int) []eventlog.Event {
	merged := make([]eventlog.Event, 0, len(prior)+len(fresh))
	merged = append(merged, prior...)
	merged = append(merged, fresh...)
	if len(merged) > limit {
		merged = merged[len(merged)-limit:]
	}
	return merged
}

// currentBranch returns the current git branch for the repo at root.
func currentBranch(root string) string {
	return sidecar.CurrentBranch(root)
}

// canonicalRoot resolves root to the path the per-project data directory is
// keyed by, matching config.ProjectDataDir: symlinks resolved, falling back to
// a lexical clean when they cannot be. Two callers naming the same project by
// different paths have to agree here or the daemon reports it as unknown.
func canonicalRoot(root string) string {
	clean := filepath.Clean(root)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved
	}
	return clean
}
