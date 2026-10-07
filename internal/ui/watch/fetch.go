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
	allEventsByProject := make([][]eventlog.Event, 0, n)
	allCommandsByProject := make([][]watchd.CommandState, 0, n)
	var allSidecars []sidecarInfo

	for i, p := range snap.Projects {
		projects = append(projects, ProjectEntry{ProjectRoot: p.Root})
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
				lastResult:   lastRunResult(p.Events, sc.ID),
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
		branches: branches,
		headRefs: headRefs,
		commands: allCommandsByProject,
		authErr:  snap.AuthError,

		sessions:      collectSessions(snap.Projects),
		reviewAuthErr: snap.ReviewAuthError,
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

// lastRunResult is the level of the most recent validate or review run to
// finish on sidecarID, levelDone or levelError, or "" when none has in the
// retained events. The sidecar's last event will not do: a sync finishes
// "done" too, so a failed run followed by a sync would read as a pass.
func lastRunResult(events []eventlog.Event, sidecarID string) string {
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.SidecarID != sidecarID {
			continue
		}
		if e.Op != eventlog.OpValidate && e.Op != eventlog.OpReview {
			continue
		}
		if _, _, ok := e.Outcome(); !ok {
			continue
		}
		if e.Level == levelDone || e.Level == levelError {
			return e.Level
		}
	}
	return ""
}
