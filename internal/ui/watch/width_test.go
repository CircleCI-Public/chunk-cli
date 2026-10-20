package watch

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// TestDashboardNeverExceedsItsWidth pins the other half of the fixed-size layout.
// A line wider than the terminal wraps, which costs a row the height budget never
// allowed for — so an over-wide line breaks the layout exactly as an extra line
// does. The output affordance and its footer hint are the widest things the right
// pane grew, so narrow terminals are where they show up first.
func TestDashboardNeverExceedsItsWidth(t *testing.T) {
	cmds := []chunkd.CommandState{{
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

// The task row wraps a long prompt to the pane without breaking a line
// mid-word — the one exception is a word longer than the pane itself, which
// has nowhere else to go.
func TestTaskLinesWrapAtEightyColumnsWithoutBreakingWords(t *testing.T) {
	const width = 80
	longWord := strings.Repeat("x", width+20)
	prompt := "Please " + strings.Repeat("refactor the helper so it stops throwing on empty input. ", 8) + longWord

	lines := taskLines(prompt, width, true)

	var gotWords []string
	for _, l := range lines {
		gotWords = append(gotWords, strings.Fields(l)...)
	}
	// Every word survives, in order and whole: wrapping only moves line
	// breaks, it never splits or drops a word.
	assert.DeepEqual(t, gotWords, strings.Fields(prompt))

	for _, l := range lines {
		if w := lipgloss.Width(l); w > width && l != longWord {
			t.Errorf("line %q is %d columns, wider than the pane (%d)", l, w, width)
		}
	}
}
