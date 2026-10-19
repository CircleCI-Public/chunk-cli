package watch

import (
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// TestDashboardNeverExceedsItsWidth pins the other half of the fixed-size layout.
// A line wider than the terminal wraps, which costs a row the height budget never
// allowed for — so an over-wide line breaks the layout exactly as an extra line
// does. The run pane holds the widest things the dashboard draws — prompts,
// worktree paths, a failed check's output — so narrow terminals are where they
// show up first.
func TestDashboardNeverExceedsItsWidth(t *testing.T) {
	run := widestRun()

	for _, details := range []bool{false, true} {
		for _, width := range []int{40, 50, 60, 70, 80, 120} {
			for _, focus := range []pane{paneLeft, paneRight} {
				m := sessModel(run)
				m = m.reselectRun()
				m.width = width
				m.focusedPane = focus
				m.runDetails = details
				m.authErr = "not authenticated to CircleCI — command output unavailable"
				for i, line := range strings.Split(m.render(), "\n") {
					if got := lipgloss.Width(line); got > width {
						t.Errorf("details=%v width=%d focus=%d: line %d is %d columns",
							details, width, focus, i, got)
					}
				}
			}
		}
	}
}

// widestRun is a run holding the widest things the run pane draws: a
// prompt, a check's name and output, a review's tally.
func widestRun() sessionInfo {
	now := time.Now()
	run := factoryRun("abc", chunkd.SessionRunning, now)
	run.s.Rounds = []chunkd.Round{{
		Number: 1, State: chunkd.RoundChecking, StartedAt: now.Add(-time.Minute),
		Implement: &chunkd.RoundImplement{State: chunkd.ImplementApplied, DurationMS: 42000, CostUSD: 1.5, Stat: "3 files changed, 120 insertions(+)"},
		Checks: []chunkd.RoundCheck{
			{Name: "a check with a long name", Status: chunkd.CheckFailed, DurationMS: 9000,
				Output: "FAIL\tgithub.com/CircleCI-Public/chunk-cli/internal/some/deeply/nested/package\t9.001s\n"},
		},
		Reviews: []chunkd.ReviewPrompt{
			{Name: "naming-and-structure", State: chunkd.PromptDone, Status: chunkd.CheckFailed, Findings: 4, Worth: 2, DurationMS: 31000},
		},
	}}
	return run
}

// No line of the run pane is wider than the pane, so none is cut at its edge:
// each wraps to the pane instead.
func TestRunPaneWrapsEveryLineToThePane(t *testing.T) {
	stubRunChange(t, "+++ b/README.md\n@@ -1 +1 @@\n+`chunk factory` has an implementer agent change your code on a sidecar, then reviews the result with each prompt in `.chunk/reviews/`\n", nil)
	done := finishedRun("run-1")
	done.s.Rounds = widestRun().s.Rounds

	for _, run := range []sessionInfo{widestRun(), done} {
		for _, details := range []bool{false, true} {
			for _, width := range []int{40, 50, 60, 70, 80, 120} {
				m := pollAndRead(t, sessModel(), run)
				m.width = width
				m.runDetails = details
				m = m.reselectRun()
				for i, line := range m.renderRunPane(m.styles(), *m.selectedRun(), 60) {
					if got := lipgloss.Width(line); got > width-leftPaneWidth-5 {
						t.Errorf("%s details=%v width=%d: line %d is %d columns: %q",
							run.s.ID, details, width, i, got, stripANSI(line))
					}
				}
			}
		}
	}
}

// The empty dashboard draws the logo, which is a fixed 28 columns wide: it has
// to be padded into the pane, not past it.
func TestEmptyDashboardNeverExceedsItsWidth(t *testing.T) {
	for _, width := range []int{40, 50, 60, 70, 80, 120} {
		m := sessModel()
		m.width = width
		for i, line := range strings.Split(m.render(), "\n") {
			if got := lipgloss.Width(line); got > width {
				t.Errorf("width=%d: line %d is %d columns", width, i, got)
			}
		}
	}
}
