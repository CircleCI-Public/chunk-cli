package watch

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// finishedRun is a run that passed with its work committed on its branch.
func finishedRun(id string) sessionInfo {
	ended := time.Now()
	r := factoryRun(id, chunkd.SessionDone, ended.Add(-3*time.Minute))
	r.s.EndedAt = &ended
	r.s.ProjectRoot = "/src/chunk-cli"
	r.s.Factory.Result = chunkd.ResultPassed
	r.s.Factory.Committed = true
	r.s.Factory.Baseline = "0123456789abcdef"
	r.s.Factory.Branch = "chunk/factory/add-a-flag/" + id
	r.s.Factory.Stat = "1 file changed, 2 insertions(+)"
	return r
}

const sampleChange = `diff --git a/README.md b/README.md
index 1234567..89abcde 100644
--- a/README.md
+++ b/README.md
@@ -1,3 +1,5 @@
 # chunk

+You can start a factory run from chunk watch by pressing n.
+
 chunk is a CLI for inner loop validation.
`

// stubRunChange answers every read of a run's change with diff, recording
// what was asked for.
func stubRunChange(t *testing.T, diff string, err error) *[]string {
	t.Helper()
	orig := readRunChange
	var asked []string
	readRunChange = func(root, baseline, branch string) (string, error) {
		asked = append(asked, root+" "+baseline+" "+branch)
		return diff, err
	}
	t.Cleanup(func() { readRunChange = orig })
	return &asked
}

// pollAndRead delivers a poll, then runs what it returned and delivers a
// change it read, as the program would. What it returns includes the next
// poll's timer, which would wait out the poll interval, so each command runs
// at once and only a change that was read is kept.
func pollAndRead(t *testing.T, m Model, runs ...sessionInfo) Model {
	t.Helper()
	next, cmd := m.Update(dataMsg{sessions: runs})
	m = next.(Model)
	read := make(chan runChangeMsg, 8)
	var run func(tea.Cmd)
	run = func(c tea.Cmd) {
		if c == nil {
			return
		}
		go func() {
			switch msg := c().(type) {
			case tea.BatchMsg:
				for _, inner := range msg {
					run(inner)
				}
			case runChangeMsg:
				read <- msg
			}
		}()
	}
	run(cmd)
	select {
	case msg := <-read:
		next, _ = m.Update(msg)
		m = next.(Model)
	case <-time.After(300 * time.Millisecond):
	}
	return m
}

// A finished run's committed change is shown under its outcome, read from its
// branch, with no key to press.
func TestAFinishedRunShowsItsChange(t *testing.T) {
	asked := stubRunChange(t, sampleChange, nil)
	run := finishedRun("run-1")
	m := sessModel()

	m = pollAndRead(t, m, run)

	assert.DeepEqual(t, *asked, []string{"/src/chunk-cli 0123456789abcdef chunk/factory/add-a-flag/run-1"})
	out := view(m)
	assert.Assert(t, strings.Contains(out, "change  1 file changed, 2 insertions(+)"), out)
	assert.Assert(t, strings.Contains(out, "+You can start a factory run from chunk watch by pressing n."), out)
	assert.Assert(t, strings.Contains(out, "README.md"), "the file is named: %s", out)
	assert.Assert(t, !strings.Contains(out, "index 1234567"), "git's headers are cut: %s", out)

	// It is read once, not on every poll.
	m = pollAndRead(t, m, run)
	assert.Equal(t, len(*asked), 1)
	assert.Assert(t, strings.Contains(view(m), "change  1 file changed"), "it is still shown")
}

// A change longer than the room under the outcome ends in how to see the rest,
// and the pane stays within the screen.
func TestALongChangeSaysHowToSeeTheRest(t *testing.T) {
	var diff strings.Builder
	diff.WriteString("+++ b/a.go\n@@ -1 +1,80 @@\n")
	for i := range 80 {
		fmt.Fprintf(&diff, "+line %02d\n", i)
	}
	stubRunChange(t, diff.String(), nil)
	m := sessModel()

	m = pollAndRead(t, m, finishedRun("run-1"))

	out := view(m)
	assert.Assert(t, strings.Contains(out, "more lines · git diff 0123456 chunk/factory/add-a-flag/run-1"), out)
	assert.Equal(t, strings.Count(m.render(), "\n"), m.height, out)
}

// A run still going, one with nothing committed, or one whose change cannot
// be read shows no change.
func TestNoChangeIsShownWithoutOneToShow(t *testing.T) {
	live := finishedRun("live")
	live.s.State, live.s.EndedAt = chunkd.SessionRunning, nil
	uncommitted := finishedRun("uncommitted")
	uncommitted.s.Factory.Committed = false

	for _, run := range []sessionInfo{live, uncommitted} {
		asked := stubRunChange(t, sampleChange, nil)
		m := pollAndRead(t, sessModel(), run)
		assert.Equal(t, len(*asked), 0, run.s.ID)
		assert.Assert(t, !strings.Contains(view(m), "change  "), run.s.ID)
	}

	stubRunChange(t, "", errors.New("unknown revision"))
	m := pollAndRead(t, sessModel(), finishedRun("gone"))
	assert.Assert(t, !strings.Contains(view(m), "change  "), view(m))
}
