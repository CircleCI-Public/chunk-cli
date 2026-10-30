package chat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// fake is the chat's daemon: send records what it was given, and poll
// answers with the queued results in turn, then with nothing new.
type fake struct {
	mu      sync.Mutex
	sent    [][2]string
	sendErr error
	afters  []int
	queue   []result
	logID   string
}

type result struct {
	u   Update
	err error
}

func (f *fake) send(_ context.Context, text, context string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, [2]string{text, context})
	return f.sendErr
}

func (f *fake) poll(_ context.Context, after int) (Update, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.afters = append(f.afters, after)
	if len(f.queue) == 0 {
		return Update{LogID: f.logID, Next: after}, nil
	}
	r := f.queue[0]
	f.queue = f.queue[1:]
	if r.err == nil {
		f.logID = r.u.LogID
	}
	return r.u, r.err
}

func (f *fake) queued(rs ...result) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queue = append(f.queue, rs...)
}

func (f *fake) lastAfter(t *testing.T) int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Assert(t, len(f.afters) > 0, "no poll was issued")
	return f.afters[len(f.afters)-1]
}

func (f *fake) polls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.afters)
}

func newChat(f *fake) Model {
	return New(context.Background(), f.send, f.poll).SetSize(80, 30)
}

// step runs cmd and drives the chat with what it returns, but for the
// spinner's ticks, and returns the commands the chat answered with.
func step(t *testing.T, m Model, cmd tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return m, nil
	}
	var cmds []tea.Cmd
	var apply func(msg tea.Msg)
	apply = func(msg tea.Msg) {
		switch msg := msg.(type) {
		case nil, spinMsg:
		case tea.BatchMsg:
			for _, c := range msg {
				if c != nil {
					apply(c())
				}
			}
		default:
			assert.Assert(t, IsMsg(msg), "%T is not the chat's own", msg)
			var next tea.Cmd
			m, next = m.Update(msg)
			cmds = append(cmds, next)
		}
	}
	apply(cmd())
	return m, tea.Batch(cmds...)
}

// open opens the chat and drives it through its first poll.
func open(t *testing.T, m Model) (Model, tea.Cmd) {
	t.Helper()
	m, cmd := m.Open()
	return step(t, m, cmd)
}

// round drives the chat from a poll's result through its next poll.
func round(t *testing.T, m Model, cmd tea.Cmd) (Model, tea.Cmd) {
	t.Helper()
	m, cmd = step(t, m, cmd) // the tick
	return step(t, m, cmd)   // the poll
}

func typed(m Model, s string) Model {
	for _, r := range s {
		m, _ = m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	return m
}

func enter(m Model) (Model, tea.Cmd) {
	return m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
}

func content(m Model) string { return ansi.Strip(m.Content()) }

func TestChatRendersEachKindOfEntry(t *testing.T) {
	f := &fake{}
	f.queued(result{u: Update{LogID: "a", Next: 4, Entries: []Entry{
		{Message: "what's left?"},
		{Activity: "Read the plan"},
		{Reply: "One **question** is open."},
		{Problem: "claude exited"},
	}}})
	m, _ := open(t, newChat(f).WithPlace("p", ""))
	view := content(m)
	for _, want := range []string{"chat · p", "what's left?", "✓ Read the plan", "One question is open.", "claude exited"} {
		assert.Check(t, cmp.Contains(view, want))
	}
}

func TestChatSendsTheTextWithThePlacesContext(t *testing.T) {
	f := &fake{}
	m, _ := open(t, newChat(f).WithPlace("p", "[plan] p1 t"))
	m = typed(m, "hi")
	m, cmd := enter(m)
	assert.Equal(t, m.input.Value(), "")
	assert.Check(t, cmp.Contains(content(m), "thinking…"))
	m, _ = step(t, m, cmd)
	assert.DeepEqual(t, f.sent, [][2]string{{"hi", "[plan] p1 t"}})
	assert.Check(t, !strings.Contains(content(m), "thinking…"))
}

func TestChatEmptyInputSendsNothing(t *testing.T) {
	f := &fake{}
	m, _ := open(t, newChat(f))
	m = typed(m, "   ")
	m, cmd := enter(m)
	_, _ = step(t, m, cmd)
	assert.Check(t, cmp.Len(f.sent, 0))
}

func TestChatSendErrorKeepsTheText(t *testing.T) {
	f := &fake{sendErr: errors.New("daemon said no")}
	m, _ := open(t, newChat(f))
	m = typed(m, "hi")
	m, cmd := enter(m)
	m, _ = step(t, m, cmd)
	assert.Check(t, cmp.Contains(content(m), "daemon said no"))
	assert.Equal(t, m.input.Value(), "hi")
}

func TestChatOpenPollsFromZeroAndCloseStops(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{}
		f.queued(result{u: Update{LogID: "a", Next: 2, Entries: []Entry{{Message: "one"}, {Reply: "two"}}}})
		m, cmd := open(t, newChat(f))
		assert.Equal(t, f.lastAfter(t), 0)
		m, cmd = round(t, m, cmd)
		assert.Equal(t, f.lastAfter(t), 2)

		// The tick after a poll that was in flight at Close is stale.
		m = m.Close()
		n := f.polls()
		m, cmd = step(t, m, cmd)
		_, cmd = step(t, m, cmd)
		assert.Check(t, cmd == nil)
		assert.Equal(t, f.polls(), n)
	})
}

func TestChatShowsEntriesFromAnotherWindow(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{}
		f.queued(
			result{u: Update{LogID: "a", Next: 0}},
			result{u: Update{LogID: "a", Next: 1, Busy: true, Entries: []Entry{{Message: "sent elsewhere"}}}},
		)
		m, cmd := open(t, newChat(f))
		m, _ = round(t, m, cmd)
		view := content(m)
		assert.Check(t, cmp.Contains(view, "sent elsewhere"))
		assert.Check(t, cmp.Contains(view, "thinking…"))
	})
}

func TestChatNewLogIDReadsAgainFromZero(t *testing.T) {
	seqs := func(n int, prefix string) []Entry {
		es := make([]Entry, n)
		for i := range es {
			es[i] = Entry{Reply: prefix + string(rune('0'+i))}
		}
		return es
	}
	for _, tc := range []struct {
		name    string
		results []result
		// rounds is how many poll results to drive after the first.
		rounds int
		afters []int
		shown  []string
		hidden []string
	}{
		{
			name: "restart",
			results: []result{
				{u: Update{LogID: "a", Next: 3, Entries: seqs(3, "old")}},
				{u: Update{LogID: "b", Next: 5, Entries: seqs(1, "stray")}},
				{u: Update{LogID: "b", Next: 2, Entries: seqs(2, "new")}},
			},
			rounds: 1,
			// The restart polls from 0 at once, and then from that
			// result's Next.
			afters: []int{0, 3, 0, 2},
			shown:  []string{"new0", "new1"},
			hidden: []string{"old", "stray"},
		},
		{
			name: "first chat",
			results: []result{
				{u: Update{LogID: "", Next: 0}},
				{u: Update{LogID: "a", Next: 1, Entries: seqs(1, "first")}},
			},
			rounds: 1,
			afters: []int{0, 0, 1},
			shown:  []string{"first0"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				f := &fake{}
				f.queued(tc.results...)
				m, cmd := open(t, newChat(f))
				for range tc.rounds {
					m, cmd = round(t, m, cmd)
				}
				if tc.name == "restart" {
					// The new LogID's poll from 0, issued with no tick.
					m, cmd = step(t, m, cmd)
				}
				m, _ = round(t, m, cmd)
				f.mu.Lock()
				afters := f.afters
				f.mu.Unlock()
				assert.DeepEqual(t, afters, tc.afters)
				view := content(m)
				for _, s := range tc.shown {
					assert.Check(t, cmp.Equal(strings.Count(view, s), 1), "%q in:\n%s", s, view)
				}
				for _, s := range tc.hidden {
					assert.Check(t, !strings.Contains(view, s), "%q in:\n%s", s, view)
				}
			})
		})
	}
}

func TestChatReopenShowsNoEntryTwice(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		two := Update{LogID: "a", Next: 2, Entries: []Entry{{Message: "question"}, {Reply: "answer"}}}
		f := &fake{}
		f.queued(result{u: two}, result{u: two})
		m, _ := open(t, newChat(f))
		m = m.Close()
		m, _ = open(t, m)
		assert.Equal(t, f.lastAfter(t), 0)
		view := content(m)
		assert.Check(t, cmp.Equal(strings.Count(view, "question"), 1), view)
		assert.Check(t, cmp.Equal(strings.Count(view, "answer"), 1), view)
	})
}

func TestChatFailedPollKeepsTheConversation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{}
		f.queued(
			result{u: Update{LogID: "a", Next: 1, Entries: []Entry{{Reply: "kept"}}}},
			result{err: errors.New("connection refused")},
			result{u: Update{LogID: "a", Next: 1}},
		)
		m, cmd := open(t, newChat(f))
		m, cmd = round(t, m, cmd)
		view := content(m)
		assert.Check(t, cmp.Contains(view, "kept"))
		assert.Check(t, cmp.Contains(view, unreachable))

		// The chain goes on after a failure, from the same cursor.
		m, _ = round(t, m, cmd)
		assert.Equal(t, f.lastAfter(t), 1)
		view = content(m)
		assert.Check(t, cmp.Contains(view, "kept"))
		assert.Check(t, !strings.Contains(view, unreachable), view)
	})
}

func TestChatUnavailableTakesNoInput(t *testing.T) {
	m := Unavailable("open chunk watch in a repository").SetSize(80, 30)
	before := m.Content()
	assert.Check(t, cmp.Contains(ansi.Strip(before), "open chunk watch in a repository"))
	m, cmd := m.Open()
	assert.Check(t, cmd == nil)
	m = typed(m, "hi")
	m, cmd = enter(m)
	assert.Check(t, cmd == nil)
	assert.Equal(t, m.Content(), before)
}
