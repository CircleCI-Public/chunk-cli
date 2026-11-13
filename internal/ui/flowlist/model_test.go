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

// newDriver is a driver for the list. It reads blockers unless an option says
// otherwise.
func newDriver(t *testing.T, list func(context.Context) ([]circleci.Flow, error), opts ...func(flowlist.Model) flowlist.Model) *driver {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &driver{t: t, ctx: ctx, cancel: cancel, events: make(chan tea.Msg, 100)}
	d.m = flowlist.New(ctx, list)
	d.m = blockers(nil, nil)(d.m)
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
	d.step() // List response starts the first blockers read and poll timer.
	d.step() // Blockers response.
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
		{ID: "docs", Title: "retry-docs", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"},
		{ID: "other", Title: "other", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"},
		{ID: "p1", Title: "retry-policy", State: "build", Next: waitAgent, WaitingOn: "session"},
		{ID: "cfg", Title: "retry-config", State: "build", Next: waitAgent, WaitingOn: "session"},
		{ID: "cfg2", Title: "retry-backoff", State: "build", Next: waitAgent, WaitingOn: "session"},
		{ID: "deep", Title: "retry-jitter", State: "build", Next: waitAgent, WaitingOn: "session"},
		{ID: "orphan", Title: "orphan", State: "build", Next: waitAgent, WaitingOn: "session"},
		{ID: "skip", Title: "retry-docs-site", State: "build", Next: waitAgent, WaitingOn: "session"},
	}
	subPlanParents = map[string]string{"docs": "p1", "cfg": "p1", "cfg2": "p1", "deep": "cfg", "orphan": "gone", "skip": "docs"}
)

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
	d.send(tea.WindowSizeMsg{Width: 50, Height: 70})
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
			circleci.Flow{ID: "a1", Title: "Fix parser", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"},
			circleci.Flow{ID: "b2", Title: "Cache café\x1b[31m\x00", State: "Review - Agent", Next: waitAgent, WaitingOn: "session"},
			// Listed in reverse phase order: Planning shows before Building.
			circleci.Flow{ID: "c3", Title: "Build it", State: "build", Next: waitHuman, WaitingOn: "human"},
			circleci.Flow{ID: "d4", Title: "Plan it", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"},
		))
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
			d := newDriver(t, fixed(subPlans...), func(m flowlist.Model) flowlist.Model {
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
		d := newDriver(t, fixed())
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

// The pane beside the list is the host's: drawn at DetailSize, to the right
// of the list, with no border of its own, and not at all when it does not fit.
func TestDetailFillsThePane(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDriver(t, fixed(circleci.Flow{ID: "a", Title: "Alpha", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"}))
		d.load()
		d.send(tea.WindowSizeMsg{Width: 110, Height: 30})
		d.m = d.m.WithDetail("hello")
		w, h := d.m.DetailSize()
		const footer = 1 // the help bar
		assert.Check(t, cmp.Equal(w, 110-theme.ListWidth-2))
		assert.Check(t, cmp.Equal(h, 30-footer))
		rows := strings.Split(d.content(), "\n")
		assert.Check(t, cmp.Len(rows, 30))
		idx := strings.Index(rows[0], "hello")
		assert.Assert(t, idx >= 0, "the detail is not drawn:\n%s", d.content())
		assert.Check(t, cmp.Equal(ansi.StringWidth(rows[0][:idx]), theme.ListWidth+2))
		assert.Check(t, !strings.Contains(d.content(), "preview"), "the pane has a title of its own:\n%s", d.content())

		d.send(tea.WindowSizeMsg{Width: 60, Height: 30})
		w, h = d.m.DetailSize()
		assert.Check(t, w == 0 && h == 0, "DetailSize at 60 columns is %d, %d", w, h)
		assert.Check(t, !strings.Contains(d.content(), "hello"), "the detail is drawn at 60 columns:\n%s", d.content())
	})
}

// Selected is the flow the list shows selected, and its marker and help bar
// are dim while the pane has the keyboard.
func TestSelectedAndFocus(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDriver(t, fixed(
			circleci.Flow{ID: "a", Title: "Alpha", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"},
			circleci.Flow{ID: "b", Title: "Be\x1b[31mta", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"},
		))
		id, title := d.m.Selected()
		assert.Check(t, id == "" && title == "", "a list that has not loaded shows none: %q, %q", id, title)
		d.load()
		id, title = d.m.Selected()
		assert.Check(t, id == "a" && title == "Alpha", "%q, %q", id, title)
		d.send(press('j'))
		d.step()
		id, title = d.m.Selected()
		assert.Check(t, id == "b" && title == "Beta", "%q, %q", id, title)

		focused := d.m.Content()
		unfocused := d.m.WithFocused(false).Content()
		assert.Check(t, focused != unfocused, "the focus does not change the drawing")
		assert.Check(t, cmp.Equal(ansi.Strip(focused), ansi.Strip(unfocused)), "only the style changes")
		assert.Check(t, cmp.Equal(d.m.WithFocused(false).WithFocused(true).Content(), focused))
	})
}

// resumeLines are every line ResumeLine can say, so a test can check the
// list itself shows none: the host shows it, in its header.
var resumeLines = []string{
	"No session holds", "Claimed by", "chunk can't resume",
	"Waiting on you", "Waiting on factory", "Waiting on blockers", "Waiting on sub-plans", "Waiting on Retry config",
}

// noResumeLine checks the list's own view says nothing about resuming.
func noResumeLine(t *testing.T, got string) {
	t.Helper()
	for _, l := range resumeLines {
		assert.Check(t, !strings.Contains(got, l), "the list says %q:\n%s", l, got)
	}
}

// deleteMsgs runs cmd, and every command it batches, and returns the
// DeleteMsgs they produce.
func deleteMsgs(cmd tea.Cmd) []flowlist.DeleteMsg {
	if cmd == nil {
		return nil
	}
	switch msg := cmd().(type) {
	case flowlist.DeleteMsg:
		return []flowlist.DeleteMsg{msg}
	case tea.BatchMsg:
		var out []flowlist.DeleteMsg
		for _, c := range msg {
			out = append(out, deleteMsgs(c)...)
		}
		return out
	}
	return nil
}

func TestDelete(t *testing.T) {
	const asking = "Delete this plan, its comments and its flow?"
	alpha := circleci.Flow{ID: "a", Title: "Alpha", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"}
	beta := circleci.Flow{ID: "b", Title: "Beta", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"}
	chat := func(m flowlist.Model) flowlist.Model { return m.WithChatKey() }
	loaded := func(t *testing.T, list func(context.Context) ([]circleci.Flow, error)) *driver {
		d := newDriver(t, list, chat)
		d.send(tea.WindowSizeMsg{Width: 140, Height: 30})
		d.load()
		return d
	}

	t.Run("y deletes the asked flow", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := loaded(t, fixed(alpha, beta))
			assert.Check(t, cmp.Contains(d.content(), "X delete plan"))
			d.send(press('X'))
			got := d.content()
			assert.Check(t, cmp.Contains(got, asking))
			assert.Check(t, cmp.Contains(got, "y / n"))
			assert.Check(t, !strings.Contains(got, "X delete plan"), "help bar shown while asking:\n%s", got)
			d.send(press('y'))
			assert.Check(t, cmp.DeepEqual(d.next(), flowlist.DeleteMsg{ID: "a", Title: "Alpha"}))
			assert.Check(t, !strings.Contains(d.content(), asking))
		})
	})

	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{
			name: "n",
			key:  press('n'),
		},
		{
			name: "q",
			key:  press('q'),
		},
		{
			name: "esc",
			key:  press(tea.KeyEscape),
		},
		{
			name: "x",
			key:  press('x'),
		},
		{
			name: "ctrl+c",
			key:  tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl},
		},
		{
			name: "R",
			key:  press('R'),
		},
		{
			name: "enter",
			key:  press(tea.KeyEnter),
		},
		{
			name: "down",
			key:  press(tea.KeyDown),
		},
	} {
		t.Run("cancelled by "+tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := loaded(t, fixed(alpha, beta))
				d.send(press('X'))
				m, cmd := d.m.Update(tc.key)
				d.m = m
				assert.Check(t, cmd == nil, "the key did something")
				got := d.content()
				assert.Check(t, !strings.Contains(got, asking), "still asking:\n%s", got)
				assert.Check(t, cmp.Contains(got, "X delete plan"))
				// The selection did not move: X asks about Alpha again.
				d.send(press('X'))
				d.send(press('y'))
				assert.Check(t, cmp.DeepEqual(d.next(), flowlist.DeleteMsg{ID: "a", Title: "Alpha"}))
			})
		})
	}

	t.Run("a claimed flow does not offer X", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			claimed := alpha
			claimed.SessionID = "s1"
			d := loaded(t, fixed(claimed))
			assert.Check(t, !strings.Contains(d.content(), "X delete plan"))
			d.send(press('X'))
			assert.Check(t, !strings.Contains(d.content(), asking))
		})
	})

	t.Run("a flow only a worker claims does not offer X", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			claimed := alpha
			claimed.WorkerID = "w-1"
			d := loaded(t, fixed(claimed))
			got := d.content()
			assert.Check(t, cmp.Contains(got, "claimed"))
			assert.Check(t, !strings.Contains(got, "X delete plan"))
			d.send(press('X'))
			assert.Check(t, !strings.Contains(d.content(), asking))
		})
	})

	t.Run("an empty list does not offer X", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newDriver(t, fixed(), chat)
			d.send(tea.WindowSizeMsg{Width: 140, Height: 30})
			d.send(d.m.Init()())
			d.step()
			assert.Check(t, !strings.Contains(d.content(), "X delete plan"))
			d.send(press('X'))
			assert.Check(t, !strings.Contains(d.content(), asking))
		})
	})

	t.Run("a flow being deleted does not offer X", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := loaded(t, fixed(alpha, beta))
			d.m = d.m.WithDeleting("a")
			assert.Check(t, !strings.Contains(d.content(), "X delete plan"))
			d.send(press('X'))
			assert.Check(t, !strings.Contains(d.content(), asking))
			d.send(press(tea.KeyDown))
			assert.Check(t, cmp.Contains(d.content(), "X delete plan"))
		})
	})

	t.Run("a refresh moves the asked flow", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			flows := []circleci.Flow{alpha, beta}
			d := loaded(t, func(context.Context) ([]circleci.Flow, error) { return flows, nil })
			d.send(press('X'))
			flows = []circleci.Flow{beta, alpha}
			d.refresh()
			assert.Check(t, cmp.Contains(d.content(), asking))
			m, cmd := d.m.Update(press('y'))
			d.m = m
			assert.Check(t, cmp.DeepEqual(deleteMsgs(cmd), []flowlist.DeleteMsg{{ID: "a", Title: "Alpha"}}))
		})
	})

	t.Run("a refresh drops the asked flow", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			flows := []circleci.Flow{alpha, beta}
			d := loaded(t, func(context.Context) ([]circleci.Flow, error) { return flows, nil })
			d.send(press('X'))
			flows = []circleci.Flow{beta}
			d.refresh()
			assert.Check(t, !strings.Contains(d.content(), asking))
			m, cmd := d.m.Update(press('y'))
			d.m = m
			assert.Check(t, cmp.Len(deleteMsgs(cmd), 0))
		})
	})

	t.Run("help bar order", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := loaded(t, fixed(alpha))
			got := d.content()
			at, del, quit := strings.Index(got, "ctrl+o chat"), strings.Index(got, "X delete plan"), strings.Index(got, "q quit")
			assert.Check(t, at >= 0 && at < del && del < quit, "help bar:\n%s", got)
		})
	})
}

func TestResume(t *testing.T) {
	building := circleci.Flow{ID: "a", Title: "Alpha", State: "build", Next: waitAgent, WaitingOn: "session"}
	for _, tc := range []struct {
		name     string
		flow     circleci.Flow
		blockers map[string][]planstate.Blocker
		err      error
		// kind is what R asks the host for; ResumeNone is nothing.
		kind planstate.ResumeKind
		// holder is the session a take-over asks about.
		holder string
		line   string
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
				ID:        "a",
				Title:     "Alpha",
				State:     "refine-proposal",
				Next:      []circleci.Transition{{Actor: "session", TaskCategory: "write-units"}},
				WaitingOn: "session",
			},
			kind: planstate.ResumePlanner,
			line: "No session holds this plan · R resumes its planner",
		},
		{
			name: "claimed",
			flow: circleci.Flow{
				ID:        "a",
				Title:     "Alpha",
				State:     "build",
				Next:      waitAgent,
				WaitingOn: "session",
				SessionID: "bb5a623b-0000",
			},
			line: "Claimed by session bb5a623b · factory daemon release a frees it",
		},
		{
			name: "claimed, planning",
			flow: circleci.Flow{
				ID:        "a",
				Title:     "Alpha",
				State:     "refine-proposal",
				Next:      []circleci.Transition{{Actor: "session", TaskCategory: "write-units"}},
				WaitingOn: "session",
				SessionID: "1be8ec19-0000",
			},
			kind:   planstate.ResumeTakeOver,
			holder: "1be8ec19-0000",
			line:   "Claimed by session 1be8ec19 · R releases it and resumes the planner",
		},
		{
			name: "claimed by a worker only, planning",
			flow: circleci.Flow{
				ID:        "a",
				Title:     "Alpha",
				State:     "refine-proposal",
				Next:      []circleci.Transition{{Actor: "session", TaskCategory: "write-units"}},
				WaitingOn: "session",
				WorkerID:  "w-1",
			},
			line: "Claimed by worker w-1 · factory daemon release a frees it",
		},
		{
			name: "claimed by a worker only, building",
			flow: circleci.Flow{
				ID:        "a",
				Title:     "Alpha",
				State:     "build",
				Next:      waitAgent,
				WaitingOn: "session",
				WorkerID:  "w-1",
			},
			line: "Claimed by worker w-1 · factory daemon release a frees it",
		},
		{
			name: "waiting on you",
			flow: circleci.Flow{ID: "a", Title: "Alpha", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"},
			line: "Waiting on you",
		},
		{
			name:     "blocked",
			flow:     building,
			blockers: map[string][]planstate.Blocker{"a": {{Title: "Retry config", State: "build", Active: true}}},
			line:     "Waiting on Retry config",
		},
		{
			name:     "blockers done",
			flow:     building,
			blockers: map[string][]planstate.Blocker{"a": {{Title: "Retry config", State: "ended"}, {Title: "Docs", State: "ended", Outcome: "merged"}}},
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
				d := newDriver(t, fixed(tc.flow), blockers(tc.blockers, tc.err))
				d.load()
				got := d.content()
				assert.Check(t, cmp.Equal(strings.Contains(got, "R resume"), tc.kind != planstate.ResumeNone), "help bar:\n%s", got)
				assert.Check(t, cmp.Equal(d.m.ResumeLine(), tc.line))
				noResumeLine(t, got)
				if tc.kind == planstate.ResumeNone {
					_, cmd := d.m.Update(press('R'))
					assert.Check(t, cmd == nil, "R did something")
					return
				}
				d.send(press('R'))
				if tc.kind == planstate.ResumeTakeOver {
					d.send(press('y'))
				}
				assert.Check(t, cmp.DeepEqual(d.next(), flowlist.ResumeMsg{ID: "a", Title: "Alpha", Kind: tc.kind, Holder: tc.holder}))
			})
		})
	}
	t.Run("without blockers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			// A host that reads no blockers still offers resume.
			d := newDriver(t, fixed(building), func(m flowlist.Model) flowlist.Model { return m.WithBlockers(nil) })
			d.send(d.m.Init()())
			d.step() // List response; there is no read to wait for.
			assert.Check(t, cmp.Contains(d.content(), "R resume"))
			d.send(press('R'))
			assert.Check(t, cmp.DeepEqual(d.next(), flowlist.ResumeMsg{ID: "a", Title: "Alpha", Kind: planstate.ResumeBuild}))
		})
	})
	t.Run("before the blockers load", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			release := make(chan struct{})
			slow := func(m flowlist.Model) flowlist.Model {
				return m.WithBlockers(func(ctx context.Context, _ string) ([]planstate.Blocker, error) {
					select {
					case <-release:
					case <-ctx.Done():
					}
					return nil, nil
				})
			}
			d := newDriver(t, fixed(building), slow)
			d.send(d.m.Init()())
			d.step() // List response starts the blockers read, which waits.
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
			claimed := circleci.Flow{ID: "b", Title: "Beta", State: "build", Next: waitAgent, WaitingOn: "session", SessionID: "cc6b734c-1111"}
			d := newDriver(t, fixed(building, claimed), blockers(nil, nil))
			d.load()
			assert.Check(t, cmp.Equal(d.m.ResumeLine(), "No session holds this plan · R starts a build session"))
			d.send(press('j'))
			d.step() // Beta's blockers.
			got := d.content()
			assert.Check(t, cmp.Contains(got, "▶ Beta"))
			assert.Check(t, cmp.Equal(d.m.ResumeLine(), "Claimed by session cc6b734c · factory daemon release b frees it"))
			noResumeLine(t, got)
			assert.Check(t, !strings.Contains(got, "R resume"), "help bar:\n%s", got)
		})
	})
}

// resumeMsg is the next ResumeMsg the driver's commands return, handing any
// other message back to the model on the way.
func (d *driver) resumeMsg() flowlist.ResumeMsg {
	for {
		msg := d.next()
		if r, ok := msg.(flowlist.ResumeMsg); ok {
			return r
		}
		d.send(msg)
	}
}

// R on a claimed planning flow asks first; only y sends the take-over, for
// the plan and session the question was opened for.
func TestTakeOver(t *testing.T) {
	const holder = "1be8ec19-0000"
	claimed := func(id, title, session string) circleci.Flow {
		return circleci.Flow{
			ID:        id,
			Title:     title,
			State:     "refine-proposal",
			Next:      []circleci.Transition{{Actor: "session", TaskCategory: "write-units"}},
			WaitingOn: "session",
			SessionID: session,
		}
	}
	question := planstate.TakeOverQuestion(holder)
	loaded := func(t *testing.T, list func(context.Context) ([]circleci.Flow, error)) *driver {
		d := newDriver(t, list, blockers(nil, nil))
		d.send(tea.WindowSizeMsg{Width: 200, Height: 30})
		d.load()
		return d
	}
	want := flowlist.ResumeMsg{ID: "a", Title: "Alpha", Kind: planstate.ResumeTakeOver, Holder: holder}

	t.Run("the footer asks, y sends", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := loaded(t, fixed(claimed("a", "Alpha", holder)))
			d.send(press('R'))
			got := d.content()
			assert.Check(t, cmp.Contains(got, question+" y / n"))
			assert.Check(t, !strings.Contains(got, "R resume"), "help bar shown while asking:\n%s", got)
			d.send(press('y'))
			assert.Check(t, cmp.DeepEqual(d.next(), want))
			assert.Check(t, !strings.Contains(d.content(), question))
		})
	})

	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{
			name: "n",
			key:  press('n'),
		},
		{
			name: "x",
			key:  press('x'),
		},
		{
			name: "R",
			key:  press('R'),
		},
		{
			name: "esc",
			key:  press(tea.KeyEscape),
		},
	} {
		t.Run("cancelled by "+tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				d := loaded(t, fixed(claimed("a", "Alpha", holder)))
				d.send(press('R'))
				m, cmd := d.m.Update(tc.key)
				d.m = m
				assert.Check(t, cmd == nil, "the key did something")
				assert.Check(t, !strings.Contains(d.content(), question))
			})
		})
	}

	t.Run("the flow goes away", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var gone atomic.Bool
			d := loaded(t, func(context.Context) ([]circleci.Flow, error) {
				if gone.Load() {
					return []circleci.Flow{claimed("b", "Beta", "cc6b734c-1111")}, nil
				}
				return []circleci.Flow{claimed("a", "Alpha", holder), claimed("b", "Beta", "cc6b734c-1111")}, nil
			})
			d.send(press('R'))
			gone.Store(true)
			d.refresh()
			assert.Check(t, !strings.Contains(d.content(), question), "still asking:\n%s", d.content())
			_, cmd := d.m.Update(press('y'))
			assert.Check(t, cmd == nil, "y sent something")
		})
	})

	for _, tc := range []struct {
		name    string
		session string
	}{
		{
			name:    "the claim moves",
			session: "s-newer",
		},
		{
			name:    "the claim is gone",
			session: "",
		},
	} {
		t.Run("still sends the confirmed session when "+tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var session atomic.Value
				session.Store(holder)
				d := loaded(t, func(context.Context) ([]circleci.Flow, error) {
					return []circleci.Flow{claimed("a", "Alpha", session.Load().(string))}, nil
				})
				d.send(press('R'))
				session.Store(tc.session)
				d.refresh()
				d.send(press('y'))
				assert.Check(t, cmp.DeepEqual(d.resumeMsg(), want))
			})
		})
	}
}

func TestStableRefreshAndSelect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var call atomic.Int32
		a := circleci.Flow{ID: "a", Title: "Alpha", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"}
		b := circleci.Flow{ID: "b", Title: "Beta", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"}
		c := circleci.Flow{ID: "c", Title: "Gamma", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"}
		zeta := circleci.Flow{ID: "z", Title: "Zeta plan", State: "Zeta - Human", Next: waitHuman, WaitingOn: "human"}
		alphaPhase := circleci.Flow{ID: "al", Title: "Alpha plan", State: "Alpha - Human", Next: waitHuman, WaitingOn: "human"}
		zetaToo := circleci.Flow{ID: "z2", Title: "Zeta too", State: " zeta - Human", Next: waitHuman, WaitingOn: "human"}
		d := newDriver(t, func(context.Context) ([]circleci.Flow, error) {
			if call.Add(1) == 1 {
				return []circleci.Flow{a, b, zeta, alphaPhase}, nil
			}
			return []circleci.Flow{c, b, a, alphaPhase, zeta, zetaToo}, nil
		})
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
		assert.Check(t, cmp.Equal(count(rows, "Zeta - Human"), 1), "want one Zeta sub-heading:\n%s", strings.Join(rows, "\n"))
		assert.Check(t, cmp.Equal(rows[zetaRow-1], "Zeta - Human"))
		assert.Check(t, cmp.Equal(rows[alphaRow-1], "Alpha - Human"))
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

// blockersOf has m read each flow's blockers from byID, once its gate in gates
// closes, when it has one.
func blockersOf(byID map[string][]planstate.Blocker, gates map[string]<-chan struct{}) func(flowlist.Model) flowlist.Model {
	return func(m flowlist.Model) flowlist.Model {
		return m.WithBlockers(func(ctx context.Context, id string) ([]planstate.Blocker, error) {
			if gate := gates[id]; gate != nil {
				select {
				case <-gate:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return byID[id], nil
		})
	}
}

func TestLateBlockersCannotOverwriteSelection(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		releaseA := make(chan struct{})
		// a's blockers are all done, so a late read would offer its resume.
		done := []planstate.Blocker{{Title: "Retry config", State: "ended"}}
		blocked := []planstate.Blocker{{Title: "Retry config", State: "build", Active: true}}
		a := circleci.Flow{ID: "a", Title: "Alpha", State: "build", Next: waitAgent, WaitingOn: "session"}
		b := circleci.Flow{ID: "b", Title: "Beta", State: "build", Next: waitAgent, WaitingOn: "session"}
		d := newDriver(t, fixed(a, b),
			blockersOf(map[string][]planstate.Blocker{"a": done, "b": blocked}, map[string]<-chan struct{}{"a": releaseA}))
		d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
		d.send(d.m.Init()())
		d.step()
		d.send(press('j'))
		d.step()
		assert.Check(t, cmp.Equal(d.m.ResumeLine(), "Waiting on Retry config"))
		close(releaseA)
		d.step()
		assert.Check(t, cmp.Equal(d.m.ResumeLine(), "Waiting on Retry config"), "a late read replaced the selected flow's")
		assert.Check(t, !strings.Contains(d.content(), "R resume"), "a late read offers the resume:\n%s", d.content())
		// The late answer was dropped, not kept for a: back on a, nothing is
		// offered until its own read lands.
		d.send(press('k'))
		assert.Check(t, cmp.Equal(d.m.ResumeLine(), ""), "the late read for a was kept")
	})
}

func TestLateBlockersForSameIDCannotOverwriteNewRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		started := make(chan struct{})
		var calls atomic.Int32
		blocked := []planstate.Blocker{{Title: "Retry config", State: "build", Active: true}}
		a := circleci.Flow{ID: "a", Title: "Alpha", State: "build", Next: waitAgent, WaitingOn: "session"}
		b := circleci.Flow{ID: "b", Title: "Beta", State: "build", Next: waitAgent, WaitingOn: "session"}
		d := newDriver(t, fixed(a, b), func(m flowlist.Model) flowlist.Model {
			return m.WithBlockers(func(ctx context.Context, id string) ([]planstate.Blocker, error) {
				if id == "a" && calls.Add(1) == 1 {
					close(started)
					select {
					case <-release:
						return nil, nil // done: the obsolete answer offers the resume
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
				return blocked, nil
			})
		})
		d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
		d.send(d.m.Init()())
		d.step()
		<-started
		d.send(press('j'))
		d.step()
		d.send(press('k'))
		d.step()
		assert.Check(t, cmp.Equal(d.m.ResumeLine(), "Waiting on Retry config"))
		close(release)
		d.step()
		assert.Check(t, cmp.Equal(d.m.ResumeLine(), "Waiting on Retry config"), "an obsolete read replaced the current one")
	})
}

func TestEmptyErrorsAndStaleData(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newDriver(t, fixed())
			d.send(d.m.Init()())
			d.step()
			text := d.content()
			assert.Check(t, cmp.Contains(text, "no open flows"))
			assert.Check(t, cmp.Contains(text, "n starts one"))
		})
	})
	t.Run("loading", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			d := newDriver(t, fixed())
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
			})
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
			d := newDriver(t, fixed(subPlans...), func(m flowlist.Model) flowlist.Model {
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
			}, func(m flowlist.Model) flowlist.Model {
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
	t.Run("list retains success", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var lists atomic.Int32
			d := newDriver(t, func(context.Context) ([]circleci.Flow, error) {
				if lists.Add(1) == 2 {
					return nil, errors.New("list offline")
				}
				return []circleci.Flow{{ID: "a", Title: "Retained"}}, nil
			})
			d.send(tea.WindowSizeMsg{Width: 120, Height: 35})
			d.load()
			d.refresh()
			text := d.content()
			for _, want := range []string{"Retained", "list offline", "stale"} {
				assert.Check(t, cmp.Contains(text, want))
			}
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
			name: "an old name, as given",
			flow: circleci.Flow{State: "planning - human", Next: human, WaitingOn: "human"},
			want: "planning - human",
		},
		{
			name: "empty",
			flow: circleci.Flow{State: "", Next: human, WaitingOn: "human"},
			want: "",
		},
		{
			name: "integrate",
			flow: circleci.Flow{State: "integrate", Next: review, WaitingOn: "session"},
			want: "Integrating",
		},
		{
			name: "refine-units",
			flow: circleci.Flow{State: "refine-units", Next: review, WaitingOn: "daemon"},
			want: "Planning",
		},
		{
			name: "waiting on blockers",
			flow: circleci.Flow{State: "build", WaitingOn: "blockers"},
			want: "",
		},
		{
			name: "not started",
			flow: circleci.Flow{State: "init", Next: review, WaitingOn: "session"},
			want: "",
		},
		{
			name: "init, waiting on you to start it",
			flow: circleci.Flow{State: "init", Next: human, WaitingOn: "human"},
			want: "Not started",
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
			in:   "stand-by",
			want: "Stand-by",
		},
		{
			name: "later words keep case",
			in:   "custom phase",
			want: "Custom phase",
		},
		{
			name: "already capitalized",
			in:   "Not Started",
			want: "Not Started",
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
		human := []circleci.Transition{{Actor: "human", Event: circleci.AddressComments, IsRequired: true}}
		session := func(task string) []circleci.Transition {
			return []circleci.Transition{{Actor: "session", TaskCategory: task, IsDispatched: true, IsRequired: true}}
		}
		flows := []circleci.Flow{
			// No waiting_on: Other, whatever the state.
			{ID: "other", Title: "Other plan", State: "Custom exact state"},
			{ID: "bot", Title: "Bot plan", State: "Reviewing - Bot"},
			// Not started: init, not waiting on you.
			{ID: "new", Title: "New plan", State: "init", WaitingOn: "session", Next: session("early-plan")},
			{ID: "agent", Title: "Agent plan", State: "refine-proposal", WaitingOn: "session", Next: session("review-plan")},
			{ID: "untitled", State: "build", WaitingOn: "session", Next: session("build-unit")},
			{ID: "built", Title: "Built", State: "build", WaitingOn: "human", Task: &circleci.FlowTask{ID: "t"}, Next: human},
			{
				ID: "human", Title: "Planned", State: "refine-proposal", WaitingOn: "human", UpdatedAt: now,
				SessionID: "s1", Task: &circleci.FlowTask{ID: "t1", Category: "build-unit"},
				ApplicableEvents: []circleci.FlowEvent{circleci.AddressComments},
				Next:             human,
			},
			// Old names wait on you too, each a sub-group of its own after
			// Planning and Building.
			{ID: "zeta", Title: "Zeta", State: "Zeta - Human", WaitingOn: "human", Next: human},
			{ID: "alpha", Title: "Alpha", State: "Alpha - Human", WaitingOn: "human", Next: human},
			{ID: "old", Title: "Old name", State: "Planning - Human", WaitingOn: "human", Next: human},
			{ID: "long-state", Title: "Long state", State: "Custom " + strings.Repeat("state ", 20)},
			{ID: "blank-state", Title: "Blank state", State: ""},
			{ID: "refined", Title: "Refined", State: "refine-units", WaitingOn: "human", Next: human},
			{ID: "building", Title: "Building new", State: "build", WaitingOn: "daemon"},
			{ID: "held", Title: "Held", State: "build", WaitingOn: "blockers"},
			{ID: "parent", Title: "Parent", State: "build", WaitingOn: "sub-plans"},
			{ID: "ended", Title: "Ended", State: "ended", Phase: circleci.PhaseEnded, Outcome: "canceled"},
			{ID: "merging", Title: "Merging", State: "integrate", WaitingOn: "human", Next: []circleci.Transition{{Actor: "human", Action: "merge-pr", IsRequired: true}}},
			{ID: "merged", Title: "Merged", State: "ended", Phase: circleci.PhaseEnded, Outcome: "merged"},
			// A plan that needs ctrl+s to start is found under Needs your
			// review, not Not started.
			{ID: "start", Title: "Start me", State: "init", WaitingOn: "human", Next: []circleci.Transition{{Actor: "human", Event: circleci.Prioritize, IsRequired: true}}},
			{ID: "unknown-actor", Title: "Unknown actor", State: "build", WaitingOn: "someone"},
		}
		d := newDriver(t, fixed(flows...))
		d.load()
		rows := listRows(d)
		all := strings.Join(rows, "\n")
		for _, want := range []string{"flows", "Needs your review  8", "With agent  3", "Not started  1", "Other  9", "Custom exact state", "Reviewing - bot", "Untitled plan"} {
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
		// One sub-heading a step: refine-proposal and refine-units are
		// both Planning.
		planned := index("Planned")
		assert.Check(t, cmp.Equal(rows[planned+2], "Refined"), "rows:\n%s", all)
		review := rows[index("Needs your review  8"):index("With agent  3")]
		assert.Check(t, cmp.Equal(count(review, "Planning"), 1), "want one Planning sub-heading:\n%s", all)
		assert.Check(t, cmp.Equal(count(review, "Building"), 1), "want one Building sub-heading:\n%s", all)
		// Waiting on you is Needs your review whatever the state: an
		// action such as merge-pr, and an init plan waiting on you to
		// start it.
		for _, title := range []string{"Refined", "Merging", "Start me", "Old name"} {
			assert.Check(t, slices.Contains(review, title), "%q is not under Needs your review:\n%s", title, all)
		}
		// A session or factory is With agent.
		agent := rows[index("With agent  3"):index("Not started  1")]
		for _, title := range []string{"Agent plan", "Untitled plan", "Building new"} {
			assert.Check(t, slices.Contains(agent, title), "%q is not under With agent:\n%s", title, all)
		}
		// Blockers, sub-plans, nobody and an actor chunk does not know are
		// Other, labelled by step and by outcome, with who they wait on.
		for _, title := range []string{"Held", "Parent", "Ended", "Merged", "Unknown actor", "Other plan"} {
			assert.Check(t, index(title) > index("Other  9"), "%q is not under Other:\n%s", title, all)
		}
		assert.Check(t, cmp.Equal(rows[index("Held")+1], "Building · waiting on blockers"), "rows:\n%s", all)
		assert.Check(t, cmp.Equal(rows[index("Parent")+1], "Building · waiting on sub-plans"), "rows:\n%s", all)
		assert.Check(t, cmp.Equal(rows[index("Unknown actor")+1], "Building"), "rows:\n%s", all)
		assert.Check(t, cmp.Equal(rows[index("Ended")+1], "Canceled"), "rows:\n%s", all)
		assert.Check(t, cmp.Equal(rows[index("Merged")+1], "Done"), "rows:\n%s", all)
		// Each sub-heading is directly above its first title. An old name
		// is a sub-group of its own, after Planning and Building.
		for title, heading := range map[string]string{
			"Planned": "Planning", "Built": "Building", "Zeta": "Zeta - Human", "Alpha": "Alpha - Human",
			"Old name": "Planning - Human", "Merging": "Integrating", "Start me": "Not started",
		} {
			assert.Check(t, cmp.Equal(rows[index(title)-1], heading), "rows:\n%s", all)
		}
		assert.Check(t, index("Built") < index("Old name"), "rows:\n%s", all)
		// Not started and Other have no sub-headings: only titles and details.
		// Only Other names the state, since no heading does; an empty one is unknown.
		other := index("Other  9")
		assert.Check(t, cmp.DeepEqual(rows[other+1:other+5], []string{"Other plan", "Custom exact state", "Bot plan", "Reviewing - bot"}), "rows:\n%s", all)
		assert.Check(t, cmp.Equal(rows[index("Blank state")+1], "Unknown"), "rows:\n%s", all)
		longState := rows[index("Long state")+1]
		assert.Check(t, strings.HasPrefix(longState, "Custom state state"), "rows:\n%s", all)
		assert.Check(t, strings.HasSuffix(longState, "…"), "a long state is not truncated:\n%s", all)
		notStarted := index("Not started  1")
		assert.Check(t, cmp.DeepEqual(rows[notStarted+1:notStarted+3], []string{"New plan", ""}), "rows:\n%s", all)
		// The row details: task category, claimed and updated-ago, each only
		// when there is one.
		// The details start the row; only the sub-heading above says the phase.
		assert.Check(t, strings.HasPrefix(rows[planned+1], "build-unit"), "rows:\n%s", all)
		assert.Check(t, cmp.Contains(rows[planned+1], "build-unit"))
		assert.Check(t, cmp.Contains(rows[planned+1], "claimed"))
		assert.Check(t, cmp.Contains(rows[planned+1], "just now"))
		assert.Check(t, cmp.Equal(rows[index("Built")+1], ""), "a flow with no details keeps a blank second row:\n%s", all)
		assert.Check(t, !strings.Contains(all, "ago"), "a zero UpdatedAt shows an age:\n%s", all)

		d.m = d.m.WithDetail("The pane\n" + strings.Repeat("界", 150))
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
				assert.Check(t, cmp.Equal(d.m.ResumeLine(), "Waiting on you"))
				if size.Width == 50 {
					assert.Check(t, !strings.Contains(text, "The pane"), "the pane shows at width 50:\n%s", text)
					assert.Check(t, cmp.Contains(lines[len(lines)-2], "n new plan"))
					assert.Check(t, !strings.Contains(text, "runs"), "the help names runs:\n%s", text)
				}
				if size.Width >= 80 && size.Height > 2 {
					assert.Check(t, cmp.Contains(text, "The pane"))
					assert.Check(t, cmp.Contains(lines[len(lines)-2], "q quit"))
					assert.Check(t, cmp.Contains(lines[len(lines)-1], "Unsent draft"))
				}
			}
		}
	})
}

func TestProgressRows(t *testing.T) {
	five, two, zero := 5, 2, 0
	long := strings.Repeat("Addressing findings ", 10)
	for _, tc := range []struct {
		name     string
		flow     circleci.Flow
		wantRow  string // the second row starts with this
		notInRow []string
		cut      bool // the second row is cut to one line
	}{
		{
			name: "summary replaces the task category",
			flow: circleci.Flow{
				Task:     &circleci.FlowTask{ID: "t", Category: "build-unit"},
				Progress: &circleci.FlowProgress{Summary: "Building unit 2 of 5", DoneUnits: &two, Units: &five},
			},
			wantRow:  "Building unit 2 of 5 · 2/5 units",
			notInRow: []string{"build-unit"},
		},
		{
			name:    "no summary, waits on blockers",
			flow:    circleci.Flow{WaitingOn: "blockers", Progress: &circleci.FlowProgress{}},
			wantRow: "Building · waiting on blockers",
		},
		{
			name:    "no summary, waits on sub-plans",
			flow:    circleci.Flow{WaitingOn: "sub-plans", Progress: &circleci.FlowProgress{}},
			wantRow: "Building · waiting on sub-plans",
		},
		{
			name: "no summary, waits on session: task category",
			flow: circleci.Flow{
				WaitingOn: "session", Task: &circleci.FlowTask{ID: "t", Category: "build-unit"},
				Progress: &circleci.FlowProgress{},
			},
			wantRow: "build-unit",
		},
		{
			name:     "older conductor: today's row",
			flow:     circleci.Flow{Task: &circleci.FlowTask{ID: "t", Category: "build-unit"}},
			wantRow:  "build-unit",
			notInRow: []string{"units", "waiting on"},
		},
		{
			name:     "no unit count when unit_count is 0",
			flow:     circleci.Flow{Progress: &circleci.FlowProgress{Summary: "Writing units", Units: &zero}},
			wantRow:  "Writing units",
			notInRow: []string{"units ·", "/0"},
		},
		{
			name:    "absent done count counts as 0",
			flow:    circleci.Flow{Progress: &circleci.FlowProgress{Summary: "Building unit 1 of 5", Units: &five}},
			wantRow: "Building unit 1 of 5 · 0/5 units",
		},
		{
			name:    "a long summary is cut to one row",
			flow:    circleci.Flow{Progress: &circleci.FlowProgress{Summary: long, DoneUnits: &two, Units: &five}},
			wantRow: "Addressing findings Addressing",
			cut:     true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := tc.flow
				f.ID, f.Title, f.State = "f", "The flow", "build"
				if f.Next == nil {
					f.Next = waitAgent
				}
				// A row checked for its details waits on a session unless
				// the case says otherwise, so it lists under With agent,
				// whose rows do not say the state.
				if f.WaitingOn == "" && tc.wantRow != "" {
					f.WaitingOn = "session"
				}
				d := newDriver(t, fixed(f))
				d.load()
				rows := listRows(d)
				all := strings.Join(rows, "\n")
				i := slices.IndexFunc(rows, func(r string) bool { return strings.TrimPrefix(r, "▶ ") == "The flow" })
				assert.Assert(t, i >= 0 && i+2 < len(rows), "rows:\n%s", all)
				row := rows[i+1]
				if tc.wantRow != "" {
					assert.Check(t, strings.HasPrefix(row, tc.wantRow), "row %q", row)
				}
				for _, not := range tc.notInRow {
					assert.Check(t, !strings.Contains(row, not), "row %q has %q", row, not)
				}
				if tc.cut {
					assert.Check(t, strings.HasSuffix(row, "…"), "row %q is not cut", row)
					assert.Check(t, cmp.Equal(rows[i+2], ""), "the flow takes more than two rows:\n%s", all)
				}
			})
		})
	}
}

func TestSubHeadingCase(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDriver(t, fixed(
			circleci.Flow{ID: "p", Title: "Lower planning", State: "planning - human", Next: waitHuman, WaitingOn: "human"},
			circleci.Flow{ID: "p2", Title: "Upper planning", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"},
			circleci.Flow{ID: "s", Title: "Parent", State: "stand-by", Next: waitHuman, WaitingOn: "human"},
			circleci.Flow{ID: "c", Title: "Custom", State: "custom phase - human", Next: waitHuman, WaitingOn: "human"},
			circleci.Flow{ID: "r", Title: "Ready", State: "ready", Next: waitHuman, WaitingOn: "human"},
			circleci.Flow{ID: "n", Title: "Numbered", State: "2nd pass - human", Next: waitHuman, WaitingOn: "human"},
			circleci.Flow{ID: "p3", Title: "Shouted planning", State: "PLANNING - HUMAN", Next: waitHuman, WaitingOn: "human"},
		))
		d.load()
		rows := listRows(d)
		all := strings.Join(rows, "\n")
		// Each sub-heading is directly above its first title, its first
		// letter upper-cased and the rest as that flow spells it. Factory's
		// states are labelled by step; any other state is shown as given.
		for title, heading := range map[string]string{
			"Upper planning": "Planning",
			"Lower planning": "Planning - human",
			"Parent":         "Standing by",
			"Custom":         "Custom phase - human",
			"Numbered":       "2nd pass - human",
		} {
			// The selected flow's row keeps its marker in listRows.
			i := slices.IndexFunc(rows, func(r string) bool { return strings.TrimPrefix(r, "▶ ") == title })
			assert.Assert(t, i > 0, "no row %q:\n%s", title, all)
			assert.Check(t, cmp.Equal(rows[i-1], heading), "rows:\n%s", all)
		}
		// ready is Planning too, under the one sub-heading.
		assert.Check(t, cmp.Equal(count(rows, "Planning"), 1), "rows:\n%s", all)
		// Spellings that differ only in case still share one sub-heading.
		assert.Check(t, cmp.Equal(count(rows, "Planning - human"), 1), "rows:\n%s", all)
		assert.Check(t, cmp.Equal(count(rows, "PLANNING - HUMAN"), 0), "rows:\n%s", all)
	})
}

func TestSubPlanRows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDriver(t, fixed(subPlans...), parents(subPlanParents))
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

		plain := newDriver(t, fixed(subPlans...))
		plain.load()
		without := newDriver(t, fixed(subPlans...), parents(nil))
		without.load()
		assert.Check(t, cmp.DeepEqual(listRows(without), listRows(plain)))

		// Indented rows and long hints fit every size.
		long := append(slices.Clone(subPlans), circleci.Flow{ID: "long", Title: strings.Repeat("界", 80), State: "build", Next: waitAgent, WaitingOn: "session"})
		withLong := maps.Clone(subPlanParents)
		withLong["long"] = "deep"
		d = newDriver(t, fixed(long...), parents(withLong))
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
			flows = append(flows, circleci.Flow{ID: fmt.Sprintf("id-%d", i), Title: fmt.Sprintf("Title-%02d", i), State: "refine-proposal", Next: waitHuman, WaitingOn: "human"})
		}
		d := newDriver(t, fixed(flows...))
		d.load()
		d.m = d.m.Select("id-79")
		d.send(nil)
		d.step()
		for _, size := range []tea.WindowSizeMsg{{Width: 80, Height: 24}, {Width: 120, Height: 35}} {
			d.send(size)
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
		})
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
		})
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
		a := circleci.Flow{ID: "a1", Title: "Fix parser", State: "refine-proposal", Next: waitHuman, WaitingOn: "human"}
		b := circleci.Flow{ID: "b2", Title: "Build it", State: "build", Next: waitAgent, WaitingOn: "session"}
		d := newDriver(t, func(context.Context) ([]circleci.Flow, error) {
			if call.Add(1) == 1 {
				return []circleci.Flow{a}, nil
			}
			return []circleci.Flow{a, b}, nil
		})
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
