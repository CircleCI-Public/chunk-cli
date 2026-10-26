package watch

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// truncate tests

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"short", 10, "short"},
		{"exactly-10", 10, "exactly-10"},
		{"this-is-too-long", 10, "this-is-t…"},
		{"ab", 1, "a"},
		{"", 5, ""},
	}
	for _, tt := range tests {
		if got := truncate(tt.in, tt.n); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

// padLine tests

func TestPadLine_padsShortString(t *testing.T) {
	got := padLine("hi", 5)
	// lipgloss.Width("hi") == 2; expect 3 spaces appended.
	if got != "hi   " {
		t.Errorf("want %q, got %q", "hi   ", got)
	}
}

func TestPadLine_alreadyAtWidth(t *testing.T) {
	got := padLine("hello", 5)
	if got != "hello" {
		t.Errorf("want %q, got %q", "hello", got)
	}
}

// ago tests

func TestAgo(t *testing.T) {
	now := time.Now()

	// Minutes bucket: clearly > 1 min, < 1 hour.
	if got := ago(now.Add(-5*time.Minute - 30*time.Second)); got != "5m ago" {
		t.Errorf("want '5m ago', got %q", got)
	}

	// Hours bucket.
	if got := ago(now.Add(-3*time.Hour - 30*time.Minute)); got != "3h ago" {
		t.Errorf("want '3h ago', got %q", got)
	}

	// Seconds bucket: verify format without asserting exact count.
	if got := ago(now.Add(-30 * time.Second)); !strings.HasSuffix(got, "s ago") {
		t.Errorf("seconds bucket: want 'Xs ago', got %q", got)
	}
}

// The daemon-unavailable line used to be appended past the last row of the
// terminal, so it was clipped at every height and a dead daemon looked like a
// dashboard that had simply gone quiet.
func TestRender_daemonErrorIsVisibleAndStillFitsTheTerminal(t *testing.T) {
	for _, height := range []int{12, 24, 40, 60} {
		m := sessModel(factoryRun("abc", chunkd.SessionRunning, time.Now()))
		m.width = 100
		m.height = height
		m = m.reselectRun()
		m.daemonErr = errors.New("connect to chunk daemon: no such file")

		out := m.render()

		assert.Assert(t, strings.Contains(out, "daemon unavailable: connect to chunk daemon"),
			"height %d: message missing:\n%s", height, out)
		assert.Assert(t, strings.Count(out, "\n") <= height,
			"height %d: rendered %d lines, which would clip the message", height, strings.Count(out, "\n"))
	}
}

func TestRender_withoutADaemonErrorTheLayoutIsUnchanged(t *testing.T) {
	m := sessModel(factoryRun("abc", chunkd.SessionRunning, time.Now()))
	m.width = 100
	m.height = 24
	m = m.reselectRun()

	assert.Equal(t, strings.Count(m.render(), "\n"), 24)
}

// With nothing to show the dashboard still has to say what it is for, rather
// than leaving two empty panes that read as a broken daemon.
func TestRender_noRunsSaysSoAndKeepsItsLayout(t *testing.T) {
	m := sessModel()
	m.width, m.height = 100, 24

	out := m.render()

	assert.Equal(t, strings.Count(out, "\n"), 24)
	assert.Assert(t, strings.Contains(out, "█"), "the mascot is drawn:\n"+out)
	assert.Assert(t, !strings.Contains(out, "no runs yet"), "no run list beside the welcome:\n"+out)
	assert.Assert(t, strings.Contains(out, "0 runs"), out)
}

// A run just sent to the factory is not listed until a poll finds it; the
// welcome must not flash up in that gap.
func TestRender_noWelcomeWhileASentRunIsPending(t *testing.T) {
	m := sessModel()
	m.width, m.height = 100, 24
	m.pendingRun = "run-1"

	out := m.render()

	assert.Assert(t, !strings.Contains(out, "█"), "no mascot while a run is pending:\n"+out)
	assert.Assert(t, strings.Contains(out, "Sent to the factory"), out)
	assert.Equal(t, strings.Count(out, "\n"), 24)
}

func TestRenderFooter_updateNoticeNeverWidensFooter(t *testing.T) {
	for _, width := range []int{40, 80, 100, 120, 200} {
		for _, focus := range []pane{paneLeft, paneRight} {
			m := New(nil, false)
			m.width = width
			m.focusedPane = focus

			// The key bar alone can already exceed a narrow terminal;
			// only the notice's contribution is under test here.
			limit := max(width, footerWidth(m.renderFooter(m.styles())))

			m.updateAvailable = "v1.2.3"
			m.upgradeCmd = "chunk upgrade"
			if got := footerWidth(m.renderFooter(m.styles())); got > limit {
				t.Errorf("width %d, pane %v: update notice widened footer to %d (limit %d)", width, focus, got, limit)
			}
		}
	}
}

// footerWidth returns the width of the widest line in a rendered footer.
func footerWidth(footer string) int {
	var widest int
	for _, line := range strings.Split(strings.TrimRight(footer, "\n"), "\n") {
		widest = max(widest, lipgloss.Width(line))
	}
	return widest
}

func TestRenderFooter_updateNoticeShownWhenItFits(t *testing.T) {
	m := New(nil, false)
	m.width = 200
	m.updateAvailable = "v1.2.3"
	m.upgradeCmd = "chunk upgrade"

	footer := m.renderFooter(m.styles())
	if !strings.Contains(footer, "v1.2.3") || !strings.Contains(footer, "chunk upgrade") {
		t.Errorf("expected update notice in footer, got %q", footer)
	}
}

// The daemon reports a project's sidecars, events and buffered commands
// whatever the dashboard does with them. This one lists runs, so the snapshot's
// infrastructure is dropped at the door rather than kept for a pane that no
// longer exists.
func TestConvertSnapshot_keepsRunsAndDropsInfrastructure(t *testing.T) {
	now := time.Now()
	snap := chunkd.Snapshot{
		AuthError:       "no token",
		ReviewAuthError: "no claude",
		Projects: []chunkd.ProjectSnapshot{{
			Root:     "/tmp/repo",
			Branch:   "main",
			HeadRef:  "0123456789abcdef",
			RepoName: "repo",
			Sidecars: []chunkd.SidecarState{{ID: "id1", Name: "sc-1", LastActivity: now}},
			Commands: []chunkd.CommandState{{CommandID: "c1", SidecarID: "id1", Running: true}},
			Sessions: []chunkd.Session{{ID: "run-1", State: chunkd.SessionRunning, StartedAt: now}},
		}},
	}

	msg := convertSnapshot(snap)

	assert.DeepEqual(t, msg.projects, []ProjectEntry{{ProjectRoot: "/tmp/repo"}})
	assert.DeepEqual(t, msg.branches, []string{"main"})
	assert.DeepEqual(t, msg.headRefs, []string{"0123456789abcdef"})
	assert.Equal(t, msg.authErr, "no token")
	assert.Equal(t, msg.reviewAuthErr, "no claude")
	assert.Equal(t, len(msg.sessions), 1)
	assert.Equal(t, msg.sessions[0].s.ID, "run-1")
	assert.Equal(t, msg.sessions[0].label, "repo")
}

// A key the footer does not offer does nothing: with no run to show, the right
// pane has nothing to select, open or fold.
func TestDisabledKeysDoNothing(t *testing.T) {
	m := sessModel()
	m.width, m.height = 100, 24
	for _, code := range []rune{tea.KeyEnter, tea.KeySpace, tea.KeyLeft, tea.KeyRight, 'd'} {
		next, cmd := m.Update(tea.KeyPressMsg{Code: code})
		assert.Assert(t, cmd == nil, "%q in the left pane", code)
		assert.Equal(t, next.(Model).focusedPane, paneLeft, "%q moved the focus", code)
		assert.Equal(t, len(next.(Model).detailsOpen), 0, "%q opened the details fold", code)
	}
}

// The dashboard's two panes are drawn as separate boxes side by side, not one
// shared frame with a divider.
func TestPanesAreSeparateBoxes(t *testing.T) {
	m := sessModel(factoryRun("abc", chunkd.SessionRunning, time.Now()))
	m.width, m.height = 100, 30
	m = m.reselectRun()

	lines := strings.Split(m.render(), "\n")
	top := stripANSI(lines[1])
	if !strings.HasPrefix(top, "╭─ runs") {
		t.Errorf("top line does not start with the left box's edge: %q", top)
	}
	if !strings.Contains(top, "╮╭─ run") {
		t.Errorf("top line does not join two boxes: %q", top)
	}
	var bottom string
	for _, l := range lines {
		if s := stripANSI(l); strings.Contains(s, "╯╰") {
			bottom = s
			break
		}
	}
	if bottom == "" {
		t.Errorf("no box line joins two boxes with ╯╰ in:\n%s", strings.Join(lines, "\n"))
	}
	for _, l := range []string{top, bottom} {
		if strings.ContainsAny(l, "┬┴") {
			t.Errorf("line still has a shared-frame junction: %q", l)
		}
	}

	// Each box leaves a blank row under its top edge and above its bottom
	// one, and a cell inside each side border.
	blankRow := "│" + strings.Repeat(" ", leftBoxWidth-2) + "││" + strings.Repeat(" ", m.width-leftBoxWidth-2) + "│"
	assert.Check(t, cmp.Equal(stripANSI(lines[2]), blankRow), "no blank row under the top edges")
	bottomAt := -1
	for i, l := range lines {
		if strings.Contains(stripANSI(l), "╯╰") {
			bottomAt = i
			break
		}
	}
	assert.Assert(t, bottomAt > 2)
	assert.Check(t, cmp.Equal(stripANSI(lines[bottomAt-1]), blankRow), "no blank row above the bottom edges")
	first := []rune(stripANSI(lines[3]))
	assert.Check(t, cmp.Equal(string(first[:3]), "│ ⌕"), "the runs pane's search row is not one cell in: %q", string(first))
	assert.Check(t, cmp.Equal(string(first[leftBoxWidth:leftBoxWidth+2]), "│ "), "the run pane's first row is not one cell in: %q", string(first))
	// The run pane's first row is the status line, which has its own indent;
	// it must start right after the one cell of padding.
	status := stripANSI(m.runStatusLine(m.styles(), m.selectedRun().s))
	assert.Check(t, strings.HasPrefix(string(first[leftBoxWidth+2:]), status),
		"the run pane's status row is not one cell in: %q", string(first))
}

// The focused pane's box is drawn in the brand accent, and the other pane's
// in the quiet grey.
func TestFocusedBoxIsTheAccent(t *testing.T) {
	m := sessModel(factoryRun("abc", chunkd.SessionRunning, time.Now()))
	m.width, m.height = 100, 30
	m = m.reselectRun()
	st := newWatchStyles(true)

	m.focusedPane = paneLeft
	top := strings.Split(m.render(), "\n")[1]
	if !strings.HasPrefix(top, st.Brand.Render("╭")) {
		t.Errorf("left-focused top line does not start with the accent ╭: %q", top)
	}
	if !strings.Contains(top, st.VDim.Render("╭")) {
		t.Errorf("left-focused top line does not contain the vdim ╭ for the right box: %q", top)
	}

	m.focusedPane = paneRight
	top = strings.Split(m.render(), "\n")[1]
	if !strings.HasPrefix(top, st.VDim.Render("╭")) {
		t.Errorf("right-focused top line does not start with the vdim ╭: %q", top)
	}
	if !strings.Contains(top, st.Brand.Render("╭")) {
		t.Errorf("right-focused top line does not contain the accent ╭ for the right box: %q", top)
	}
}
