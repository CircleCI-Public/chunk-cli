package flowlist_test

import (
	"context"
	"errors"
	"fmt"
	"image/color"
	"maps"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/flowlist"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

// driver runs the component's commands, timers included, and hands back the
// messages they return. Each test runs in a synctest bubble, so the poll's
// timer fires as soon as everything else is blocked.
type driver struct {
	t      *testing.T
	m      flowlist.Model
	ctx    context.Context
	cancel context.CancelFunc
	events chan tea.Msg
	wg     sync.WaitGroup
}

func newDriver(t *testing.T, list func(context.Context) ([]circleci.Flow, error), preview func(context.Context, string) (string, []circleci.PlanComment, error), opts ...func(flowlist.Model) flowlist.Model) *driver {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &driver{t: t, ctx: ctx, cancel: cancel, events: make(chan tea.Msg, 100)}
	d.m = flowlist.New(ctx, list, preview)
	for _, o := range opts {
		d.m = o(d.m)
	}
	t.Cleanup(func() { cancel(); d.wg.Wait() })
	return d
}

func (d *driver) run(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		msg := cmd()
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, next := range batch {
				d.run(next)
			}
			return
		}
		if msg != nil {
			select {
			case d.events <- msg:
			case <-d.ctx.Done():
			}
		}
	}()
}

func (d *driver) send(msg tea.Msg) {
	var cmd tea.Cmd
	d.m, cmd = d.m.Update(msg)
	d.run(cmd)
}

func (d *driver) next() tea.Msg { return <-d.events }

func (d *driver) step() { d.send(d.next()) }

func (d *driver) load() {
	d.send(d.m.Init()())
	d.step() // List response starts the first preview and poll timer.
	d.step() // Preview response.
}

func (d *driver) refresh() {
	d.send(d.m.Refresh()())
	d.step()
}

func (d *driver) content() string { return ansi.Strip(d.m.Content()) }

func press(code rune) tea.KeyPressMsg {
	msg := tea.KeyPressMsg{Code: code}
	if code >= ' ' && code <= '~' {
		msg.Text = string(code)
	}
	return msg
}

func fixed(flows ...circleci.Flow) func(context.Context) ([]circleci.Flow, error) {
	return func(context.Context) ([]circleci.Flow, error) { return flows, nil }
}

func parents(m map[string]string) func(flowlist.Model) flowlist.Model {
	return func(fm flowlist.Model) flowlist.Model {
		return fm.WithParents(func(context.Context) (map[string]string, error) { return m, nil })
	}
}

func blockers(m map[string][]planstate.Blocker, err error) func(flowlist.Model) flowlist.Model {
	return func(fm flowlist.Model) flowlist.Model {
		return fm.WithBlockers(func(_ context.Context, id string) ([]planstate.Blocker, error) {
			return m[id], err
		})
	}
}

// waitHuman and waitAgent are the next of a flow that waits on the
// developer and on a session.
var (
	waitHuman = []circleci.Transition{{Actor: "human", Event: circleci.AddressComments}}
	waitAgent = []circleci.Transition{{Actor: "session", TaskCategory: "build-unit", IsDispatched: true}}
)

// subPlans are flows with sub-plans in one sub-group and across two, and
// subPlanParents their parents.
var (
	subPlans = []circleci.Flow{
		{ID: "docs", Title: "retry-docs", State: "Planning - Human", Next: waitHuman},
		{ID: "other", Title: "other", State: "Planning - Human", Next: waitHuman},
		{ID: "p1", Title: "retry-policy", State: "Building - Agent", Next: waitAgent},
		{ID: "cfg", Title: "retry-config", State: "Building - Agent", Next: waitAgent},
		{ID: "cfg2", Title: "retry-backoff", State: "Building - Agent", Next: waitAgent},
		{ID: "deep", Title: "retry-jitter", State: "Building - Agent", Next: waitAgent},
		{ID: "orphan", Title: "orphan", State: "Building - Agent", Next: waitAgent},
		{ID: "skip", Title: "retry-docs-site", State: "Building - Agent", Next: waitAgent},
	}
	subPlanParents = map[string]string{"docs": "p1", "cfg": "p1", "cfg2": "p1", "deep": "cfg", "orphan": "gone", "skip": "docs"}
)

func preview(_ context.Context, id string) (string, []circleci.PlanComment, error) {
	return "# Preview " + id, nil, nil
}

// titleRows is the list's rows as listRows has them, but with only the
// pane's border and the selection marker cut, so a title keeps its indent.
func titleRows(d *driver) []string {
	d.t.Helper()
	d.send(tea.WindowSizeMsg{Width: 50, Height: 60})
	var rows []string
	for _, line := range strings.Split(d.content(), "\n") {
		line = strings.TrimRight(strings.TrimPrefix(line, "│"), "│ ")
		switch {
		case strings.HasPrefix(line, "▶ "):
			line = strings.TrimPrefix(line, "▶ ")
		case strings.HasPrefix(line, "  "):
			line = line[2:]
		}
		rows = append(rows, line)
	}
	return rows
}

// listRows is the list's rows at a size that shows every one, without the
// pane's border and the rows' indent.
func listRows(d *driver) []string {
	d.t.Helper()
	d.send(tea.WindowSizeMsg{Width: 50, Height: 60})
	var rows []string
	for _, line := range strings.Split(d.content(), "\n") {
		rows = append(rows, strings.Trim(line, "│ "))
	}
	return rows
}

func count(rows []string, row string) int {
	n := 0
	for _, r := range rows {
		if r == row {
			n++
		}
	}
	return n
}

func open(d *driver, id string) {
	d.t.Helper()
	d.send(press(tea.KeyEnter))
	msg, ok := d.next().(flowlist.OpenMsg)
	assert.Assert(d.t, ok, "enter did not emit OpenMsg")
	assert.Equal(d.t, msg.ID, id)
}

func TestNavigation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDriver(t, fixed(
			circleci.Flow{ID: "a1", Title: "Fix parser", State: "Planning - Human", Next: waitHuman},
			circleci.Flow{ID: "b2", Title: "Cache café\x1b[31m\x00", State: "Review - Agent", Next: waitAgent},
			// Listed in reverse phase order: Planning shows before Building.
			circleci.Flow{ID: "c3", Title: "Build it", State: "Building - Human", Next: waitHuman},
			circleci.Flow{ID: "d4", Title: "Plan it", State: "Planning - Human", Next: waitHuman},
		), preview)
		d.load()
		text := d.content()
		assert.Check(t, cmp.Contains(text, "▶ Fix parser"))
		assert.Check(t, cmp.Contains(text, "Cache café"))
		assert.Check(t, !strings.Contains(text, "\x00"), "control characters reached the screen")
		assert.Check(t, !strings.Contains(text, "\x1b"), "escape sequences reached the screen")
		// There is no search: / neither filters nor takes the letters after it.
		d.send(press('/'))
		d.send(press('j'))
		d.step()
		open(d, "d4")
		d.send(press('j'))
		d.step()
		open(d, "c3")
		d.send(press('j'))
		d.step()
		open(d, "b2")
		text = d.content()
		assert.Check(t, cmp.Contains(text, "Fix parser"))
		assert.Check(t, cmp.Contains(text, "▶ Cache café"))
		d.send(press('k'))
		d.step()
		open(d, "c3")
		d.send(press(tea.KeyDown))
		d.step()
		open(d, "b2")
	})
	t.Run("sub-plans", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int32
			// The second read drops retry-config and its sub-plan's parents.
			later := map[string]string{"docs": "p1", "cfg2": "p1", "orphan": "gone", "skip": "docs"}
			d := newDriver(t, fixed(subPlans...), preview, func(m flowlist.Model) flowlist.Model {
				return m.WithParents(func(context.Context) (map[string]string, error) {
					if calls.Add(1) == 1 {
						return subPlanParents, nil
					}
					return later, nil
				})
			})
			d.load()
			d.m = d.m.Select("p1")
			d.send(nil)
			d.step()
			d.send(press('j'))
			d.step()
			open(d, "cfg")
			d.send(press('j'))
			d.step()
			d.send(press(tea.KeyEnter))
			assert.Check(t, cmp.DeepEqual(d.next(), flowlist.OpenMsg{ID: "deep", Title: "retry-jitter"}))
			assert.Check(t, cmp.Contains(titleRows(d), "    retry-jitter"))
			d.refresh()
			d.step()
			open(d, "deep")
			rows := titleRows(d)
			assert.Check(t, cmp.Contains(rows, "retry-jitter"), "rows:\n%s", strings.Join(rows, "\n"))
			assert.Check(t, cmp.Contains(d.content(), "▶ retry-jitter"))
		})
	})
}

func TestHostMessages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDriver(t, fixed(), preview)
		// There is no other screen to switch to.
		for _, key := range []tea.KeyPressMsg{press('2'), press(tea.KeyTab)} {
			_, cmd := d.m.Update(key)
			assert.Check(t, cmd == nil, "%s did something", key.String())
		}
		d.send(press('n'))
		assert.Check(t, cmp.DeepEqual(d.next(), flowlist.NewMsg{}), "n did not emit NewMsg")
		for _, key := range []tea.KeyPressMsg{press('q'), press(tea.KeyEscape), {Code: 'c', Mod: tea.ModCtrl}} {
			d.send(key)
			assert.Check(t, cmp.DeepEqual(d.next(), flowlist.QuitMsg{}), "%s must ask the host to quit, not tea.Quit", key.String())
		}
	})
}

// resumeLines are every line ResumeLine can say, so a test can check the
// list itself shows none: the host shows it, in its header.
var resumeLines = []string{"No session holds", "Claimed by", "Waiting on", "chunk can't resume", "No flow"}

// noResumeLine checks the list's own view says nothing about resuming.
func noResumeLine(t *testing.T, got string) {
	t.Helper()
	for _, l := range resumeLines {
		assert.Check(t, !strings.Contains(got, l), "the list says %q:\n%s", l, got)
	}
}

func TestResume(t *testing.T) {
	building := circleci.Flow{ID: "a", Title: "Alpha", State: "Building - Agent", Next: waitAgent}
	for _, tc := range []struct {
		name     string
		flow     circleci.Flow
		blockers map[string][]planstate.Blocker
		err      error
		// kind is what R asks the host for; ResumeNone is nothing.
		kind planstate.ResumeKind
		line string
	}{
		{
			name: "building, unclaimed",
			flow: building,
			kind: planstate.ResumeBuild,
			line: "No session holds this plan · R starts a build session",
		},
		{
			name: "planning",
			flow: circleci.Flow{
				ID:    "a",
				Title: "Alpha",
				State: "Planning - Agent",
				Next:  []circleci.Transition{{Actor: "session", TaskCategory: "write-units"}},
			},
			kind: planstate.ResumePlanner,
			line: "No session holds this plan · R resumes its planner",
		},
		{
			name: "claimed",
			flow: circleci.Flow{
				ID:        "a",
				Title:     "Alpha",
				State:     "Building - Agent",
				Next:      waitAgent,
				SessionID: "bb5a623b-0000",
			},
			line: "Claimed by session bb5a623b · factory daemon release a frees it",
		},
		{
			name: "waiting on you",
			flow: circleci.Flow{ID: "a", Title: "Alpha", State: "Planning - Human", Next: waitHuman},
			line: "Waiting on you",
		},
		{
			name:     "blocked",
			flow:     building,
			blockers: map[string][]planstate.Blocker{"a": {{Title: "Retry config", State: "Building - Agent"}}},
			line:     "Waiting on Retry config",
		},
		{
			name:     "blockers done",
			flow:     building,
			blockers: map[string][]planstate.Blocker{"a": {{Title: "Retry config", State: "Done"}, {Title: "Docs", State: "ended", Outcome: "merged"}}},
			kind:     planstate.ResumeBuild,
			line:     "No session holds this plan · R starts a build session",
		},
		{
			name: "blockers fail to load",
			flow: building,
			err:  errors.New("factory is down"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := newDriver(t, fixed(tc.flow), preview, blockers(tc.blockers, tc.err))
				d.load()
				got := d.content()
				assert.Check(t, cmp.Contains(got, "Preview a"), "the preview has not loaded:\n%s", got)
				assert.Check(t, cmp.Equal(strings.Contains(got, "R resume"), tc.kind != planstate.ResumeNone), "help bar:\n%s", got)
				assert.Check(t, cmp.Equal(d.m.ResumeLine(), tc.line))
				noResumeLine(t, got)
				if tc.kind == planstate.ResumeNone {
					_, cmd := d.m.Update(press('R'))
					assert.Check(t, cmd == nil, "R did something")
					return
				}
				d.send(press('R'))
				assert.Check(t, cmp.DeepEqual(d.next(), flowlist.ResumeMsg{ID: "a", Title: "Alpha", Kind: tc.kind}))
			})
		})
	}
	t.Run("without blockers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// A host that reads no blockers still offers resume.
			d := newDriver(t, fixed(building), preview)
			d.load()
			assert.Check(t, cmp.Contains(d.content(), "R resume"))
			d.send(press('R'))
			assert.Check(t, cmp.DeepEqual(d.next(), flowlist.ResumeMsg{ID: "a", Title: "Alpha", Kind: planstate.ResumeBuild}))
		})
	})
	t.Run("before the preview loads", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			release := make(chan struct{})
			slow := func(ctx context.Context, id string) (string, []circleci.PlanComment, error) {
				select {
				case <-release:
				case <-ctx.Done():
				}
				return preview(ctx, id)
			}
			d := newDriver(t, fixed(building), slow, blockers(nil, nil))
			d.send(d.m.Init()())
			d.step() // List response starts the preview, which waits.
			got := d.content()
			assert.Check(t, !strings.Contains(got, "R resume"), "help bar:\n%s", got)
			assert.Check(t, cmp.Equal(d.m.ResumeLine(), ""))
			_, cmd := d.m.Update(press('R'))
			assert.Check(t, cmd == nil, "R did something before the blockers loaded")
			close(release)
			d.step()
			assert.Check(t, cmp.Equal(d.m.ResumeLine(), "No session holds this plan · R starts a build session"))
			noResumeLine(t, d.content())
		})
	})
	t.Run("selection moves", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			claimed := circleci.Flow{ID: "b", Title: "Beta", State: "Building - Agent", Next: waitAgent, SessionID: "cc6b734c-1111"}
			d := newDriver(t, fixed(building, claimed), preview, blockers(nil, nil))
			d.load()
			assert.Check(t, cmp.Equal(d.m.ResumeLine(), "No session holds this plan · R starts a build session"))
			d.send(press('j'))
			d.step() // Beta's preview, and its blockers.
			got := d.content()
			assert.Check(t, cmp.Contains(got, "▶ Beta"))
			assert.Check(t, cmp.Equal(d.m.ResumeLine(), "Claimed by session cc6b734c · factory daemon release b frees it"))
			noResumeLine(t, got)
			assert.Check(t, !strings.Contains(got, "R resume"), "help bar:\n%s", got)
		})
	})
}

func TestStableRefreshAndSelect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var call atomic.Int32
		a := circleci.Flow{ID: "a", Title: "Alpha", State: "Planning - Human", Next: waitHuman}
		b := circleci.Flow{ID: "b", Title: "Beta", State: "Planning - Human", Next: waitHuman}
		c := circleci.Flow{ID: "c", Title: "Gamma", State: "Planning - Human", Next: waitHuman}
		zeta := circleci.Flow{ID: "z", Title: "Zeta plan", State: "Zeta - Human", Next: waitHuman}
		alphaPhase := circleci.Flow{ID: "al", Title: "Alpha plan", State: "Alpha - Human", Next: waitHuman}
		zetaToo := circleci.Flow{ID: "z2", Title: "Zeta too", State: " zeta - Human", Next: waitHuman}
		d := newDriver(t, func(context.Context) ([]circleci.Flow, error) {
			if call.Add(1) == 1 {
				return []circleci.Flow{a, b, zeta, alphaPhase}, nil
			}
			return []circleci.Flow{c, b, a, alphaPhase, zeta, zetaToo}, nil
		}, preview)
		d.load()
		d.send(press('j'))
		d.step()
		d.refresh()
		d.step()
		text := d.content()
		assert.Check(t, cmp.Contains(text, "▶ Beta"))
		alpha, beta, gamma := strings.Index(text, "Alpha"), strings.Index(text, "Beta"), strings.Index(text, "Gamma")
		assert.Assert(t, alpha < beta && beta < gamma, "refresh reordered existing rows:\n%s", text)
		// Unknown phases keep the order the list first saw them in, and a
		// phase spelled differently joins its sub-group.
		rows := listRows(d)
		zetaRow, zetaTooRow, alphaRow := slices.Index(rows, "Zeta plan"), slices.Index(rows, "Zeta too"), slices.Index(rows, "Alpha plan")
		assert.Check(t, zetaRow >= 0 && zetaTooRow == zetaRow+2 && alphaRow == zetaTooRow+3, "wrong phase order:\n%s", strings.Join(rows, "\n"))
		assert.Check(t, cmp.Equal(count(rows, "Zeta"), 1), "want one Zeta sub-heading:\n%s", strings.Join(rows, "\n"))
		assert.Check(t, cmp.Equal(rows[zetaRow-1], "Zeta"))
		assert.Check(t, cmp.Equal(rows[alphaRow-1], "Alpha"))
		open(d, "b")
		d.m = d.m.Select("c")
		d.send(nil)
		d.step()
		open(d, "c")
		d.m = d.m.Select("missing")
		open(d, "c")
		d.m = d.m.Select("b")
		d.send(nil)
		d.step()
		text = d.content()
		assert.Check(t, cmp.Contains(text, "▶ Beta"))
		assert.Check(t, cmp.Contains(text, "Gamma"))
	})
}

func TestLatePreviewCannotOverwriteSelection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		releaseA := make(chan struct{})
		d := newDriver(t, fixed(
			circleci.Flow{ID: "a", Title: "Alpha"}, circleci.Flow{ID: "b", Title: "Beta"},
		), func(ctx context.Context, id string) (string, []circleci.PlanComment, error) {
			if id == "a" {
				select {
				case <-releaseA:
				case <-ctx.Done():
					return "", nil, ctx.Err()
				}
			}
			return "UNIQUE PREVIEW " + id, nil, nil
		})
		d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
		d.send(d.m.Init()())
		d.step()
		d.send(press('j'))
		d.step()
		assert.Check(t, cmp.Contains(d.content(), "UNIQUE PREVIEW b"))
		close(releaseA)
		d.step()
		text := d.content()
		assert.Check(t, cmp.Contains(text, "UNIQUE PREVIEW b"))
		assert.Check(t, !strings.Contains(text, "UNIQUE PREVIEW a"), "a late preview replaced the selected flow's:\n%s", text)
	})
}

func TestLatePreviewForSameIDCannotOverwriteNewRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		started := make(chan struct{})
		var calls atomic.Int32
		d := newDriver(t, fixed(
			circleci.Flow{ID: "a", Title: "Alpha"}, circleci.Flow{ID: "b", Title: "Beta"},
		), func(ctx context.Context, id string) (string, []circleci.PlanComment, error) {
			if id == "a" && calls.Add(1) == 1 {
				close(started)
				select {
				case <-release:
					return "OBSOLETE CONTENT", nil, nil
				case <-ctx.Done():
					return "", nil, ctx.Err()
				}
			}
			return "CURRENT CONTENT " + id, nil, nil
		})
		d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
		d.send(d.m.Init()())
		d.step()
		<-started
		d.send(press('j'))
		d.step()
		d.send(press('k'))
		d.step()
		assert.Check(t, cmp.Contains(d.content(), "CURRENT CONTENT a"))
		close(release)
		d.step()
		text := d.content()
		assert.Check(t, cmp.Contains(text, "CURRENT CONTENT a"))
		assert.Check(t, !strings.Contains(text, "OBSOLETE CONTENT"), "an obsolete preview replaced the current one:\n%s", text)
	})
}

func TestEmptyErrorsAndStaleData(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newDriver(t, fixed(), preview)
			d.send(d.m.Init()())
			d.step()
			text := d.content()
			assert.Check(t, cmp.Contains(text, "no open flows"))
			assert.Check(t, cmp.Contains(text, "n starts one"))
		})
	})
	t.Run("loading", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newDriver(t, fixed(), preview)
			assert.Check(t, cmp.Contains(d.content(), "loading flows…"))
		})
	})
	t.Run("initial error recovers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int32
			d := newDriver(t, func(context.Context) ([]circleci.Flow, error) {
				if calls.Add(1) == 1 {
					return nil, errors.New("offline")
				}
				return []circleci.Flow{{ID: "a", Title: "Retained"}}, nil
			}, preview)
			d.send(d.m.Init()())
			d.step()
			text := d.content()
			for _, want := range []string{"offline", "flows unavailable", "retrying"} {
				assert.Check(t, cmp.Contains(text, want))
			}
			d.refresh()
			d.step()
			text = d.content()
			assert.Check(t, cmp.Contains(text, "Retained"))
			assert.Check(t, !strings.Contains(text, "offline"), "the error outlived a successful list:\n%s", text)
		})
	})
	t.Run("parents error recovers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int32
			d := newDriver(t, fixed(subPlans...), preview, func(m flowlist.Model) flowlist.Model {
				return m.WithParents(func(context.Context) (map[string]string, error) {
					if calls.Add(1) == 1 {
						return nil, errors.New("plans\noffline")
					}
					return subPlanParents, nil
				})
			})
			d.load()
			d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
			lines := strings.Split(d.content(), "\n")
			assert.Check(t, cmp.Contains(lines[len(lines)-1], "sub-plans unavailable: plans offline"))
			assert.Check(t, !strings.Contains(d.content(), "↳"), "a hint shows without parents:\n%s", d.content())
			d.refresh()
			d.step()
			text := d.content()
			assert.Check(t, !strings.Contains(text, "sub-plans unavailable"), "the error outlived a successful read:\n%s", text)
			assert.Check(t, cmp.Contains(text, "↳ retry-policy"))
		})
	})
	t.Run("list error skips parents", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int32
			d := newDriver(t, func(context.Context) ([]circleci.Flow, error) {
				return nil, errors.New("offline")
			}, preview, func(m flowlist.Model) flowlist.Model {
				return m.WithParents(func(context.Context) (map[string]string, error) {
					calls.Add(1)
					return subPlanParents, nil
				})
			})
			d.send(d.m.Init()())
			d.step()
			assert.Check(t, cmp.Equal(calls.Load(), int32(0)))
			text := d.content()
			assert.Check(t, cmp.Contains(text, "flows unavailable"))
			assert.Check(t, !strings.Contains(text, "sub-plans unavailable"), "footer:\n%s", text)
		})
	})
	t.Run("list and preview retain success", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var lists, previews atomic.Int32
			d := newDriver(t, func(context.Context) ([]circleci.Flow, error) {
				if lists.Add(1) == 2 {
					return nil, errors.New("list offline")
				}
				return []circleci.Flow{{ID: "a", Title: "Retained"}}, nil
			}, func(context.Context, string) (string, []circleci.PlanComment, error) {
				if previews.Add(1) > 1 {
					return "", nil, errors.New("preview offline")
				}
				return "Retained preview", nil, nil
			})
			d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
			d.load()
			d.refresh()
			text := d.content()
			for _, want := range []string{"Retained", "Retained preview", "list offline", "stale"} {
				assert.Check(t, cmp.Contains(text, want))
			}
			d.refresh()
			d.step()
			text = d.content()
			for _, want := range []string{"Retained preview", "preview offline", "stale"} {
				assert.Check(t, cmp.Contains(text, want))
			}
		})
	})
	t.Run("revisit keeps stale cache", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newDriver(t, fixed(circleci.Flow{ID: "a", Title: "Alpha"}, circleci.Flow{ID: "b", Title: "Beta"}), replies(map[string][]reply{
				"a": {{text: "# Preview a"}, {err: errors.New("preview offline")}},
			}))
			d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
			d.load()
			d.send(press('j'))
			d.step()
			d.send(press('k'))
			d.step()
			text := d.content()
			for _, want := range []string{"Preview a", "Preview error (stale)", "preview offline"} {
				assert.Check(t, cmp.Contains(text, want))
			}
		})
	})
}

// reply is one answer of a fake preview.
type reply struct {
	text     string
	comments []circleci.PlanComment
	err      error
}

// replies is a preview that answers each flow's calls in turn from byID,
// repeating the last answer, or "# Preview <id>" for a flow it has none for.
func replies(byID map[string][]reply) func(context.Context, string) (string, []circleci.PlanComment, error) {
	var mu sync.Mutex
	return func(_ context.Context, id string) (string, []circleci.PlanComment, error) {
		mu.Lock()
		defer mu.Unlock()
		rs := byID[id]
		if len(rs) == 0 {
			return "# Preview " + id, nil, nil
		}
		if len(rs) > 1 {
			byID[id] = rs[1:]
		}
		return rs[0].text, rs[0].comments, rs[0].err
	}
}

// renderCall is the width and style of one render.
type renderCall struct {
	width int
	dark  bool
}

// counting has m render with a fake that waits for gate to close, when it
// is not nil, records each call in calls, and renders text as one line
// naming the width and style it was given.
func counting(mu *sync.Mutex, calls *[]renderCall, gate <-chan struct{}) func(flowlist.Model) flowlist.Model {
	return func(m flowlist.Model) flowlist.Model {
		return flowlist.WithRender(m, func(text string, width int, dark bool) []string {
			if gate != nil {
				<-gate
			}
			mu.Lock()
			defer mu.Unlock()
			*calls = append(*calls, renderCall{width, dark})
			return []string{fmt.Sprintf("%s (w=%d dark=%t)", text, width, dark)}
		})
	}
}

func TestPreviewRenderCache(t *testing.T) {
	for _, tc := range []struct {
		name      string
		act       func(d *driver)
		wantCalls int
		wantLast  renderCall
	}{
		{
			name: "redraws reuse the render",
			act: func(d *driver) {
				for range 3 {
					d.content()
				}
				d.refresh()
				d.step()           // Same preview text again.
				d.send(press('k')) // Already on the first row.
			},
			wantCalls: 1,
			wantLast:  renderCall{width: 120 - theme.ListWidth - 9, dark: true},
		},
		{
			name: "resize re-renders",
			act: func(d *driver) {
				d.send(tea.WindowSizeMsg{Width: 160, Height: 35})
				d.step() // The render.
			},
			wantCalls: 2,
			wantLast:  renderCall{width: 160 - theme.ListWidth - 9, dark: true},
		},
		{
			name: "background re-renders",
			act: func(d *driver) {
				d.send(tea.BackgroundColorMsg{Color: color.White})
				d.step() // The render.
			},
			wantCalls: 2,
			wantLast:  renderCall{width: 120 - theme.ListWidth - 9},
		},
		{
			name: "revisit at same size does not render",
			act: func(d *driver) {
				d.send(press('j'))
				d.step()
				d.send(press('k'))
				d.step() // a's refetch, with the same text.
			},
			wantCalls: 2,
			wantLast:  renderCall{width: 120 - theme.ListWidth - 9, dark: true},
		},
		{
			name: "revisit after resize renders once",
			act: func(d *driver) {
				d.send(press('j'))
				d.step()
				d.send(tea.WindowSizeMsg{Width: 160, Height: 35})
				d.step() // b's render at 160.
				d.send(press('k'))
				d.step() // a's render at 160 and its refetch, in either order.
				d.step()
			},
			wantCalls: 4,
			wantLast:  renderCall{width: 160 - theme.ListWidth - 9, dark: true},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var mu sync.Mutex
				var calls []renderCall
				d := newDriver(t, fixed(circleci.Flow{ID: "a", Title: "Alpha"}, circleci.Flow{ID: "b", Title: "Beta"}), preview, counting(&mu, &calls, nil))
				d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
				d.load()
				tc.act(d)
				mu.Lock()
				defer mu.Unlock()
				assert.Check(t, cmp.Len(calls, tc.wantCalls))
				if len(calls) > 0 {
					assert.Check(t, cmp.Equal(calls[len(calls)-1], tc.wantLast))
				}
			})
		})
	}
	// A render for an old size or style must not reach the screen.
	for _, tc := range []struct {
		name   string
		change tea.Msg
		want   string
	}{
		{
			name:   "stale async render dropped on resize",
			change: tea.WindowSizeMsg{Width: 160, Height: 35},
			want:   fmt.Sprintf("w=%d dark=true", 160-theme.ListWidth-9),
		},
		{
			name:   "stale async render dropped on background",
			change: tea.BackgroundColorMsg{Color: color.White},
			want:   fmt.Sprintf("w=%d dark=false", 120-theme.ListWidth-9),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				old := fmt.Sprintf("w=%d dark=true", 120-theme.ListWidth-9)
				release := make(chan struct{})
				var mu sync.Mutex
				var calls []renderCall
				d := newDriver(t, fixed(circleci.Flow{ID: "a", Title: "Alpha"}), preview, counting(&mu, &calls, release))
				d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
				d.send(d.m.Init()())
				d.step()        // The list starts a's fetch, which renders and blocks.
				synctest.Wait() // a's render has started.
				d.send(tc.change)
				close(release)
				d.step() // The fetch, rendered for the old pane.
				text := d.content()
				assert.Check(t, !strings.Contains(text, old), "a render for the old pane shows:\n%s", text)
				d.step() // The render for the new pane.
				text = d.content()
				assert.Check(t, cmp.Contains(text, "# Preview a ("+tc.want+")"))
				assert.Check(t, !strings.Contains(text, old), "a render for the old pane shows:\n%s", text)
				mu.Lock()
				defer mu.Unlock()
				assert.Check(t, cmp.Len(calls, 2))
			})
		})
	}
}

func TestPreviewCache(t *testing.T) {
	ab := fixed(circleci.Flow{ID: "a", Title: "Alpha"}, circleci.Flow{ID: "b", Title: "Beta"})
	t.Run("revisit shows cache on same frame", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newDriver(t, ab, preview)
			d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
			d.load()
			d.send(press('j'))
			d.step()
			d.send(press('k'))
			text := d.content()
			assert.Check(t, cmp.Contains(text, "Preview a"))
			assert.Check(t, !strings.Contains(text, "Loading preview…"), "a revisit waits for its fetch:\n%s", text)
		})
	})
	t.Run("refetch replaces changed text", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newDriver(t, ab, replies(map[string][]reply{"a": {{text: "# Preview a"}, {text: "# Preview a v2"}}}))
			d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
			d.load()
			d.send(press('j'))
			d.step()
			d.send(press('k'))
			d.step()
			assert.Check(t, cmp.Contains(d.content(), "a v2"))
		})
	})
	t.Run("refetch replaces changed comments", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newDriver(t, ab, replies(map[string][]reply{"a": {
				{text: "# Preview a"},
				{text: "# Preview a", comments: []circleci.PlanComment{{Body: "Fresh note"}}},
			}}))
			d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
			d.load()
			text := d.content()
			assert.Check(t, !strings.Contains(text, "Open comments"), "content:\n%s", text)
			d.send(press('j'))
			d.step()
			d.send(press('k'))
			d.step()
			text = d.content()
			assert.Check(t, cmp.Contains(text, "Open comments"))
			assert.Check(t, cmp.Contains(text, "Fresh note"))
		})
	})
	t.Run("new flow shows loading", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newDriver(t, ab, func(ctx context.Context, id string) (string, []circleci.PlanComment, error) {
				if id == "b" {
					<-ctx.Done()
					return "", nil, ctx.Err()
				}
				return preview(ctx, id)
			})
			d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
			d.load()
			d.send(press('j'))
			text := d.content()
			assert.Check(t, cmp.Contains(text, "Loading preview…"))
			assert.Check(t, !strings.Contains(text, "Preview a"), "the previous flow's preview shows:\n%s", text)
		})
	})
	t.Run("pruned on list", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			a, b := circleci.Flow{ID: "a", Title: "Alpha"}, circleci.Flow{ID: "b", Title: "Beta"}
			var lists atomic.Int32
			d := newDriver(t, func(context.Context) ([]circleci.Flow, error) {
				if lists.Add(1) == 2 {
					return []circleci.Flow{a}, nil
				}
				return []circleci.Flow{a, b}, nil
			}, replies(map[string][]reply{"b": {{text: "# Old b"}, {text: "# New b"}}}))
			d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
			d.load()
			d.send(press('j'))
			d.step()
			assert.Check(t, cmp.Contains(d.content(), "Old b"))
			d.refresh() // b leaves the list, so a is selected again.
			d.step()
			d.refresh() // b comes back.
			d.step()
			d.send(press('j'))
			text := d.content()
			assert.Check(t, cmp.Contains(text, "Loading preview…"))
			assert.Check(t, !strings.Contains(text, "Old b"), "a pruned flow's preview shows:\n%s", text)
			d.step()
			assert.Check(t, cmp.Contains(d.content(), "New b"))
		})
	})
	t.Run("empty text", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var mu sync.Mutex
			var calls []renderCall
			d := newDriver(t, ab, replies(map[string][]reply{"a": {{text: " \n"}}, "b": {{text: ""}}}), counting(&mu, &calls, nil))
			d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
			d.load()
			assert.Check(t, cmp.Contains(d.content(), "No plan text yet."))
			d.send(press('j'))
			d.step()
			d.send(press('k'))
			assert.Check(t, cmp.Contains(d.content(), "No plan text yet."))
			d.step()
			assert.Check(t, cmp.Contains(d.content(), "No plan text yet."))
			mu.Lock()
			defer mu.Unlock()
			assert.Check(t, cmp.Len(calls, 0))
		})
	})
}

func TestPhase(t *testing.T) {
	human := []circleci.Transition{{Actor: "human", Event: circleci.AddressComments}}
	review := []circleci.Transition{{Actor: "session", TaskCategory: "review-plan", IsDispatched: true}}
	for _, tc := range []struct {
		name string
		flow circleci.Flow
		want string
	}{
		{
			name: "bare word",
			flow: circleci.Flow{State: "Planning"},
			want: "",
		},
		{
			name: "any case",
			flow: circleci.Flow{State: "planning - human", Next: human},
			want: "planning",
		},
		{
			name: "empty",
			flow: circleci.Flow{State: "", Next: human},
			want: "",
		},
		{
			name: "unknown phase",
			flow: circleci.Flow{State: "Integrating - Agent", Next: review},
			want: "Integrating",
		},
		{
			name: "new name",
			flow: circleci.Flow{State: "refine-units", Next: review},
			want: "Planning",
		},
		{
			name: "held, next empty",
			flow: circleci.Flow{State: "build"},
			want: "",
		},
		{
			name: "not started",
			flow: circleci.Flow{State: "init", Next: human},
			want: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.Equal(flowlist.Phase(tc.flow), tc.want))
		})
	}
}

func TestCapitalize(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "empty",
		},
		{
			name: "lower word",
			in:   "planning",
			want: "Planning",
		},
		{
			name: "hyphenated",
			in:   "standing-by",
			want: "Standing-by",
		},
		{
			name: "later words keep case",
			in:   "custom phase",
			want: "Custom phase",
		},
		{
			name: "already capitalized",
			in:   "Agent Ready",
			want: "Agent Ready",
		},
		{
			name: "digit first",
			in:   "2nd pass",
			want: "2nd pass",
		},
		{
			name: "punctuation first",
			in:   "-x",
			want: "-x",
		},
		{
			name: "uncased script",
			in:   "界",
			want: "界",
		},
		{
			name: "multi-byte letter",
			in:   "école",
			want: "École",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.Equal(flowlist.Capitalize(tc.in), tc.want))
		})
	}
}

func TestGroupsAndRows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		human := []circleci.Transition{{Actor: "human", Event: circleci.AddressComments}}
		session := func(task string) []circleci.Transition {
			return []circleci.Transition{{Actor: "session", TaskCategory: task, IsDispatched: true}}
		}
		rr := circleci.Transition{Actor: "session", Event: "request-revision"}
		flows := []circleci.Flow{
			{ID: "other", Title: "Other plan", State: "Custom exact state"},
			{ID: "bot", Title: "Bot plan", State: "Integrating - Bot"},
			{ID: "new", Title: "New plan", State: "Unprioritized"},
			{ID: "agent", Title: "Agent plan", State: "Planning - Agent", Next: session("review-plan")},
			{ID: "untitled", State: "Building - Agent", Next: session("build-unit")},
			{ID: "built", Title: "Built", State: "Building - Human", Task: &circleci.FlowTask{ID: "t"}, Next: human},
			{
				ID: "human", Title: "Planned", State: "Planning - Human", UpdatedAt: now,
				SessionID: "s1", Task: &circleci.FlowTask{ID: "t1", Category: "build-unit"},
				ApplicableEvents: []circleci.FlowEvent{circleci.AddressComments, circleci.ResolveConflicts},
				Next: []circleci.Transition{
					{Actor: "human", Event: circleci.AddressComments},
					{Actor: "human", Event: circleci.ResolveConflicts},
				},
			},
			{ID: "zeta", Title: "Zeta", State: "Zeta - Human", Next: human},
			{ID: "alpha", Title: "Alpha", State: "Alpha - Human", Next: human},
			{ID: "planned-too", Title: "Planned too", State: "planning - human", Next: human},
			{ID: "long-state", Title: "Long state", State: "Custom " + strings.Repeat("state ", 20)},
			{ID: "blank-state", Title: "Blank state", State: ""},
			{ID: "refined", Title: "Refined", State: "refine-units", Next: human},
			{ID: "building", Title: "Building new", State: "build", Next: session("build-unit")},
			{ID: "held", Title: "Held", State: "build"},
			{ID: "ended", Title: "Ended", State: "ended", Phase: circleci.PhaseEnded, Outcome: "canceled"},
			{ID: "merging", Title: "Merging", State: "Integrating - Human", Next: []circleci.Transition{{Actor: "human", Action: "merge-pr"}}},
			{ID: "merged", Title: "Merged", State: "ended", Phase: circleci.PhaseEnded, Outcome: "merged"},
			{ID: "abandon-only", Title: "Abandon only", State: "refine-units", Next: []circleci.Transition{{Actor: "human", Event: circleci.Abandon}}},
			{ID: "asked", Title: "Asked", State: "Planning - Human", Next: []circleci.Transition{{Actor: "human", Event: circleci.AddressComments}, rr}},
			{ID: "building-rr", Title: "Building beside", State: "Building - Agent", Next: append(session("build-unit"), rr)},
			{ID: "rr-only", Title: "Revision only", State: "refine-units", Next: []circleci.Transition{rr, {Actor: "human", Event: circleci.Abandon}}},
		}
		d := newDriver(t, fixed(flows...), func(context.Context, string) (string, []circleci.PlanComment, error) {
			return "# Markdown heading\n\n**Plan prose**\n\n" + strings.Repeat("界", 150), []circleci.PlanComment{
				{Body: "Please clarify", QuotedText: "Plan prose"},
				{Body: "RESOLVED SECRET", ResolvedAt: &now},
			}, nil
		})
		d.load()
		rows := listRows(d)
		all := strings.Join(rows, "\n")
		for _, want := range []string{"flows", "Needs your review  8", "With agent  4", "Not started  1", "Other  9", "Custom exact state", "Integrating - bot", "Untitled plan"} {
			assert.Check(t, cmp.Contains(all, want))
		}
		index := func(row string) int {
			t.Helper()
			for i := len(rows) - 1; i >= 0; i-- {
				// The last match, so a title is not taken for the
				// sub-heading of the same name above it.
				if strings.TrimPrefix(rows[i], "▶ ") == row {
					return i
				}
			}
			t.Fatalf("no row %q:\n%s", row, all)
			return -1
		}
		order := []string{"Planned", "Built", "Zeta", "Alpha", "Agent plan", "New plan", "Other plan", "Bot plan"}
		for i := 1; i < len(order); i++ {
			assert.Check(t, index(order[i-1]) < index(order[i]), "%q is not before %q:\n%s", order[i-1], order[i], all)
		}
		// A phase spelled differently shares its sub-heading.
		planned := index("Planned")
		assert.Check(t, cmp.Equal(rows[planned+2], "Planned too"), "rows:\n%s", all)
		review := rows[index("Needs your review  8"):index("With agent  4")]
		assert.Check(t, cmp.Equal(count(review, "Planning"), 1), "want one Planning sub-heading:\n%s", all)
		assert.Check(t, cmp.Equal(count(review, "Building"), 1), "want one Building sub-heading:\n%s", all)
		// A new-spelled state lists by its next, under its old phase.
		review = rows[index("Needs your review  8"):index("With agent  4")]
		assert.Check(t, slices.Contains(review, "Refined"), "rows:\n%s", all)
		assert.Check(t, slices.Contains(review, "Merging"), "rows:\n%s", all)
		agent := rows[index("With agent  4"):index("Not started  1")]
		assert.Check(t, slices.Contains(agent, "Building new"), "rows:\n%s", all)
		// request-revision, which factory lists in every live state, is
		// not a move factory waits on.
		review = rows[index("Needs your review  8"):index("With agent  4")]
		assert.Check(t, slices.Contains(review, "Asked"), "rows:\n%s", all)
		agent = rows[index("With agent  4"):index("Not started  1")]
		assert.Check(t, slices.Contains(agent, "Building beside"), "rows:\n%s", all)
		assert.Check(t, index("Revision only") > index("Other  9"), "rows:\n%s", all)
		// A held flow, its next empty, and an ended one list in Other,
		// labelled by phase and by outcome.
		assert.Check(t, cmp.Equal(rows[index("Held")+1], "Building"), "rows:\n%s", all)
		assert.Check(t, cmp.Equal(rows[index("Ended")+1], "Canceled"), "rows:\n%s", all)
		assert.Check(t, cmp.Equal(rows[index("Merged")+1], "Done"), "rows:\n%s", all)
		// Only the escape hatches left: nothing for the developer to do.
		assert.Check(t, index("Abandon only") > index("Other  9"), "rows:\n%s", all)
		assert.Check(t, cmp.Equal(rows[index("Abandon only")+1], "Planning"), "rows:\n%s", all)
		// Each sub-heading is directly above its first title.
		for title, heading := range map[string]string{"Planned": "Planning", "Built": "Building", "Zeta": "Zeta", "Alpha": "Alpha"} {
			assert.Check(t, cmp.Equal(rows[index(title)-1], heading), "rows:\n%s", all)
		}
		// Not started and Other have no sub-headings: only titles and details.
		// Only Other names the state, since no heading does; an empty one is unknown.
		other := index("Other  9")
		assert.Check(t, cmp.DeepEqual(rows[other+1:other+5], []string{"Other plan", "Custom exact state", "Bot plan", "Integrating - bot"}), "rows:\n%s", all)
		assert.Check(t, cmp.Equal(rows[index("Blank state")+1], "Unknown"), "rows:\n%s", all)
		longState := rows[index("Long state")+1]
		assert.Check(t, strings.HasPrefix(longState, "Custom state state"), "rows:\n%s", all)
		assert.Check(t, strings.HasSuffix(longState, "…"), "a long state is not truncated:\n%s", all)
		notStarted := index("Not started  1")
		assert.Check(t, cmp.DeepEqual(rows[notStarted+1:notStarted+3], []string{"New plan", ""}), "rows:\n%s", all)
		// The row details: a conflict marker first, then task category,
		// claimed and updated-ago, each only when there is one.
		// The details start the row; only the sub-heading above says the phase.
		assert.Check(t, strings.HasPrefix(rows[planned+1], "⚠ · build-unit"), "rows:\n%s", all)
		assert.Check(t, cmp.Contains(rows[planned+1], "build-unit"))
		assert.Check(t, cmp.Contains(rows[planned+1], "claimed"))
		assert.Check(t, cmp.Contains(rows[planned+1], "just now"))
		assert.Check(t, cmp.Equal(rows[index("Built")+1], ""), "a flow with no details keeps a blank second row:\n%s", all)
		assert.Check(t, !strings.Contains(all, "ago"), "a zero UpdatedAt shows an age:\n%s", all)

		for _, size := range []tea.WindowSizeMsg{{Width: 50, Height: 20}, {Width: 80, Height: 24}, {Width: 120, Height: 35}, {Width: 1, Height: 1}, {Width: 100, Height: 2}} {
			for _, bg := range []color.Color{color.White, color.Black} {
				d.send(size)
				d.send(tea.BackgroundColorMsg{Color: bg})
				d.m = d.m.WithNote("Unsent draft: " + strings.Repeat("界", 200) + "\nunsafe\x1b[2J")
				text := d.content()
				lines := strings.Split(text, "\n")
				assert.Assert(t, len(lines) <= size.Height, "height %d exceeds %d", len(lines), size.Height)
				for _, line := range lines {
					assert.Assert(t, ansi.StringWidth(line) <= size.Width, "width %d exceeds %d: %q", ansi.StringWidth(line), size.Width, line)
				}
				// The help bar, then the host's note under it; why the
				// claimed flow can't be resumed is the host's to show.
				noResumeLine(t, text)
				assert.Check(t, cmp.Equal(d.m.ResumeLine(), "Claimed by session s1 · factory daemon release human frees it"))
				if size.Width == 50 {
					for _, not := range []string{"preview", "Markdown heading", "Please clarify"} {
						assert.Check(t, !strings.Contains(text, not), "%q shows at width 50:\n%s", not, text)
					}
					assert.Check(t, cmp.Contains(lines[len(lines)-2], "n new plan"))
					assert.Check(t, !strings.Contains(text, "runs"), "the help names runs:\n%s", text)
				}
				if size.Width >= 80 && size.Height > 2 {
					for _, want := range []string{"preview · human", "Open comments (not plan text)", "> Please clarify", "Markdown heading", "Plan prose", "⚠"} {
						assert.Check(t, cmp.Contains(text, want))
					}
					for _, not := range []string{"RESOLVED SECRET", "# Markdown", "**Plan prose**"} {
						assert.Check(t, !strings.Contains(text, not), "%q shows in the preview:\n%s", not, text)
					}
					assert.Check(t, cmp.Contains(lines[len(lines)-2], "q quit"))
					assert.Check(t, cmp.Contains(lines[len(lines)-1], "Unsent draft"))
				}
			}
		}
	})
}

func TestSubHeadingCase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDriver(t, fixed(
			circleci.Flow{ID: "p", Title: "Lower planning", State: "planning - human", Next: waitHuman},
			circleci.Flow{ID: "p2", Title: "Upper planning", State: "Planning - Human", Next: waitHuman},
			circleci.Flow{ID: "s", Title: "Parent", State: "stand-by", Next: waitHuman},
			circleci.Flow{ID: "c", Title: "Custom", State: "custom phase - human", Next: waitHuman},
			circleci.Flow{ID: "r", Title: "Ready", State: "ready", Next: waitHuman},
			circleci.Flow{ID: "n", Title: "Numbered", State: "2nd pass - human", Next: waitHuman},
		), preview)
		d.load()
		rows := listRows(d)
		all := strings.Join(rows, "\n")
		// Each sub-heading is directly above its first title, its first
		// letter upper-cased and the rest as that flow spells it.
		for title, heading := range map[string]string{
			"Lower planning": "Planning",
			"Parent":         "Standing-by",
			"Custom":         "Custom phase",
			"Ready":          "Agent Ready",
			"Numbered":       "2nd pass",
		} {
			// The selected flow's row keeps its marker in listRows.
			i := slices.IndexFunc(rows, func(r string) bool { return strings.TrimPrefix(r, "▶ ") == title })
			assert.Assert(t, i > 0, "no row %q:\n%s", title, all)
			assert.Check(t, cmp.Equal(rows[i-1], heading), "rows:\n%s", all)
		}
		// Spellings that differ only in case still share one sub-heading.
		assert.Check(t, cmp.Equal(count(rows, "Planning"), 1), "rows:\n%s", all)
		assert.Check(t, cmp.Equal(count(rows, "planning"), 0), "rows:\n%s", all)
	})
}

func TestSubPlanRows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDriver(t, fixed(subPlans...), preview, parents(subPlanParents))
		d.load()
		rows, titles := listRows(d), titleRows(d)
		all := strings.Join(rows, "\n")
		agent := slices.Index(rows, "With agent  6")
		assert.Assert(t, agent >= 0 && rows[agent+1] == "Building", "rows:\n%s", all)
		// A sub-plan counts once, in its own section, whether or not its parent is there.
		assert.Check(t, cmp.Equal(count(rows, "Needs your review  2"), 1), "rows:\n%s", all)
		var building []string
		for i := agent + 2; i < len(titles) && titles[i] != ""; i += 2 {
			building = append(building, titles[i])
		}
		// Siblings stay in order, a grandchild is indented twice, and a
		// parent in another sub-group or not listed means no indent.
		assert.Check(t, cmp.DeepEqual(building, []string{"retry-policy", "  retry-config", "    retry-jitter", "  retry-backoff", "orphan", "retry-docs-site"}), "rows:\n%s", strings.Join(titles, "\n"))
		detail := func(title string) string {
			t.Helper()
			i := slices.IndexFunc(rows, func(r string) bool { return strings.TrimPrefix(r, "▶ ") == title })
			assert.Assert(t, i >= 0, "no row %q:\n%s", title, all)
			return rows[i+1]
		}
		assert.Check(t, cmp.Contains(detail("retry-docs-site"), "↳ retry-docs"))
		assert.Check(t, cmp.Contains(detail("retry-docs"), "↳ retry-policy"))
		assert.Check(t, strings.HasPrefix(detail("retry-config"), "↳ retry-policy"), "rows:\n%s", all)
		assert.Check(t, !strings.Contains(detail("orphan"), "↳"), "rows:\n%s", all)
		// A sub-plan stays in its own group, unindented, in stable order.
		planning := slices.Index(rows, "Planning")
		assert.Check(t, cmp.DeepEqual([]string{titles[planning+1], titles[planning+3]}, []string{"retry-docs", "other"}), "rows:\n%s", all)

		plain := newDriver(t, fixed(subPlans...), preview)
		plain.load()
		without := newDriver(t, fixed(subPlans...), preview, parents(nil))
		without.load()
		assert.Check(t, cmp.DeepEqual(listRows(without), listRows(plain)))

		// Indented rows and long hints fit every size.
		long := append(slices.Clone(subPlans), circleci.Flow{ID: "long", Title: strings.Repeat("界", 80), State: "Building - Agent", Next: waitAgent})
		withLong := maps.Clone(subPlanParents)
		withLong["long"] = "deep"
		d = newDriver(t, fixed(long...), preview, parents(withLong))
		d.load()
		for _, size := range []tea.WindowSizeMsg{{Width: 50, Height: 20}, {Width: 80, Height: 24}, {Width: 120, Height: 35}, {Width: 1, Height: 1}, {Width: 100, Height: 2}} {
			for _, bg := range []color.Color{color.White, color.Black} {
				d.send(size)
				d.send(tea.BackgroundColorMsg{Color: bg})
				lines := strings.Split(d.content(), "\n")
				assert.Assert(t, len(lines) <= size.Height, "height %d exceeds %d", len(lines), size.Height)
				for _, line := range lines {
					assert.Assert(t, ansi.StringWidth(line) <= size.Width, "width %d exceeds %d: %q", ansi.StringWidth(line), size.Width, line)
				}
			}
		}
	})
}

func TestLongListKeepsSelectedRowVisible(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var flows []circleci.Flow
		for i := range 80 {
			flows = append(flows, circleci.Flow{ID: fmt.Sprintf("id-%d", i), Title: fmt.Sprintf("Title-%02d", i), State: "Planning - Human", Next: waitHuman})
		}
		d := newDriver(t, fixed(flows...), preview)
		d.load()
		d.m = d.m.Select("id-79")
		d.send(nil)
		d.step()
		for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 120, Height: 35}} {
			d.send(size)
			if size.Width != 80 {
				d.step() // The preview's render at the new width.
			}
			text := d.content()
			assert.Check(t, cmp.Contains(text, "▶ Title-79"))
			assert.Check(t, !strings.Contains(text, "Title-00"), "the list did not scroll to the selected row:\n%s", text)
			open(d, "id-79")
		}
		// The selected flow's blank detail row stays in view, directly above the
		// pane's bottom border.
		d.send(tea.WindowSizeMsg{Width: 50, Height: 24})
		lines := strings.Split(d.content(), "\n")
		i := slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, "▶ Title-79") })
		assert.Assert(t, i >= 0 && i+2 < len(lines), "lines:\n%s", strings.Join(lines, "\n"))
		assert.Check(t, cmp.Equal(strings.Trim(lines[i+1], "│ "), ""), "lines:\n%s", strings.Join(lines, "\n"))
		assert.Check(t, strings.HasPrefix(lines[i+2], "╰"), "the detail row scrolled off:\n%s", strings.Join(lines, "\n"))
	})
}

func TestRefreshCoalescesAndCancellationStopsRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		started := make(chan struct{})
		d := newDriver(t, func(ctx context.Context) ([]circleci.Flow, error) {
			calls.Add(1)
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		}, preview)
		d.send(d.m.Init()())
		<-started
		for range 20 {
			d.send(d.m.Refresh()())
		}
		assert.Equal(t, calls.Load(), int32(1), "overlapping list requests")
		d.cancel()
		assert.Check(t, cmp.Nil(d.m.Refresh()()), "Refresh after cancellation returned a message")
		_, cmd := d.m.Update(press('n'))
		assert.Check(t, cmd == nil, "Update returned a command after cancellation")
	})
}

func TestPollRefreshesAfterFiveSecondsAndStopsOnCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		d := newDriver(t, func(context.Context) ([]circleci.Flow, error) {
			calls.Add(1)
			return nil, nil
		}, preview)
		d.send(d.m.Init()())
		d.step()
		start := time.Now()
		poll := d.next()
		assert.Equal(t, time.Since(start), 5*time.Second, "polled other than five seconds after the list came back")
		d.send(poll)
		d.step()
		assert.Equal(t, calls.Load(), int32(2))
		// The next poll's timer is running: cancelling ends it without
		// waiting it out.
		start = time.Now()
		d.cancel()
		d.wg.Wait()
		assert.Equal(t, time.Since(start), time.Duration(0), "the poll waited out its timer after cancellation")
	})
}

// Flows is the flows the last refresh read, as a copy the caller may change.
func TestFlowsIsTheLastReadCopied(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var call atomic.Int32
		a := circleci.Flow{ID: "a1", Title: "Fix parser", State: "Planning - Human", Next: waitHuman}
		b := circleci.Flow{ID: "b2", Title: "Build it", State: "Building - Agent", Next: waitAgent}
		d := newDriver(t, func(context.Context) ([]circleci.Flow, error) {
			if call.Add(1) == 1 {
				return []circleci.Flow{a}, nil
			}
			return []circleci.Flow{a, b}, nil
		}, preview)
		assert.Check(t, cmp.Len(d.m.Flows(), 0))
		d.load()
		d.refresh()
		d.step()

		got := d.m.Flows()
		assert.DeepEqual(t, got, []circleci.Flow{a, b})
		got[0].Title = "changed"
		assert.Check(t, cmp.Equal(d.m.Flows()[0].Title, "Fix parser"))
	})
}
