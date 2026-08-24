package watch

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"

	tea "charm.land/bubbletea/v2"
)

// Client performs all disk and subprocess I/O for the watch model.
type Client struct{}

// load reads current project state from disk and returns a dataMsg for the model.
func (c *Client) load(m Model) tea.Msg {
	projects := m.projects
	if m.watchAll {
		projects = discoverNewProjects(projects)
	}

	var allSidecars []sidecarInfo

	allEventsByProject := make([][]eventlog.Event, len(projects))
	for i := range allEventsByProject {
		if i < len(m.events) {
			allEventsByProject[i] = m.events[i]
		}
	}

	newOffsets := make([]int64, len(projects))
	copy(newOffsets, m.offsets)
	newBranches := make([]string, len(projects))
	newHeadRefs := make([]string, len(projects))

	for i, p := range projects {
		newBranches[i] = sidecar.CurrentBranch(p.ProjectRoot)
		newHeadRefs[i] = headRef(p.ProjectRoot)
		snapName := loadSnapshotName(p.DataDir)
		repo := projectRepoName(p.ProjectRoot)
		sidecars := loadSidecars(p.DataDir, p.ProjectRoot, snapName, newHeadRefs[i], i, repo, newBranches[i])
		allSidecars = append(allSidecars, sidecars...)

		allSidecars = append(allSidecars, sidecarInfo{
			id:          "",
			sidecarIDs:  []string{""},
			name:        "local",
			projectName: filepath.Base(p.ProjectRoot),
			repoName:    repo,
			branch:      newBranches[i],
			projectIdx:  i,
		})

		if p.Log == nil {
			continue
		}
		var priorOffset int64
		if i < len(m.offsets) {
			priorOffset = m.offsets[i]
		}
		fresh, newOff, _ := p.Log.TailFrom(priorOffset)
		allEventsByProject[i] = capEvents(allEventsByProject[i], fresh)
		newOffsets[i] = newOff
	}

	annotateActivity(allSidecars, allEventsByProject)
	sortByActivity(allSidecars)
	allSidecars = mergeBranches(allSidecars)
	allSidecars = filterSidecars(allSidecars, m.sidecarCapacity())

	return dataMsg{
		projects: projects,
		sidecars: allSidecars,
		events:   allEventsByProject,
		offsets:  newOffsets,
		branches: newBranches,
		headRefs: newHeadRefs,
	}
}

// discoverNewProjects returns known plus any project whose data directory exists
// (per sidecar.AllProjectRoots) but isn't in known yet. Roots that fail to open
// are skipped rather than aborting the whole poll.
func discoverNewProjects(known []ProjectEntry) []ProjectEntry {
	roots, err := sidecar.AllProjectRoots()
	if err != nil {
		return known
	}
	seen := make(map[string]bool, len(known))
	for _, p := range known {
		seen[p.ProjectRoot] = true
	}
	for _, root := range roots {
		if seen[root] {
			continue
		}
		dataDir, err := config.ProjectDataDir(root)
		if err != nil {
			continue
		}
		el, err := eventlog.Open(dataDir)
		if err != nil {
			continue
		}
		known = append(known, ProjectEntry{Log: el, DataDir: dataDir, ProjectRoot: root})
		seen[root] = true
	}
	return known
}

// loadSidecars reads all sidecar*.json files from dataDir, keeping one entry per
// sidecar ID. Only the most recently written file holds the current synced ref,
// so entries are deduplicated by newest mtime rather than glob order.
func loadSidecars(dataDir, projectRoot string, snapshotName string, head string, projectIdx int, repoName, branch string) []sidecarInfo {
	matches, _ := filepath.Glob(filepath.Join(dataDir, "sidecar*.json"))
	projectName := filepath.Base(projectRoot)
	idx := map[string]int{}
	var result []sidecarInfo
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
		if dup && !mtime.After(result[at].fileMtime) {
			continue
		}
		info := sidecarInfo{
			id:            as.SidecarID,
			sidecarIDs:    []string{as.SidecarID},
			name:          as.Name,
			projectName:   projectName,
			repoName:      repoName,
			branch:        branch,
			projectIdx:    projectIdx,
			snapshotName:  snapshotName,
			fileMtime:     mtime,
			lastSyncedRef: as.LastSyncedRef,
			inSync:        head != "" && as.LastSyncedRef != "" && head == as.LastSyncedRef,
		}
		if dup {
			result[at] = info
			continue
		}
		idx[as.SidecarID] = len(result)
		result = append(result, info)
	}
	return result
}

// loadSnapshotName returns the Name field from any snapshot*.json in dataDir,
// or "" if none is found or the name is not set.
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

// projectRepoName returns the basename of the main git worktree for the repo at
// projectRoot. For a linked worktree (where .git is a file, not a directory) it
// traces the gitdir back to the main worktree so all worktrees of the same repo
// share a common group header. Falls back to filepath.Base(projectRoot) on any error.
func projectRepoName(projectRoot string) string {
	gitPath := filepath.Join(projectRoot, ".git")
	fi, err := os.Stat(gitPath)
	if err != nil {
		return filepath.Base(projectRoot)
	}
	if fi.IsDir() {
		return filepath.Base(projectRoot)
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return filepath.Base(projectRoot)
	}
	line := strings.TrimSpace(string(data))
	const pfx = "gitdir: "
	if !strings.HasPrefix(line, pfx) {
		return filepath.Base(projectRoot)
	}
	// Navigate up 3 levels: <name> → worktrees → .git → main root
	mainRoot := filepath.Dir(filepath.Dir(filepath.Dir(strings.TrimPrefix(line, pfx))))
	if mainRoot == "" || mainRoot == "." {
		return filepath.Base(projectRoot)
	}
	return filepath.Base(mainRoot)
}
