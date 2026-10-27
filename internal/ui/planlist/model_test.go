package planlist_test

import (
	"context"
	"errors"
	"fmt"
	"image/color"
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
	"github.com/CircleCI-Public/chunk-cli/internal/ui/planlist"
)

// driver runs the component's commands, timers included, and hands back the
// messages they return. Each test runs in a synctest bubble, so the poll's
// timer fires as soon as everything else is blocked.
type driver struct {
	t      *testing.T
	m      planlist.Model
	ctx    context.Context
	cancel context.CancelFunc
	events chan tea.Msg
	wg     sync.WaitGroup
}

func newDriver(t *testing.T, list func(context.Context) ([]circleci.Plan, error), preview func(context.Context, string) (string, []circleci.PlanComment, error)) *driver {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	d := &driver{t: t, ctx: ctx, cancel: cancel, events: make(chan tea.Msg, 100)}
	d.m = planlist.New(ctx, list, preview)
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

func fixed(plans ...circleci.Plan) func(context.Context) ([]circleci.Plan, error) {
	return func(context.Context) ([]circleci.Plan, error) { return plans, nil }
}

func preview(_ context.Context, id string) (string, []circleci.PlanComment, error) {
	return "# Preview " + id, nil, nil
}

func open(d *driver, id string) {
	d.t.Helper()
	d.send(press(tea.KeyEnter))
	msg, ok := d.next().(planlist.OpenMsg)
	assert.Assert(d.t, ok, "enter did not emit OpenMsg")
	assert.Equal(d.t, msg.ID, id)
}

func TestNavigation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDriver(t, fixed(
			circleci.Plan{ID: "a1", Title: "Fix parser", State: "Planning - Human"},
			circleci.Plan{ID: "b2", Title: "Cache café\x1b[31m\x00", State: "Review - Agent"},
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
		open(d, "b2")
		text = d.content()
		assert.Check(t, cmp.Contains(text, "Fix parser"))
		assert.Check(t, cmp.Contains(text, "▶ Cache café"))
		d.send(press('k'))
		d.step()
		open(d, "a1")
		d.send(press(tea.KeyDown))
		d.step()
		open(d, "b2")
	})
}

func TestHostMessages(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d := newDriver(t, fixed(), preview)
		for _, key := range []tea.KeyPressMsg{press('2'), press(tea.KeyTab), press(tea.KeyEscape)} {
			d.send(key)
			assert.Check(t, cmp.DeepEqual(d.next(), planlist.BackMsg{}), "%s did not emit BackMsg", key.String())
		}
		d.send(press('n'))
		assert.Check(t, cmp.DeepEqual(d.next(), planlist.NewMsg{}), "n did not emit NewMsg")
		for _, key := range []tea.KeyPressMsg{press('q'), {Code: 'c', Mod: tea.ModCtrl}} {
			d.send(key)
			assert.Check(t, cmp.DeepEqual(d.next(), planlist.QuitMsg{}), "%s must ask the host to quit, not tea.Quit", key.String())
		}
	})
}

func TestStableRefreshAndSelect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var call atomic.Int32
		a := circleci.Plan{ID: "a", Title: "Alpha", State: "Planning - Human"}
		b := circleci.Plan{ID: "b", Title: "Beta", State: "Planning - Human"}
		c := circleci.Plan{ID: "c", Title: "Gamma", State: "Planning - Human"}
		d := newDriver(t, func(context.Context) ([]circleci.Plan, error) {
			if call.Add(1) == 1 {
				return []circleci.Plan{a, b}, nil
			}
			return []circleci.Plan{c, b, a}, nil
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
			circleci.Plan{ID: "a", Title: "Alpha"}, circleci.Plan{ID: "b", Title: "Beta"},
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
		assert.Check(t, !strings.Contains(text, "UNIQUE PREVIEW a"), "a late preview replaced the selected plan's:\n%s", text)
	})
}

func TestLatePreviewForSameIDCannotOverwriteNewRequest(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		started := make(chan struct{})
		var calls atomic.Int32
		d := newDriver(t, fixed(
			circleci.Plan{ID: "a", Title: "Alpha"}, circleci.Plan{ID: "b", Title: "Beta"},
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
			assert.Check(t, cmp.Contains(text, "no open plans"))
			assert.Check(t, cmp.Contains(text, "n starts one"))
		})
	})
	t.Run("initial error recovers", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var calls atomic.Int32
			d := newDriver(t, func(context.Context) ([]circleci.Plan, error) {
				if calls.Add(1) == 1 {
					return nil, errors.New("offline")
				}
				return []circleci.Plan{{ID: "a", Title: "Retained"}}, nil
			}, preview)
			d.send(d.m.Init()())
			d.step()
			text := d.content()
			for _, want := range []string{"offline", "plans unavailable", "retrying"} {
				assert.Check(t, cmp.Contains(text, want))
			}
			d.refresh()
			d.step()
			text = d.content()
			assert.Check(t, cmp.Contains(text, "Retained"))
			assert.Check(t, !strings.Contains(text, "offline"), "the error outlived a successful list:\n%s", text)
		})
	})
	t.Run("list and preview retain success", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			var lists, previews atomic.Int32
			d := newDriver(t, func(context.Context) ([]circleci.Plan, error) {
				if lists.Add(1) == 2 {
					return nil, errors.New("list offline")
				}
				return []circleci.Plan{{ID: "a", Title: "Retained"}}, nil
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
}

func TestGroupsCommentsAndDimensions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		now := time.Now()
		plans := []circleci.Plan{
			{ID: "other", Title: "Other plan", State: "Custom exact state"},
			{ID: "new", Title: "New plan", State: "Unprioritized"},
			{ID: "agent", Title: "Agent plan", State: "Planning - Agent"},
			{ID: "human", Title: "Human plan", State: "Review - Human", OpenComments: 2, UpdatedAt: now},
		}
		d := newDriver(t, fixed(plans...), func(context.Context, string) (string, []circleci.PlanComment, error) {
			return "# Markdown heading\n\n**Plan prose**\n\n" + strings.Repeat("界", 150), []circleci.PlanComment{
				{Body: "Please clarify", QuotedText: "Plan prose"},
				{Body: "RESOLVED SECRET", ResolvedAt: &now},
			}, nil
		})
		d.load()
		text := d.content()
		for _, want := range []string{"Needs your review", "With agent", "Not started", "Other", "custom exact state"} {
			assert.Check(t, cmp.Contains(text, want))
		}
		order := []string{"Human plan", "Agent plan", "New plan", "Other plan"}
		for i := 1; i < len(order); i++ {
			assert.Assert(t, strings.Index(text, order[i-1]) < strings.Index(text, order[i]), "wrong group order:\n%s", text)
		}
		for _, size := range []tea.WindowSizeMsg{{Width: 50, Height: 20}, {Width: 80, Height: 24}, {Width: 120, Height: 35}, {Width: 1, Height: 1}, {Width: 100, Height: 2}} {
			for _, bg := range []color.Color{color.White, color.Black} {
				d.send(size)
				d.send(tea.BackgroundColorMsg{Color: bg})
				d.m = d.m.WithNote("Unsent draft: " + strings.Repeat("界", 200) + "\nunsafe\x1b[2J")
				text = d.content()
				lines := strings.Split(text, "\n")
				assert.Assert(t, len(lines) <= size.Height, "height %d exceeds %d", len(lines), size.Height)
				for _, line := range lines {
					assert.Assert(t, ansi.StringWidth(line) <= size.Width, "width %d exceeds %d: %q", ansi.StringWidth(line), size.Width, line)
				}
				// The help bar, then the host's note under it.
				if size.Width == 50 {
					for _, not := range []string{"preview", "Markdown heading", "Please clarify"} {
						assert.Check(t, !strings.Contains(text, not), "%q shows at width 50:\n%s", not, text)
					}
					assert.Check(t, cmp.Contains(lines[len(lines)-2], "tab runs"))
				}
				if size.Width >= 80 && size.Height > 2 {
					for _, want := range []string{"preview · human", "Open comments (not plan text)", "> Please clarify", "Markdown heading", "Plan prose", "● 2", "just now"} {
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

func TestLongListKeepsSelectedRowVisible(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var plans []circleci.Plan
		for i := range 80 {
			plans = append(plans, circleci.Plan{ID: fmt.Sprintf("id-%d", i), Title: fmt.Sprintf("Title-%02d", i), State: "Planning - Human"})
		}
		d := newDriver(t, fixed(plans...), preview)
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
	})
}

func TestRefreshCoalescesAndCancellationStopsRequests(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		started := make(chan struct{})
		d := newDriver(t, func(ctx context.Context) ([]circleci.Plan, error) {
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
		d := newDriver(t, func(context.Context) ([]circleci.Plan, error) {
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
