package watch

import (
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// loadFromDaemon is the default Model.loadFn.
func loadFromDaemon(m Model) tea.Msg {
	return loadWithRelaunch(m, fetchFromDaemon, watchd.EnsureLaunched)
}

// loadWithRelaunch fetches a snapshot, relaunching the daemon and retrying once
// if it has gone away.
//
// EnsureRunning at startup is not enough on its own: the daemon can exit while
// the dashboard is open — it crashes, someone kills it, or a chunk from another
// build replaces it (see watchd.BuildID) — and nothing here would ever start
// another. The dashboard would then hold whatever it last saw for as long as it
// stayed open, which reads as a quiet dashboard rather than a broken one.
//
// relaunch is watchd.EnsureLaunched rather than EnsureRunning on purpose: a
// failed poll should start a daemon if none is answering, not replace one that
// is. fetch and relaunch are parameters so this can be tested without a daemon.
func loadWithRelaunch(m Model, fetch func(Model) (dataMsg, error), relaunch func([]string) error) tea.Msg {
	msg, err := fetch(m)
	if err == nil {
		return msg
	}
	// Without the daemon's argv there is nothing to relaunch; report the failure.
	if len(m.daemonArgs) == 0 {
		return errMsg{err}
	}
	if relaunchErr := relaunch(m.daemonArgs); relaunchErr != nil {
		// Report the fetch failure rather than the relaunch failure: the first is
		// what the reader is looking at, and the second is usually a restatement.
		return errMsg{err}
	}
	msg, err = fetch(m)
	if err != nil {
		return errMsg{err}
	}
	return msg
}
