package watch

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// groupEvents tests

func TestGroupEvents_empty(t *testing.T) {
	if groups := groupEvents(nil); len(groups) != 0 {
		t.Errorf("want 0 groups, got %d", len(groups))
	}
}

func TestGroupEvents_singleRunning(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpValidate, Level: "step"},
		{Op: eventlog.OpValidate, Level: "info"},
	}
	groups := groupEvents(events)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Errorf("want 2 events in group, got %d", len(groups[0]))
	}
}

func TestGroupEvents_terminatedByDone(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpSync, Level: "step"},
		{Op: eventlog.OpSync, Level: "done"},
		{Op: eventlog.OpSync, Level: "info"},
	}
	groups := groupEvents(events)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Errorf("first group: want 2 events, got %d", len(groups[0]))
	}
	if len(groups[1]) != 1 {
		t.Errorf("second group: want 1 event, got %d", len(groups[1]))
	}
}

func TestGroupEvents_terminatedByError(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpValidate, Level: "step"},
		{Op: eventlog.OpValidate, Level: "error"},
	}
	groups := groupEvents(events)
	if len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("want 1 group of 2, got %d groups", len(groups))
	}
}

func TestGroupEvents_opChange(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpSync, Level: "info"},
		{Op: eventlog.OpValidate, Level: "info"},
	}
	groups := groupEvents(events)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	if groups[0][0].Op != eventlog.OpSync {
		t.Errorf("group 0 should be sync, got %q", groups[0][0].Op)
	}
	if groups[1][0].Op != eventlog.OpValidate {
		t.Errorf("group 1 should be validate, got %q", groups[1][0].Op)
	}
}

func TestGroupEvents_doneFollowedByNewOp(t *testing.T) {
	// done terminates the group, next event starts a fresh one even with same op.
	events := []eventlog.Event{
		{Op: eventlog.OpSync, Level: "step"},
		{Op: eventlog.OpSync, Level: "done"},
		{Op: eventlog.OpSync, Level: "step"},
		{Op: eventlog.OpSync, Level: "info"},
	}
	groups := groupEvents(events)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	if len(groups[1]) != 2 {
		t.Errorf("second run: want 2 events, got %d", len(groups[1]))
	}
}

// groupByInvocation / isSummaryEvent tests

func TestGroupByInvocation_singleRun(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpSync, Level: "done", Msg: "synced"},
		{Op: eventlog.OpValidate, Level: "done", Msg: "test    1.0s (remote)"},
		{Op: eventlog.OpValidate, Level: "done", Msg: "lint    0.2s (remote)"},
		{Op: eventlog.OpValidate, Level: "done", Msg: "2/2 passed  1.5s"},
	}
	groups := groupByInvocation(events)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	if len(groups[0].events) != 4 {
		t.Errorf("want 4 events in group, got %d", len(groups[0].events))
	}
}

func TestGroupByInvocation_twoRuns(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpSync, Level: "done", Msg: "synced"},
		{Op: eventlog.OpValidate, Level: "done", Msg: "test    1.0s (remote)"},
		{Op: eventlog.OpValidate, Level: "error", Msg: "0/1 passed  1.0s"},
		// second run
		{Op: eventlog.OpSync, Level: "done", Msg: "synced"},
		{Op: eventlog.OpValidate, Level: "done", Msg: "test    0.8s (remote)"},
		{Op: eventlog.OpValidate, Level: "done", Msg: "1/1 passed  0.9s"},
	}
	groups := groupByInvocation(events)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
}

func TestGroupByInvocation_inProgress(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpSync, Level: "done", Msg: "synced"},
		{Op: eventlog.OpValidate, Level: "step", Msg: "$ task test"},
	}
	groups := groupByInvocation(events)
	if len(groups) != 1 {
		t.Fatalf("want 1 in-progress group, got %d", len(groups))
	}
}

func TestIsSummaryEvent(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"3/3 passed  5.2s", true},
		{"0/4 passed  13.7s", true},
		{"0/0 passed  3.1s  setup failed: agent: failed to sign challenge", true},
		{"test    8.0s (remote)", false},
		{"lint    0.4s (local)", false},
		{"synced", false},
		{"", false},
	}
	for _, c := range cases {
		e := eventlog.Event{Op: eventlog.OpValidate, Level: "done", Msg: c.msg}
		if got := isSummaryEvent(e); got != c.want {
			t.Errorf("isSummaryEvent(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

// truncate tests

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{"hello", 1, "h"},
		{"", 5, ""},
		{"héllo", 3, "hé…"},
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

// recency ordering and per-project event caps

func TestSortByActivity_freshestProjectFirst(t *testing.T) {
	now := time.Now()
	sidecars := []sidecarInfo{
		{id: "stale", repoName: "old-project", fileMtime: now.Add(-6 * time.Hour)},
		{id: "older", repoName: "busy-project", lastActivity: now.Add(-30 * time.Minute)},
		{id: "newest", repoName: "busy-project", lastActivity: now.Add(-1 * time.Minute)},
	}

	sortByActivity(sidecars, "")

	want := []string{"newest", "older", "stale"}
	for i, id := range want {
		if sidecars[i].id != id {
			t.Errorf("position %d: want %s, got %s", i, id, sidecars[i].id)
		}
	}
}

func TestSortByActivity_keepsProjectsGrouped(t *testing.T) {
	now := time.Now()
	sidecars := []sidecarInfo{
		{id: "a1", repoName: "a", lastActivity: now.Add(-2 * time.Minute)},
		{id: "b1", repoName: "b", lastActivity: now.Add(-1 * time.Minute)},
		{id: "a2", repoName: "a", lastActivity: now.Add(-3 * time.Minute)},
	}

	sortByActivity(sidecars, "")

	// Project b is freshest so it leads, then both of a's sidecars together.
	want := []string{"b1", "a1", "a2"}
	for i, id := range want {
		if sidecars[i].id != id {
			t.Errorf("position %d: want %s, got %s", i, id, sidecars[i].id)
		}
	}
}

func TestFilterSidecars_noPerProjectCap(t *testing.T) {
	now := time.Now()
	sidecars := []sidecarInfo{
		{id: "s1", projectName: "p", lastActivity: now.Add(-1 * time.Minute)},
		{id: "s2", projectName: "p", lastActivity: now.Add(-2 * time.Minute)},
		{id: "s3", projectName: "p", lastActivity: now.Add(-3 * time.Minute)},
		{id: "s4", projectName: "p", lastActivity: now.Add(-4 * time.Minute)},
	}

	sortByActivity(sidecars, "")
	got := filterSidecars(sidecars, 2)

	if len(got) != 4 {
		t.Fatalf("want all 4 sidecars, got %d", len(got))
	}
	for i, id := range []string{"s1", "s2", "s3", "s4"} {
		if got[i].id != id {
			t.Errorf("position %d: want %s, got %s", i, id, got[i].id)
		}
	}
}

func TestFilterSidecars_dropsInactive(t *testing.T) {
	now := time.Now()
	sidecars := []sidecarInfo{
		{id: "recent", projectName: "p", lastActivity: now.Add(-59 * time.Minute)},
		{id: "just-aged-out", projectName: "p", lastActivity: now.Add(-61 * time.Minute)},
		{id: "mtime-recent", projectName: "q", fileMtime: now.Add(-10 * time.Minute)},
		{id: "mtime-old", projectName: "q", fileMtime: now.Add(-25 * time.Hour)},
		{id: "no-activity-at-all", projectName: "r"},
	}

	got := filterSidecars(sidecars, 10)

	want := []string{"recent", "mtime-recent"}
	if len(got) != len(want) {
		t.Fatalf("want %d sidecars, got %d", len(want), len(got))
	}
	for i, id := range want {
		if got[i].id != id {
			t.Errorf("position %d: want %s, got %s", i, id, got[i].id)
		}
	}
}

func TestFilterSidecars_fallsBackToPaneFullOfRecent(t *testing.T) {
	now := time.Now()
	// Nothing inside the hour, so the fallback fills the pane by recency.
	sidecars := []sidecarInfo{
		{id: "h2", projectName: "a", lastActivity: now.Add(-2 * time.Hour)},
		{id: "h3", projectName: "b", lastActivity: now.Add(-3 * time.Hour)},
		{id: "h4", projectName: "c", lastActivity: now.Add(-4 * time.Hour)},
		{id: "yesterday", projectName: "d", lastActivity: now.Add(-25 * time.Hour)},
	}

	got := filterSidecars(sidecars, 2)

	if len(got) != 2 {
		t.Fatalf("want 2 sidecars (pane capacity), got %d", len(got))
	}
	for i, id := range []string{"h2", "h3"} {
		if got[i].id != id {
			t.Errorf("position %d: want %s, got %s", i, id, got[i].id)
		}
	}
}

func TestFilterSidecars_fallbackStopsAtOneDay(t *testing.T) {
	now := time.Now()
	sidecars := []sidecarInfo{
		{id: "just-inside", projectName: "a", lastActivity: now.Add(-23 * time.Hour)},
		{id: "too-old", projectName: "b", lastActivity: now.Add(-25 * time.Hour)},
	}

	got := filterSidecars(sidecars, 10)

	if len(got) != 1 || got[0].id != "just-inside" {
		t.Fatalf("want only just-inside, got %v", got)
	}
}

func TestFilterSidecars_activeSetIgnoresCapacity(t *testing.T) {
	now := time.Now()
	var sidecars []sidecarInfo
	for i := range 8 {
		sidecars = append(sidecars, sidecarInfo{
			id: fmt.Sprintf("s%d", i), projectName: "p",
			lastActivity: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	// All eight are inside the hour, so none are dropped to fit the pane.
	if got := filterSidecars(sidecars, 2); len(got) != 8 {
		t.Fatalf("want all 8 active sidecars, got %d", len(got))
	}
}

func TestSidecarCapacity(t *testing.T) {
	tests := []struct {
		name   string
		height int
		want   int
	}{
		{"unset height", 0, defaultCapacity},
		{"tiny terminal", 8, 1},
		{"40 rows", 40, 5},
		{"80 rows", 80, 12},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(nil, false)
			m.height = tt.height
			if got := m.sidecarCapacity(); got != tt.want {
				t.Errorf("height %d: want %d, got %d", tt.height, tt.want, got)
			}
		})
	}
}

func TestUpdate_selectionFollowsSidecarID(t *testing.T) {
	now := time.Now()
	m := New(nil, false)
	m.sidecars = []sidecarInfo{{id: "a", projectName: "p"}, {id: "b", projectName: "p"}}

	// Select the second sidecar.
	next, _ := m.Update(tea.KeyPressMsg{Code: 's'})
	m = next.(Model)
	if m.selectedID != "b" {
		t.Fatalf("want selectedID b, got %q", m.selectedID)
	}

	// A poll reorders the list; the selection should follow the id, not the index.
	next, _ = m.Update(dataMsg{
		sidecars: []sidecarInfo{
			{id: "c", projectName: "p", lastActivity: now},
			{id: "b", projectName: "p", lastActivity: now.Add(-time.Minute)},
			{id: "a", projectName: "p", lastActivity: now.Add(-2 * time.Minute)},
		},
	})
	m = next.(Model)
	if m.selectedIdx != 1 || m.selectedID != "b" {
		t.Errorf("want selection to stay on b (idx 1), got idx %d id %q", m.selectedIdx, m.selectedID)
	}
}

func TestUpdate_initialSelectionPicksMostRecent(t *testing.T) {
	// On first entry, New() has no selection. Even when a local runner (id="")
	// is in the list with older activity, the freshest sidecar must be selected.
	now := time.Now()
	m := New(nil, false)

	next, _ := m.Update(dataMsg{sidecars: []sidecarInfo{
		{id: "newest", lastActivity: now.Add(-1 * time.Minute)},
		{id: "older", lastActivity: now.Add(-30 * time.Minute)},
		{id: "", name: "local", lastActivity: now.Add(-45 * time.Minute)},
	}})
	m = next.(Model)

	if m.selectedIdx != 0 || m.selectedID != "newest" {
		t.Errorf("want initial selection at idx 0 (newest), got idx %d id %q", m.selectedIdx, m.selectedID)
	}
}

func TestUpdate_unknownSelectionFallsBackToFreshest(t *testing.T) {
	m := New(nil, false)
	m.selectedID = "gone"

	next, _ := m.Update(dataMsg{sidecars: []sidecarInfo{{id: "fresh"}, {id: "older"}}})
	m = next.(Model)

	if m.selectedIdx != 0 || m.selectedID != "fresh" {
		t.Errorf("want fallback to idx 0 (fresh), got idx %d id %q", m.selectedIdx, m.selectedID)
	}
}

// The update notice is right-aligned with padding, so a notice that does not
// fit would wrap the footer and push the fixed-height layout off screen.
// The daemon-unavailable line used to be appended past the last row of the
// terminal, so it was clipped at every height and a dead daemon looked like a
// dashboard that had simply gone quiet.
func TestRender_daemonErrorIsVisibleAndStillFitsTheTerminal(t *testing.T) {
	now := time.Now()
	for _, height := range []int{12, 24, 40, 60} {
		m := sessionModel("", []sidecarInfo{
			{id: "id1", sessionID: "sessA", repoName: "repo", branch: "main",
				lastActivity: now},
		})
		m.width = 100
		m.height = height
		m.selectedIdx = 0
		m.daemonErr = errors.New("connect to watch daemon: no such file")

		out := m.render()

		assert.Assert(t, strings.Contains(out, "daemon unavailable: connect to watch daemon"),
			"height %d: message missing:\n%s", height, out)
		assert.Assert(t, strings.Count(out, "\n") <= height,
			"height %d: rendered %d lines, which would clip the message", height, strings.Count(out, "\n"))
	}
}

func TestRender_withoutADaemonErrorTheLayoutIsUnchanged(t *testing.T) {
	now := time.Now()
	m := sessionModel("", []sidecarInfo{
		{id: "id1", sessionID: "sessA", repoName: "repo", branch: "main",
			lastActivity: now},
	})
	m.width = 100
	m.height = 24
	m.selectedIdx = 0

	assert.Equal(t, strings.Count(m.render(), "\n"), 24)
}

// sessionModel leaves the selection at -1 so labels are not masked by the ▶
// marker. Rendering must survive that rather than indexing m.sidecars[-1].
func TestRender_survivesAnOutOfRangeSelection(t *testing.T) {
	now := time.Now()
	for _, idx := range []int{-1, 0, 5} {
		m := sessionModel("", []sidecarInfo{
			{id: "id1", sessionID: "sessA", repoName: "repo", branch: "main",
				lastActivity: now},
		})
		m.width = 100
		m.height = 24
		m.selectedIdx = idx

		assert.Assert(t, m.render() != "", "selection %d rendered nothing", idx)
	}
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

// session-aware rendering tests

// sessionModel builds a model whose own session is fixed, so tests do not
// depend on whether the suite itself runs inside an agent session.
func sessionModel(own string, sidecars []sidecarInfo) Model {
	m := New(nil, false)
	m.ownSession = own
	m.sidecars = sidecars
	m.selectedIdx = -1 // no selection, so the ▶ marker cannot mask a label
	return m
}

func TestRenderSidecarPane_singleSessionKeepsBranchLabel(t *testing.T) {
	m := sessionModel("sessA", []sidecarInfo{
		{id: "id1", name: "chunk-cli-sessA", sessionID: "sessA",
			repoName: "chunk-cli", branch: "main", lastActivity: time.Now()},
	})

	pane := strings.Join(m.renderSidecarPane(newWatchStyles(false), 40), "\n")

	// One session in the worktree: nothing to disambiguate, so the row reads
	// exactly as it did before sessions existed.
	assert.Assert(t, strings.Contains(pane, "main"), pane)
	assert.Assert(t, !strings.Contains(pane, "sessions"), pane)
	assert.Assert(t, !strings.Contains(pane, "this session"), pane)
}

func TestRenderSidecarPane_twoSessionsAreNamed(t *testing.T) {
	now := time.Now()
	m := sessionModel("aaaaaaaa-1111-2222-3333-444444444444", []sidecarInfo{
		{id: "id1", name: "chunk-cli-aaaaaaaa", sessionID: "aaaaaaaa-1111-2222-3333-444444444444",
			repoName: "chunk-cli", branch: "main", lastActivity: now},
		{id: "id2", name: "chunk-cli-bbbbbbbb", sessionID: "bbbbbbbb-1111-2222-3333-444444444444",
			repoName: "chunk-cli", branch: "main", lastActivity: now.Add(-5 * time.Minute)},
	})

	pane := strings.Join(m.renderSidecarPane(newWatchStyles(false), 40), "\n")

	// The branch is named once for the group, and each row by its session.
	assert.Assert(t, strings.Contains(pane, "2 sessions"), pane)
	assert.Assert(t, strings.Contains(pane, "this session"), pane)
	assert.Assert(t, strings.Contains(pane, "bbbbbbbb"), pane)
	// The full UUID is never printed — it is unreadable at this pane width.
	assert.Assert(t, !strings.Contains(pane, "bbbbbbbb-1111"), pane)
}

func TestRenderSidecarPane_groupHeaderPrintedOncePerWorktree(t *testing.T) {
	now := time.Now()
	m := sessionModel("", []sidecarInfo{
		{id: "id1", sessionID: "sessA", repoName: "chunk-cli", branch: "main", lastActivity: now},
		{id: "id2", sessionID: "sessB", repoName: "chunk-cli", branch: "main", lastActivity: now},
	})

	pane := strings.Join(m.renderSidecarPane(newWatchStyles(false), 40), "\n")

	assert.Equal(t, strings.Count(pane, "2 sessions"), 1, pane)
}

func TestRenderSidecarPane_detachedHeadNamesTheDirectory(t *testing.T) {
	now := time.Now()
	// A distinct projectName, so finding it proves the group header used the
	// directory rather than merely matching the repo header above it.
	m := sessionModel("", []sidecarInfo{
		{id: "id1", sessionID: "sessA", repoName: "monorepo", projectName: "wt-detached", lastActivity: now},
		{id: "id2", sessionID: "sessB", repoName: "monorepo", projectName: "wt-detached", lastActivity: now},
	})

	// With no branch to name the group, the header must still say something the
	// reader can act on rather than rendering blank.
	lines := m.renderSidecarPane(newWatchStyles(false), 40)
	countAt := -1
	for i, l := range lines {
		if strings.Contains(l, "2 sessions") {
			countAt = i
		}
	}
	assert.Assert(t, countAt > 0, strings.Join(lines, "\n"))
	assert.Assert(t, strings.Contains(lines[countAt-1], "wt-detached"), lines[countAt-1])
}

func TestRenderSidecarPane_dropsWholeRowsRatherThanCuttingOne(t *testing.T) {
	now := time.Now()
	m := sessionModel("", []sidecarInfo{
		{id: "id1", sessionID: "sessA", repoName: "repo-a", branch: "main",
			lastActivity: now},
		{id: "id2", sessionID: "sessB", repoName: "repo-b", branch: "main",
			lastActivity: now.Add(-time.Minute)},
	})

	// Room for the title, the blank under it, and the first row only. The second
	// row would previously have been started and then clipped by renderBody,
	// losing its sync badge and age.
	pane := strings.Join(m.renderSidecarPane(newWatchStyles(false), 9), "\n")

	// A complete row ends in an age line, so one age line per sync badge means
	// no row was cut part-way through.
	assert.Equal(t, strings.Count(pane, "synced via rsync"), strings.Count(pane, "ago"), pane)
	assert.Assert(t, strings.Contains(pane, "1 more"), pane)
}

func TestRenderSidecarPane_noOverflowHintWhenEverythingFits(t *testing.T) {
	now := time.Now()
	m := sessionModel("", []sidecarInfo{
		{id: "id1", sessionID: "sessA", repoName: "repo-a", branch: "main",
			lastActivity: now},
		{id: "id2", sessionID: "sessB", repoName: "repo-b", branch: "main",
			lastActivity: now.Add(-time.Minute)},
	})

	pane := strings.Join(m.renderSidecarPane(newWatchStyles(false), 40), "\n")

	assert.Assert(t, !strings.Contains(pane, "more"), pane)
}

func TestRenderSidecarPane_sameBranchInTwoCheckoutsNamesTheDirectory(t *testing.T) {
	now := time.Now()
	// Two clones of one repo, both on main. repoName is the basename of the main
	// worktree, so both collapse under one repo header — and the basenames match
	// too, which is exactly why the branch alone cannot name these rows.
	m := sessionModel("", []sidecarInfo{
		{id: "id1", sessionID: "sessA", repoName: "chunk-cli", projectName: "chunk-cli",
			projectPath: "/Users/j/work/chunk-cli", branch: "main", projectIdx: 0, lastActivity: now},
		{id: "id2", sessionID: "sessB", repoName: "chunk-cli", projectName: "chunk-cli",
			projectPath: "/Users/j/tmp/chunk-cli", branch: "main", projectIdx: 1,
			lastActivity: now.Add(-1 * time.Minute)},
	})

	pane := strings.Join(m.renderSidecarPane(newWatchStyles(false), 40), "\n")

	assert.Assert(t, strings.Contains(pane, "work/chunk-cli"), pane)
	assert.Assert(t, strings.Contains(pane, "tmp/chunk-cli"), pane)
}

func TestRenderSidecarPane_distinctBranchesKeepTheirBranchLabels(t *testing.T) {
	now := time.Now()
	// Two directories of one repo on different branches: the branch already tells
	// them apart, so nothing should change.
	m := sessionModel("", []sidecarInfo{
		{id: "id1", sessionID: "sessA", repoName: "chunk-cli", projectName: "chunk-cli",
			projectPath: "/Users/j/work/chunk-cli", branch: "main", projectIdx: 0, lastActivity: now},
		{id: "id2", sessionID: "sessB", repoName: "chunk-cli", projectName: "chunk-cli-wt",
			projectPath: "/Users/j/work/chunk-cli-wt", branch: "feature", projectIdx: 1,
			lastActivity: now.Add(-1 * time.Minute)},
	})

	pane := strings.Join(m.renderSidecarPane(newWatchStyles(false), 40), "\n")

	assert.Assert(t, strings.Contains(pane, "main"), pane)
	assert.Assert(t, strings.Contains(pane, "feature"), pane)
	assert.Assert(t, !strings.Contains(pane, "work/chunk-cli"), pane)
}

func TestRenderSidecarPane_ambiguousBranchNamesTheDirectoryInTheGroupHeader(t *testing.T) {
	now := time.Now()
	// One of the two checkouts is itself shared by two sessions: its group header
	// has to name the directory, since "main" would match the other checkout.
	m := sessionModel("sessA", []sidecarInfo{
		{id: "id1", sessionID: "sessA", repoName: "chunk-cli", projectName: "chunk-cli",
			projectPath: "/Users/j/work/chunk-cli", branch: "main", projectIdx: 0, lastActivity: now},
		{id: "id2", sessionID: "sessB", repoName: "chunk-cli", projectName: "chunk-cli",
			projectPath: "/Users/j/work/chunk-cli", branch: "main", projectIdx: 0,
			lastActivity: now.Add(-1 * time.Minute)},
		{id: "id3", sessionID: "sessC", repoName: "chunk-cli", projectName: "chunk-cli",
			projectPath: "/Users/j/tmp/chunk-cli", branch: "main", projectIdx: 1,
			lastActivity: now.Add(-2 * time.Minute)},
	})

	pane := strings.Join(m.renderSidecarPane(newWatchStyles(false), 40), "\n")

	assert.Assert(t, strings.Contains(pane, "work/chunk-cli"), pane)
	assert.Assert(t, strings.Contains(pane, "2 sessions"), pane)
	assert.Assert(t, strings.Contains(pane, "tmp/chunk-cli"), pane)
}

func TestShortestUniqueSuffixes(t *testing.T) {
	tests := []struct {
		name  string
		paths map[int]string
		want  map[int]string
	}{
		{
			"colliding basenames take the parent",
			map[int]string{0: "/Users/j/work/chunk-cli", 1: "/Users/j/tmp/chunk-cli"},
			map[int]string{0: "work/chunk-cli", 1: "tmp/chunk-cli"},
		},
		{
			"distinct basenames stay short",
			map[int]string{0: "/Users/j/work/chunk-cli", 1: "/Users/j/work/chunk-cli-wt"},
			map[int]string{0: "chunk-cli", 1: "chunk-cli-wt"},
		},
		{
			"digs as deep as it must",
			map[int]string{0: "/a/one/x/repo", 1: "/a/two/x/repo"},
			map[int]string{0: "one/x/repo", 1: "two/x/repo"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shortestUniqueSuffixes(tc.paths)
			for idx, want := range tc.want {
				assert.Equal(t, got[idx], want, "index %d", idx)
			}
		})
	}
}

func TestSortByActivity_pinsTheViewersOwnRowFirstInItsGroup(t *testing.T) {
	now := time.Now()
	// The viewer's own session is the stalest of the three, so recency alone
	// would bury it in the middle of the group.
	sidecars := []sidecarInfo{
		{id: "theirs-new", sessionID: "sessB", repoName: "r", branch: "main", lastActivity: now},
		{id: "mine", sessionID: "sessMine", repoName: "r", branch: "main", lastActivity: now.Add(-30 * time.Minute)},
		{id: "theirs-mid", sessionID: "sessC", repoName: "r", branch: "main", lastActivity: now.Add(-5 * time.Minute)},
	}

	sortByActivity(sidecars, "sessMine")

	// Own row first, and the rest still in recency order behind it.
	want := []string{"mine", "theirs-new", "theirs-mid"}
	for i, id := range want {
		assert.Equal(t, sidecars[i].id, id, "position %d", i)
	}
}

func TestSortByActivity_pinningDoesNotJumpGroups(t *testing.T) {
	now := time.Now()
	// A busier branch must still lead the repo: pinning orders rows inside a
	// group, it does not promote the viewer's group over a fresher one.
	sidecars := []sidecarInfo{
		{id: "busy", sessionID: "sessB", repoName: "r", branch: "feature", lastActivity: now},
		{id: "mine", sessionID: "sessMine", repoName: "r", branch: "main", lastActivity: now.Add(-30 * time.Minute)},
	}

	sortByActivity(sidecars, "sessMine")

	assert.Equal(t, sidecars[0].id, "busy")
}

func TestRenderSidecarPane_localRunnerIsNotCountedAsASession(t *testing.T) {
	now := time.Now()
	m := sessionModel("", []sidecarInfo{
		{id: "id1", sessionID: "sessA", repoName: "r", branch: "main", lastActivity: now},
		{id: "id2", sessionID: "sessB", repoName: "r", branch: "main", lastActivity: now.Add(-1 * time.Minute)},
		{id: "", name: localRunnerName, repoName: "r", branch: "main", lastActivity: now.Add(-2 * time.Minute)},
	})

	pane := strings.Join(m.renderSidecarPane(newWatchStyles(false), 40), "\n")

	// Three rows, but only two of them are sessions.
	assert.Assert(t, strings.Contains(pane, "2 sessions"), pane)
	assert.Assert(t, !strings.Contains(pane, "3 sessions"), pane)
	assert.Assert(t, strings.Contains(pane, "○ "+localRunnerName), pane)
}

func TestRowLabel(t *testing.T) {
	m := New(nil, false)
	m.ownSession = "mine1234-abcd"

	tests := []struct {
		name     string
		sc       sidecarInfo
		multi    bool
		dirLabel string
		want     string
	}{
		{"alone uses the branch", sidecarInfo{id: "x", branch: "main", sessionID: "other"}, false, "", "main"},
		{"a branch checked out twice is named by directory", sidecarInfo{id: "x", branch: "main"}, false, "work/repo", "work/repo"},
		{"alone with no branch falls back to the identifier", sidecarInfo{id: "11111111-aaaa-bbbb-cccc-000000000001", name: "sc-1"}, false, "", "11111111-aaaa-bbbb-cccc-000000000001"},
		{"own session is called out", sidecarInfo{id: "x", branch: "main", sessionID: "mine1234-abcd"}, true, "", "● this session"},
		{"other session is shortened", sidecarInfo{id: "x", branch: "main", sessionID: "theirs99-abcd"}, true, "", "○ theirs99"},
		{"local runner keeps its name", sidecarInfo{id: "", name: localRunnerName, branch: "main"}, true, "", "○ " + localRunnerName},
		{"pre-session state is abbreviated too", sidecarInfo{id: "11111111-aaaa-bbbb-cccc-000000000001", name: "sc-legacy", branch: "main"}, true, "", "○ 11111111"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, m.rowLabel(tc.sc, tc.multi, tc.dirLabel), tc.want)
		})
	}
}

func TestSortByActivity_keepsWorktreeGroupsTogether(t *testing.T) {
	now := time.Now()
	// Two sessions on main, with a busier branch in the same repo between them.
	// Without a worktree tier the busy branch splits main's rows apart and the
	// pane cannot print one header for them.
	sidecars := []sidecarInfo{
		{id: "main-old", repoName: "r", branch: "main", lastActivity: now.Add(-10 * time.Minute)},
		{id: "feature", repoName: "r", branch: "feature", lastActivity: now.Add(-2 * time.Minute)},
		{id: "main-new", repoName: "r", branch: "main", lastActivity: now.Add(-1 * time.Minute)},
	}

	sortByActivity(sidecars, "")

	want := []string{"main-new", "main-old", "feature"}
	for i, id := range want {
		assert.Equal(t, sidecars[i].id, id, "position %d", i)
	}
}

func TestRenderActivityPane_namesTheSelectedSession(t *testing.T) {
	now := time.Now()
	m := sessionModel("mine1234-abcd", []sidecarInfo{
		{id: "id1", name: "sc-1", sessionID: "theirs99-abcd", repoName: "chunk-cli",
			branch: "main", lastActivity: now},
	})
	m.selectedIdx = 0
	m.width = 120

	pane := strings.Join(m.renderActivityPane(newWatchStyles(false), 20), "\n")

	// The left pane can be scrolled away from the selection, so the activity
	// header has to say whose events these are on its own.
	assert.Assert(t, strings.Contains(pane, "theirs99"), pane)
}

func TestConvertSnapshot_carriesSessionID(t *testing.T) {
	now := time.Now()
	snap := watchd.Snapshot{Projects: []watchd.ProjectSnapshot{{
		Root:     "/tmp/repo",
		Branch:   "main",
		RepoName: "repo",
		Sidecars: []watchd.SidecarState{
			{ID: "id1", Name: "sc-1", SessionID: "sessA", LastActivity: now},
			{ID: "id2", Name: "sc-2", SessionID: "sessB", LastActivity: now},
		},
	}}}

	msg := convertSnapshot(snap, New(nil, true))

	got := map[string]string{}
	for _, sc := range msg.sidecars {
		got[sc.id] = sc.sessionID
	}
	assert.Equal(t, got["id1"], "sessA")
	assert.Equal(t, got["id2"], "sessB")
}

// A local validate run belongs to the worktree, not to any one agent. Folding
// its events into a session's row makes the pane claim another session ran them.
func TestConvertSnapshot_localRunIsNotAttributedToASession(t *testing.T) {
	now := time.Now()
	snap := twoSessionSnapshot(now.Add(-5*time.Minute), now.Add(-2*time.Minute), now.Add(-1*time.Minute))
	m := New(nil, false)
	m.height = 60

	msg := convertSnapshot(snap, m)

	for _, sc := range msg.sidecars {
		if sc.sessionID == "" {
			continue // the local row itself, or unattributed state
		}
		assert.Assert(t, !hasSidecarID(sc.sidecarIDs, ""),
			"session %q absorbed the local run: ids=%v", sc.sessionID, sc.sidecarIDs)
	}
}

// twoSessionSnapshot builds a daemon snapshot for one worktree driven by two
// sessions. localAt, when non-zero, adds a local (non-sidecar) validate run.
func twoSessionSnapshot(aAt, bAt, localAt time.Time) watchd.Snapshot {
	ev := func(ts time.Time, sidecarID string) eventlog.Event {
		return eventlog.Event{Ts: ts, SidecarID: sidecarID, Op: eventlog.OpValidate,
			Level: levelDone, Msg: "1/1 passed"}
	}
	events := []eventlog.Event{ev(aAt, "idA"), ev(bAt, "idB")}
	if !localAt.IsZero() {
		events = append(events, ev(localAt, ""))
	}
	return watchd.Snapshot{Projects: []watchd.ProjectSnapshot{{
		Root:     "/repo",
		Branch:   "main",
		RepoName: "repo",
		Sidecars: []watchd.SidecarState{
			{ID: "idA", Name: "repo-sessA", SessionID: "sessA", RepoName: "repo", LastActivity: aAt},
			{ID: "idB", Name: "repo-sessB", SessionID: "sessB", RepoName: "repo", LastActivity: bAt},
		},
		Events: events,
	}}}
}

// The unit tests above set m.sidecars directly, so they cannot catch a merge
// that collapses two sessions into one row. This drives the real path —
// convertSnapshot → sortByActivity → mergeBranches → filterSidecars — and
// asserts both sessions survive it with their identities intact.
func TestConvertSnapshot_twoSessionsSurviveTheMerge(t *testing.T) {
	now := time.Now()
	msg := convertSnapshot(twoSessionSnapshot(now, now.Add(-5*time.Minute), time.Time{}), New(nil, true))

	assert.Equal(t, len(msg.sidecars), 2)
	assert.Equal(t, sharedWorktrees(msg.sidecars)[groupOf(msg.sidecars[0])], 2)

	m := sessionModel("sessA", msg.sidecars)
	pane := strings.Join(m.renderSidecarPane(newWatchStyles(false), 40), "\n")
	assert.Assert(t, strings.Contains(pane, "2 sessions"), pane)
	assert.Assert(t, strings.Contains(pane, "this session"), pane)
	assert.Assert(t, strings.Contains(pane, "sessB"), pane)
}

// oneSessionSnapshot builds a snapshot for a worktree held by a single session,
// with the local run fresher than the sidecar so the local row leads its group.
func oneSessionSnapshot(sidecarAt, localAt time.Time) watchd.Snapshot {
	ev := func(ts time.Time, sidecarID string) eventlog.Event {
		return eventlog.Event{Ts: ts, SidecarID: sidecarID, Op: eventlog.OpValidate,
			Level: levelDone, Msg: "1/1 passed"}
	}
	return watchd.Snapshot{Projects: []watchd.ProjectSnapshot{{
		Root:     "/repo",
		Branch:   "main",
		RepoName: "repo",
		Sidecars: []watchd.SidecarState{
			{ID: "idA", Name: "repo-sessA", SessionID: "sessA", RepoName: "repo", LastActivity: sidecarAt},
		},
		Events: []eventlog.Event{ev(sidecarAt, "idA"), ev(localAt, "")},
	}}}
}

// A local run fresher than the sidecar makes the local row lead its group, so
// mergeBranches promotes the sidecar onto it. The promotion has to carry the
// session, or the activity pane stops naming it. Only a worktree held by one
// session folds the local row in at all — see the test below for the other case.
func TestConvertSnapshot_promotionKeepsTheSession(t *testing.T) {
	now := time.Now()
	msg := convertSnapshot(oneSessionSnapshot(now.Add(-10*time.Minute), now), New(nil, true))

	assert.Equal(t, len(msg.sidecars), 1)
	assert.Equal(t, msg.sidecars[0].id, "idA")
	assert.Equal(t, msg.sidecars[0].sessionID, "sessA")

	m := sessionModel("sessA", msg.sidecars)
	m.selectedIdx = 0
	m.width = 120
	pane := strings.Join(m.renderActivityPane(newWatchStyles(false), 20), "\n")
	assert.Assert(t, strings.Contains(pane, "this session"), pane)
}

// Two sessions plus a local run: the local row belongs to neither session, so it
// stays a row of its own. The worktree then shows three rows described as two
// sessions, and no session is credited with the local run.
func TestConvertSnapshot_localRowStaysSeparateWhenSessionsShareAWorktree(t *testing.T) {
	now := time.Now()
	msg := convertSnapshot(
		twoSessionSnapshot(now.Add(-10*time.Minute), now.Add(-11*time.Minute), now),
		New(nil, true))

	assert.Equal(t, len(msg.sidecars), 3)
	sessions := map[string]string{}
	for _, sc := range msg.sidecars {
		sessions[sc.id] = sc.sessionID
	}
	assert.Equal(t, sessions["idA"], "sessA")
	assert.Equal(t, sessions["idB"], "sessB")

	m := sessionModel("sessA", msg.sidecars)
	pane := strings.Join(m.renderSidecarPane(newWatchStyles(false), 40), "\n")
	assert.Assert(t, strings.Contains(pane, "2 sessions"), pane)
	assert.Assert(t, strings.Contains(pane, "this session"), pane)
}

// outcomeOf tests

func TestOutcomeOf(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name      string
		events    []eventlog.Event
		wantIcon  string
		wantLabel string
		wantLevel string
	}{
		{
			name:      "running while recent",
			events:    []eventlog.Event{{Op: eventlog.OpValidate, Level: "info", Msg: "Syncing workspace...", Ts: now.Add(-time.Minute)}},
			wantIcon:  "●",
			wantLabel: "running",
			wantLevel: "",
		},
		{
			name:      "abandoned once past the running timeout",
			events:    []eventlog.Event{{Op: eventlog.OpValidate, Level: "info", Msg: "Syncing workspace...", Ts: now.Add(-8 * time.Hour)}},
			wantIcon:  "⊘",
			wantLabel: "abandoned",
			wantLevel: levelAbandoned,
		},
		{
			name: "passed",
			events: []eventlog.Event{
				{Op: eventlog.OpValidate, Level: "info", Msg: "$ task test", Ts: now.Add(-8 * time.Hour)},
				{Op: eventlog.OpValidate, Level: "done", Msg: "4/4 passed  32.4s", Ts: now.Add(-8 * time.Hour)},
			},
			wantIcon:  "✓",
			wantLabel: "4/4",
			wantLevel: levelDone,
		},
		{
			name:      "setup failure closes the invocation",
			events:    []eventlog.Event{{Op: eventlog.OpValidate, Level: "error", Msg: "0/0 passed  3.1s  setup failed: agent: failed to sign challenge", Ts: now.Add(-8 * time.Hour)}},
			wantIcon:  "✗",
			wantLabel: "0/0",
			wantLevel: levelError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			icon, label, level := outcomeOf(invocationGroup{events: tt.events})
			if icon != tt.wantIcon || label != tt.wantLabel || level != tt.wantLevel {
				t.Errorf("outcomeOf() = (%q, %q, %q), want (%q, %q, %q)", icon, label, level, tt.wantIcon, tt.wantLabel, tt.wantLevel)
			}
		})
	}
}
