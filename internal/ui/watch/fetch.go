package watch

import (
	"path/filepath"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// fetchFromDaemon fetches a snapshot from the watch daemon and converts it to
// a dataMsg for the model.
func fetchFromDaemon(m Model) (dataMsg, error) {
	var roots []string
	if !m.watchAll {
		roots = make([]string, len(m.projects))
		for i, p := range m.projects {
			roots[i] = p.ProjectRoot
		}
	}
	snap, err := watchd.FetchSnapshot(roots)
	if err != nil {
		return dataMsg{}, err
	}
	return convertSnapshot(snap, m), nil
}

// convertSnapshot maps a watchd.Snapshot to the dataMsg the model expects.
// The daemon has already annotated sidecars with activity; this function
// handles TUI-side concerns: synthesising local-runner entries, ordering,
// branch merging, and filtering.
func convertSnapshot(snap watchd.Snapshot, m Model) dataMsg {
	n := len(snap.Projects)
	projects := make([]ProjectEntry, 0, n)
	branches := make([]string, 0, n)
	headRefs := make([]string, 0, n)
	offsets := make([]int64, n) // daemon owns offsets; TUI keeps zeros
	allEventsByProject := make([][]eventlog.Event, 0, n)
	var allSidecars []sidecarInfo

	for i, p := range snap.Projects {
		// Preserve the existing ProjectEntry when available so the Log handle
		// stays open (used by tests and future local fallback paths).
		entry := ProjectEntry{ProjectRoot: p.Root}
		for _, e := range m.projects {
			if e.ProjectRoot == p.Root {
				entry = e
				break
			}
		}
		projects = append(projects, entry)
		branches = append(branches, p.Branch)
		headRefs = append(headRefs, p.HeadRef)
		allEventsByProject = append(allEventsByProject, p.Events)

		for _, sc := range p.Sidecars {
			allSidecars = append(allSidecars, sidecarInfo{
				id:           sc.ID,
				sidecarIDs:   []string{sc.ID},
				name:         sc.Name,
				sessionID:    sc.SessionID,
				projectName:  sc.ProjectName,
				repoName:     sc.RepoName,
				projectPath:  p.Root,
				branch:       p.Branch,
				projectIdx:   i,
				snapshotName: sc.SnapshotName,
				fileMtime:    sc.FileMtime,
				lastActivity: sc.LastActivity,
				lastOp:       sc.LastOp,
				lastLevel:    sc.LastLevel,
				running:      sc.Running,
			})
		}

		// Synthesise a local-runner entry; its activity comes from events
		// where SidecarID is "" (local validate runs, not sidecar ones).
		local := sidecarInfo{
			id:          "",
			sidecarIDs:  []string{""},
			name:        localRunnerName,
			projectName: filepath.Base(p.Root),
			repoName:    p.RepoName,
			projectPath: p.Root,
			branch:      p.Branch,
			projectIdx:  i,
		}
		for j := len(p.Events) - 1; j >= 0; j-- {
			e := p.Events[j]
			if e.SidecarID != "" {
				continue
			}
			local.lastActivity = e.Ts
			local.lastOp = e.Op
			local.lastLevel = e.Level
			if e.Level != levelDone && e.Level != levelError && time.Since(e.Ts) < runningTimeout {
				local.running = true
			}
			break
		}
		allSidecars = append(allSidecars, local)
	}

	sortByActivity(allSidecars, m.ownSession)
	allSidecars = mergeBranches(allSidecars)
	allSidecars = filterSidecars(allSidecars, m.sidecarCapacity())

	return dataMsg{
		projects: projects,
		sidecars: allSidecars,
		events:   allEventsByProject,
		offsets:  offsets,
		branches: branches,
		headRefs: headRefs,
	}
}
