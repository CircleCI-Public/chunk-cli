package plan

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// fakeBuild answers Activity with scripted answers, one per call, repeating
// the last, and records what it was asked. Stop records the sessions it is
// asked to stop, and fails with stopErr.
type fakeBuild struct {
	mu      sync.Mutex
	answers []fakeBuildAnswer
	asked   []buildAsk
	stopped []string
	stopErr error
}

type fakeBuildAnswer struct {
	activity BuildActivity
	err      error
}

type buildAsk struct {
	planID string
	after  int
}

func (b *fakeBuild) Activity(_ context.Context, planID string, after int) (BuildActivity, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.asked = append(b.asked, buildAsk{planID, after})
	if len(b.answers) == 0 {
		return BuildActivity{}, nil
	}
	a := b.answers[0]
	if len(b.answers) > 1 {
		b.answers = b.answers[1:]
	}
	return a.activity, a.err
}

// Asked is every read so far.
func (b *fakeBuild) Asked() []buildAsk {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]buildAsk(nil), b.asked...)
}

func (b *fakeBuild) Stop(_ context.Context, sessionID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopped = append(b.stopped, sessionID)
	return b.stopErr
}

// Stopped is every session asked to stop so far.
func (b *fakeBuild) Stopped() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.stopped...)
}

// answerFrom has the fake answer with a from now on, whatever it had left.
func (b *fakeBuild) answerFrom(a BuildActivity) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.answers = []fakeBuildAnswer{{activity: a}}
}

// answer has the fake answer with as, in order, after what it has left.
func (b *fakeBuild) answer(as ...BuildActivity) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, a := range as {
		b.answers = append(b.answers, fakeBuildAnswer{activity: a})
	}
}

// askedFor checks that b was asked for the fake store's plan with each of
// afters, in order, and nothing else.
func askedFor(t *testing.T, b *fakeBuild, afters ...int) {
	t.Helper()
	want := make([]buildAsk, len(afters))
	for i, a := range afters {
		want[i] = buildAsk{buildPlan, a}
	}
	got := b.Asked()
	assert.Check(t, slices.Equal(got, want), "asked %v, want %v", got, want)
}

// buildPlan is the ID the fake store gives a plan that exists.
const buildPlan = "saved-plan"

// buildStore is a store of a plan that exists, in state.
func buildStore(state string) *fakeStore {
	store := newFakeStore()
	store.Created, store.StateV = true, state
	store.TextV = "# Add a flag\n\n## 1. Outcome\n\nThe flag exists."
	return store
}

// buildScreenOn is the screen of store's plan, sized and refreshed once, on a
// host that reads builds from b, or none when b is nil.
func buildScreenOn(t *testing.T, store *fakeStore, b *fakeBuild, height int) Model {
	t.Helper()
	m := New(context.Background(), newFakeAgent(), nil, store, "").WithBack()
	if b != nil {
		m = m.WithBuild(b)
	}
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: height})
	return refreshed(t, m)
}

func buildScreen(t *testing.T, state string, b *fakeBuild) Model {
	t.Helper()
	return buildScreenOn(t, buildStore(state), b, 40)
}

// buildTick fires the build's timer, so a read and its answer run.
func buildTick(t *testing.T, m Model) Model {
	t.Helper()
	return drive(t, m, buildTickMsg{})
}

// lines is n info lines, "line from" to "line from+n-1".
func lines(n, from int) []BuildLine {
	out := make([]BuildLine, n)
	for i := range out {
		out[i] = BuildLine{Level: BuildInfo, Text: fmt.Sprintf("line %d", from+i)}
	}
	return out
}

// paneView is the Flow view, all of it, without styles.
func paneView(m Model) string { return ansi.Strip(m.flowView.GetContent()) }

// sectionRows is the Building section's rows without styles, blank ones left
// out, as the Flow view appends it whole under the activity.
func sectionRows(m Model) []string {
	w := m.flowView.Width()
	var rows []string
	for _, r := range strings.Split(ansi.Strip(m.buildSection(w)), "\n") {
		if strings.TrimSpace(r) != "" {
			rows = append(rows, r)
		}
	}
	return rows
}

func TestBuildIsNotReadWhilePlanning(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"refine-proposal", "refine-units", "init"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			b := &fakeBuild{}
			m := buildScreen(t, state, b)
			m = buildTick(t, m)
			assert.Check(t, cmp.Len(b.Asked(), 0))
			assert.Check(t, !strings.Contains(paneView(m), "Building"), paneView(m))
		})
	}
}

func TestBuildIsReadOnceBuilding(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"build", "integrate"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			b := &fakeBuild{}
			b.answer(BuildActivity{Found: true, ID: "s-1", Running: true, Total: 3, Lines: lines(3, 0)})
			m := buildScreen(t, state, b)
			askedFor(t, b, 0)
			buildTick(t, m)
			askedFor(t, b, 0, 3)
		})
	}
}

func TestBuildIsReadAfterApprovingHere(t *testing.T) {
	t.Parallel()
	b := &fakeBuild{}
	m := buildScreen(t, "refine-proposal", b)
	assert.Check(t, cmp.Len(b.Asked(), 0))
	drive(t, m, approvedMsg{buildID: "s-1"})
	// The approval's own followBuild and its refresh's start one loop.
	askedFor(t, b, 0)
}

func TestBuildIsNotReadOnAnEndedFlow(t *testing.T) {
	t.Parallel()
	t.Run("ended at open", func(t *testing.T) {
		t.Parallel()
		b := &fakeBuild{}
		store := buildStore("ended")
		store.FlowV.Ended = true
		m := buildScreenOn(t, store, b, 40)
		m = buildTick(t, m)
		assert.Check(t, cmp.Len(b.Asked(), 0))
		assert.Check(t, !strings.Contains(ansi.Strip(m.Content()), "── Building"))
	})
	t.Run("ends while open", func(t *testing.T) {
		t.Parallel()
		b := &fakeBuild{}
		b.answer(BuildActivity{Found: true, ID: "s-1", Total: 1, Lines: lines(1, 0)})
		store := buildStore("build")
		m := buildScreenOn(t, store, b, 40)
		assert.Check(t, cmp.Len(b.Asked(), 1))
		store.mu.Lock()
		store.FlowV.Ended, store.StateV = true, "ended"
		store.mu.Unlock()
		m = refreshed(t, m)
		m = buildTick(t, m)
		assert.Check(t, cmp.Len(b.Asked(), 1))
		assert.Check(t, cmp.Contains(paneView(m), "── Building"))
		assert.Check(t, cmp.Contains(paneView(m), "line 0"))
	})
}

func TestBuildIsNotReadWithoutABuild(t *testing.T) {
	t.Parallel()
	m := buildScreenOn(t, buildStore("build"), nil, 40)
	m = buildTick(t, m)
	assert.Check(t, !strings.Contains(paneView(m), "Building"), paneView(m))
}

func TestBuildSectionFollowsThePlanner(t *testing.T) {
	t.Parallel()
	b := &fakeBuild{}
	m := buildScreen(t, "build", b)
	m = drive(t, m, eventMsg{agent: m.agent, Event: Event{Reply: &Reply{Text: "Wrote the units."}}})
	b.answer(BuildActivity{Found: true, ID: "s-1", Running: true, Total: 2, Lines: []BuildLine{
		{Level: BuildStep, Text: "Building plan plan-1 in /w"},
		{Level: BuildInfo, Text: "Bash go test ./..."},
	}})
	m = buildTick(t, m)

	view := paneView(m)
	activity := strings.Index(view, "ACTIVITY")
	divider := strings.Index(view, "── Building")
	step := strings.Index(view, "Building plan plan-1 in /w")
	info := strings.Index(view, "Bash go test ./...")
	assert.Check(t, activity >= 0 && divider > activity, view)
	assert.Check(t, step > divider, view)
	assert.Check(t, info > step, view)
}

func TestBuildSectionStylesByLevel(t *testing.T) {
	t.Parallel()
	warn := "Plan plan-1 is claimed by session x"
	bad := "Could not tell the build session"
	b := &fakeBuild{}
	b.answer(BuildActivity{Found: true, ID: "s-1", Total: 2, Lines: []BuildLine{
		{Level: BuildWarn, Text: warn},
		{Level: BuildError, Text: bad},
	}})
	m := buildScreen(t, "build", b)
	assert.Check(t, cmp.Contains(m.Content(), ui.WarningStyle.Render(warn)))
	assert.Check(t, cmp.Contains(m.Content(), ui.ErrorStyle.Render(bad)))
}

func TestBuildSectionTruncatesRows(t *testing.T) {
	t.Parallel()
	b := &fakeBuild{}
	b.answer(BuildActivity{Found: true, ID: "s-1", Total: 1, Lines: []BuildLine{{Text: strings.Repeat("x", 300)}}})
	m := buildScreen(t, "build", b)

	var row string
	for _, r := range sectionRows(m) {
		if strings.HasPrefix(r, "xxx") {
			assert.Check(t, row == "", "the line takes more than one row")
			row = r
		}
	}
	assert.Check(t, strings.HasSuffix(row, "…"), row)
	for _, l := range strings.Split(paneView(m), "\n") {
		assert.Check(t, ansi.StringWidth(l) <= m.flowView.Width(), "%q is wider than the view", l)
	}
}

func TestBuildSectionSpinsWhileRunning(t *testing.T) {
	t.Parallel()
	t.Run("running", func(t *testing.T) {
		t.Parallel()
		b := &fakeBuild{}
		b.answer(BuildActivity{Found: true, ID: "s-1", Running: true, Total: 2, Lines: lines(2, 0)})
		m := buildScreen(t, "build", b)
		rows := sectionRows(m)
		assert.Check(t, cmp.Equal(rows[len(rows)-1], ansi.Strip(m.spin.View())+"line 1"))
		assert.Check(t, m.spinning())
	})
	t.Run("ended", func(t *testing.T) {
		t.Parallel()
		b := &fakeBuild{}
		b.answer(BuildActivity{Found: true, ID: "s-1", Total: 2, Lines: lines(2, 0)})
		m := buildScreen(t, "build", b)
		rows := sectionRows(m)
		assert.Check(t, cmp.Equal(rows[len(rows)-1], "Build session ended"))
		assert.Check(t, cmp.Equal(rows[len(rows)-2], "line 1"))
		assert.Check(t, !m.spinning())
	})
	t.Run("failed", func(t *testing.T) {
		t.Parallel()
		b := &fakeBuild{}
		b.answer(BuildActivity{Found: true, ID: "s-1", Failed: "boom", Total: 2, Lines: lines(2, 0)})
		m := buildScreen(t, "build", b)
		rows := sectionRows(m)
		assert.Check(t, cmp.Equal(rows[len(rows)-1], "Build session failed: boom"))
		assert.Check(t, cmp.Contains(m.Content(), ui.ErrorStyle.Render("Build session failed: boom")))
		assert.Check(t, !m.spinning())
	})
}

func TestBuildSectionNotesDroppedLines(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		answers     []BuildActivity
		note        string
		first, last string
	}{
		{
			name:    "all kept",
			answers: []BuildActivity{{First: 0, Total: 100, Lines: lines(100, 0)}},
			first:   "line 0",
			last:    "line 99",
		},
		{
			name:    "the daemon dropped some",
			answers: []BuildActivity{{First: 30, Total: 130, Lines: lines(100, 30)}},
			note:    "… 30 earlier lines not kept",
			first:   "line 30",
			last:    "line 129",
		},
		{
			name: "the screen's own cap",
			answers: []BuildActivity{
				{First: 0, Total: 100, Lines: lines(100, 0)},
				{First: 100, Total: 150, Lines: lines(50, 100)},
			},
			note:  "… 50 earlier lines not kept",
			first: "line 50",
			last:  "line 149",
		},
		{
			name: "more than 100 between reads",
			answers: []BuildActivity{
				{First: 30, Total: 130, Lines: lines(100, 30)},
				{First: 200, Total: 300, Lines: lines(100, 200)},
			},
			note:  "… 200 earlier lines not kept",
			first: "line 200",
			last:  "line 299",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := &fakeBuild{}
			for _, a := range tc.answers {
				a.Found, a.ID = true, "s-1"
				b.answer(a)
			}
			m := buildScreen(t, "build", b)
			for range tc.answers[1:] {
				m = buildTick(t, m)
			}

			rows := sectionRows(m)
			// The divider, the note if any, the lines, then the closing row.
			assert.Assert(t, cmp.Contains(rows[0], "── Building"))
			body := rows[1 : len(rows)-1]
			if tc.note != "" {
				assert.Check(t, cmp.Equal(body[0], tc.note))
				body = body[1:]
			}
			assert.Check(t, cmp.Equal(body[0], tc.first))
			assert.Check(t, cmp.Equal(body[len(body)-1], tc.last))
			assert.Assert(t, cmp.Len(m.builds, 1))
			assert.Check(t, cmp.Len(m.builds[0].rows, 100))
		})
	}
}

func TestBuildSectionWithNoBuild(t *testing.T) {
	t.Parallel()
	b := &fakeBuild{}
	b.answer(BuildActivity{}, BuildActivity{Found: true, ID: "s-1", Running: true, Total: 1, Lines: lines(1, 0)})
	m := buildScreen(t, "build", b)
	// The pane is narrow, so the row is truncated to it.
	missing := "No build of this plan in this machine"
	rows := sectionRows(m)
	assert.Assert(t, cmp.Len(rows, 2))
	assert.Check(t, cmp.Contains(rows[0], "── Building"))
	assert.Check(t, strings.HasPrefix(rows[1], missing), rows[1])
	assert.Check(t, cmp.Contains(paneView(m), missing))
	assert.Check(t, cmp.Contains(ansi.Strip(m.buildSection(200)), "No build of this plan in this machine's chunk daemon\n"))

	m = buildTick(t, m)
	rows = sectionRows(m)
	assert.Check(t, cmp.Len(rows, 2))
	assert.Check(t, cmp.Equal(rows[1], ansi.Strip(m.spin.View())+"line 0"))
	assert.Check(t, !strings.Contains(paneView(m), missing))
}

func TestBuildSectionNewSession(t *testing.T) {
	t.Parallel()
	t.Run("the new session is still short", func(t *testing.T) {
		t.Parallel()
		b := &fakeBuild{}
		b.answer(
			BuildActivity{Found: true, ID: "s-1", Total: 1, Lines: []BuildLine{{Text: "a"}}},
			// The daemon reads after=1 as 0, so the answer starts at 0.
			BuildActivity{Found: true, ID: "s-2", Running: true, First: 0, Total: 1, Lines: []BuildLine{{Text: "b"}}},
		)
		m := buildScreen(t, "build", b)
		m = buildTick(t, m)
		asked := b.Asked()
		assert.Check(t, cmp.Equal(asked[1].after, 1))

		rows := sectionRows(m)
		spin := ansi.Strip(m.spin.View())
		assert.Check(t, cmp.DeepEqual(rows[1:3], []string{"a", "Build session ended"}))
		assert.Check(t, cmp.Contains(rows[3], "── Building"))
		assert.Check(t, cmp.DeepEqual(rows[4:], []string{spin + "b"}))
		assert.Check(t, cmp.Contains(rows[0], "── Building"))

		buildTick(t, m)
		asked = b.Asked()
		assert.Check(t, cmp.Equal(asked[len(asked)-1].after, 1))
	})
	t.Run("the new session has passed the old one's total", func(t *testing.T) {
		t.Parallel()
		b := &fakeBuild{}
		b.answer(
			BuildActivity{Found: true, ID: "s-1", Total: 5, Lines: lines(5, 0)},
			BuildActivity{Found: true, ID: "s-2", Running: true, First: 5, Total: 8, Lines: lines(3, 5)},
			BuildActivity{Found: true, ID: "s-2", Running: true, First: 0, Total: 8, Lines: lines(8, 0)},
		)
		m := buildScreen(t, "build", b)
		m = buildTick(t, m)
		// The third read comes at once, without a tick.
		askedFor(t, b, 0, 5, 0)

		rows := sectionRows(m)
		second := -1
		for i, r := range rows {
			if i > 0 && strings.Contains(r, "── Building") {
				second = i
			}
		}
		assert.Assert(t, second > 0, rows)
		under := rows[second+1:]
		assert.Assert(t, cmp.Len(under, 8))
		assert.Check(t, cmp.Equal(under[0], "line 0"))
		assert.Check(t, cmp.Equal(under[7], ansi.Strip(m.spin.View())+"line 7"))
		assert.Check(t, !strings.Contains(strings.Join(rows, "\n"), "earlier lines not kept"))
	})
}

func TestBuildSectionShowsAReadErrorOnce(t *testing.T) {
	t.Parallel()
	b := &fakeBuild{answers: []fakeBuildAnswer{
		{err: errors.New("daemon down")},
		{err: errors.New("daemon down")},
		{err: errors.New("daemon down")},
		{activity: BuildActivity{Found: true, ID: "s-1", Running: true, Total: 1, Lines: lines(1, 0)}},
	}}
	m := buildScreen(t, "build", b)
	m = buildTick(t, m)
	m = buildTick(t, m)
	assert.Check(t, cmp.Len(b.Asked(), 3))
	assert.Check(t, cmp.Equal(strings.Count(paneView(m), "daemon down"), 1), paneView(m))

	m = buildTick(t, m)
	assert.Check(t, !strings.Contains(paneView(m), "daemon down"), paneView(m))
	assert.Check(t, cmp.Contains(paneView(m), "line 0"))
}

// The screen's timers run on the real clock unless a test stops them.
func TestTimersRunOnTheRealClock(t *testing.T) {
	t.Parallel()
	fromNew := func() Model { return New(context.Background(), newFakeAgent(), nil, newFakeStore(), "") }
	for _, tc := range []struct {
		name  string
		model func() Model
		every time.Duration
		want  time.Duration
		cmd   func(Model) tea.Cmd
		msg   tea.Msg
	}{
		{
			name:  "build read",
			model: fromNew,
			every: buildEvery,
			want:  2 * time.Second,
			cmd:   Model.buildTickCmd,
			msg:   buildTickMsg{},
		},
		{
			name:  "poll",
			model: fromNew,
			every: pollEvery,
			want:  5 * time.Second,
			cmd:   Model.pollCmd,
			msg:   pollMsg{},
		},
		{
			// A Model not built by New has a nil tick, which is the real clock.
			name:  "zero Model build read",
			model: func() Model { return Model{} },
			every: buildEvery,
			want:  2 * time.Second,
			cmd:   Model.buildTickCmd,
			msg:   buildTickMsg{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Check(t, cmp.Equal(tc.every, tc.want))
			synctest.Test(t, func(t *testing.T) {
				m := tc.model()
				start := time.Now()
				got := tc.cmd(m)() // blocks on the fake clock
				assert.Check(t, cmp.DeepEqual(got, tc.msg))
				assert.Check(t, cmp.Equal(time.Since(start), tc.every))
			})
		})
	}
}
