package watchd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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

// loadSidecars reads all sidecar*.json files from dataDir and returns one
// SidecarState per unique sidecar ID. When multiple files share the same ID
// the entry with the newest mtime wins, so a stale state file never masks a
// more recent sync.
func loadSidecars(dataDir, root, snapshotName, head string) []SidecarState {
	matches, _ := filepath.Glob(filepath.Join(dataDir, "sidecar*.json"))
	projectName := filepath.Base(root)
	repoName := projectRepoName(root)
	idx := map[string]int{}
	var result []SidecarState
	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var as sidecar.ActiveSidecar
		if json.Unmarshal(data, &as) != nil || as.SidecarID == "" {
			continue
		}
		var mtime time.Time
		if fi, err := os.Stat(path); err == nil {
			mtime = fi.ModTime()
		}
		at, dup := idx[as.SidecarID]
		if dup && !mtime.After(result[at].FileMtime) {
			continue
		}
		ss := SidecarState{
			ID:            as.SidecarID,
			Name:          as.Name,
			SessionID:     as.SessionID,
			ProjectName:   projectName,
			RepoName:      repoName,
			SnapshotName:  snapshotName,
			FileMtime:     mtime,
			LastSyncedRef: as.LastSyncedRef,
			Workspace:     as.Workspace,
			InSync:        head != "" && as.LastSyncedRef != "" && head == as.LastSyncedRef,
		}
		if dup {
			result[at] = ss
			continue
		}
		idx[as.SidecarID] = len(result)
		result = append(result, ss)
	}
	return result
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
