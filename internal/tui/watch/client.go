package watch

import (
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// loadFromDaemon is the default Model.loadFn. It fetches a snapshot from the
// watch daemon and converts it to a dataMsg. If the daemon is unreachable it
// is relaunched; the next tick will reconnect.
func loadFromDaemon(m Model) tea.Msg {
	msg, err := fetchFromDaemon(m)
	if err != nil {
		_ = watchd.EnsureRunning([]string{"watch", "_daemon"})
		return dataMsg{}
	}
	return msg
}
