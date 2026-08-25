package watch

import (
	tea "charm.land/bubbletea/v2"
)

// loadFromDaemon is the default Model.loadFn. It fetches a snapshot from the
// watch daemon and converts it to a dataMsg. On failure it returns an errMsg
// so prior state is preserved; the daemon is restarted at startup only (see
// watch.go), and the next tick will reconnect once it is up.
func loadFromDaemon(m Model) tea.Msg {
	msg, err := fetchFromDaemon(m)
	if err != nil {
		return errMsg{err}
	}
	return msg
}
