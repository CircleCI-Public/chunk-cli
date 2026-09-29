package watch

import (
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
// The daemon has already annotated sidecars with activity and dropped the ones
// the API no longer lists; this function handles TUI-side concerns: ordering
// and filtering. Only sidecars get rows — a validate run that happened locally
// has no sidecar to show.
func convertSnapshot(snap watchd.Snapshot, m Model) dataMsg {
	n := len(snap.Projects)
	projects := make([]ProjectEntry, 0, n)
	branches := make([]string, 0, n)
	headRefs := make([]string, 0, n)
	offsets := make([]int64, n) // daemon owns offsets; TUI keeps zeros
	allEventsByProject := make([][]eventlog.Event, 0, n)
	allCommandsByProject := make([][]watchd.CommandState, 0, n)
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
		allCommandsByProject = append(allCommandsByProject, p.Commands)

		for _, sc := range p.Sidecars {
			allSidecars = append(allSidecars, sidecarInfo{
				id:           sc.ID,
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
				running:      sc.Running || commandRunning(p.Commands, sc.ID),
				verified:     sc.Verified,
				resources:    sc.Resources,
			})
		}
	}

	sortByActivity(allSidecars, m.ownSession)
	allSidecars = filterSidecars(allSidecars)

	return dataMsg{
		projects: projects,
		sidecars: allSidecars,
		events:   allEventsByProject,
		offsets:  offsets,
		branches: branches,
		headRefs: headRefs,
		commands: allCommandsByProject,
		authErr:  snap.AuthError,
	}
}

// commandRunning reports whether the daemon is still streaming a command on
// sidecarID. Unlike the event log, which can only say a run went quiet without
// finishing, this is known to be in flight.
func commandRunning(commands []watchd.CommandState, sidecarID string) bool {
	for _, c := range commands {
		if c.SidecarID == sidecarID && c.Running {
			return true
		}
	}
	return false
}
