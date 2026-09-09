package watch

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// TestDashboardNeverExceedsItsWidth pins the other half of the fixed-size layout.
// A line wider than the terminal wraps, which costs a row the height budget never
// allowed for — so an over-wide line breaks the layout exactly as an extra line
// does. The output affordance and its footer hint are the widest things the right
// pane grew, so narrow terminals are where they show up first.
func TestDashboardNeverExceedsItsWidth(t *testing.T) {
	cmds := []watchd.CommandState{{
		CommandID:   "cmd-1",
		SidecarID:   "sc-1",
		SubmittedAt: time.Now().Add(-time.Minute).Add(time.Second),
	}}
	for _, hasOutput := range []bool{false, true} {
		for _, width := range []int{40, 50, 60, 70, 80, 120} {
			for _, focus := range []pane{paneLeft, paneRight} {
				m := modelWithInvocation(t, nil)
				if hasOutput {
					m = modelWithInvocation(t, cmds)
				}
				m.width = width
				m.focusedPane = focus
				m.authErr = "not authenticated to CircleCI — command output unavailable"
				for i, line := range strings.Split(m.render(), "\n") {
					if got := lipgloss.Width(line); got > width {
						t.Errorf("hasOutput=%v width=%d focus=%d: line %d is %d columns",
							hasOutput, width, focus, i, got)
					}
				}
			}
		}
	}
}
