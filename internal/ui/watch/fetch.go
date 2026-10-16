package watch

import (
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// loadFromDaemon is the default Model.loadFn.
func loadFromDaemon(m Model) tea.Msg {
	msg, err := fetchFromDaemon(m)
	if err != nil {
		return errMsg{err}
	}
	return msg
}

// fetchFromDaemon fetches a snapshot from the chunk daemon and converts it to
// a dataMsg for the model.
func fetchFromDaemon(m Model) (dataMsg, error) {
	var roots []string
	if !m.watchAll {
		roots = make([]string, len(m.projects))
		for i, p := range m.projects {
			roots[i] = p.ProjectRoot
		}
	}
	fetch := chunkd.FetchSnapshot
	if m.relaunch {
		fetch = chunkd.FetchSnapshotRelaunching
	}
	snap, err := fetch(roots)
	if err != nil {
		return dataMsg{}, err
	}
	return convertSnapshot(snap), nil
}

// convertSnapshot maps a chunkd.Snapshot to the dataMsg the model expects.
// The dashboard is about factory runs, so the sidecars, events and buffered
// commands a snapshot also carries are left where they are: the infrastructure
// a run happens to use is not what anyone opens this to watch.
func convertSnapshot(snap chunkd.Snapshot) dataMsg {
	n := len(snap.Projects)
	projects := make([]ProjectEntry, 0, n)
	branches := make([]string, 0, n)
	headRefs := make([]string, 0, n)

	for _, p := range snap.Projects {
		projects = append(projects, ProjectEntry{ProjectRoot: p.Root})
		branches = append(branches, p.Branch)
		headRefs = append(headRefs, p.HeadRef)
	}

	return dataMsg{
		projects: projects,
		branches: branches,
		headRefs: headRefs,
		authErr:  snap.AuthError,

		sessions:      collectSessions(snap.Projects),
		reviewAuthErr: snap.ReviewAuthError,
	}
}
