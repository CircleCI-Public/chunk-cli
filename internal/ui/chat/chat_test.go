package chat

import (
	"context"
	"errors"
	"reflect"
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
	m := New(context.Background(), f.send, f.poll).SetSize(80, 30)
	// The tests are not to touch the clipboard of the machine they run on.
	m.copyText = tea.SetClipboard
	return m
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

func TestChatBlurKeepsPollingAndFocusTakesInputAgain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := &fake{}
		m, cmd := open(t, newChat(f))
		m = m.Blur()
		assert.Check(t, !m.input.Focused(), "Blur left the input focused")
		n := f.polls()
		m, _ = round(t, m, cmd)
		assert.Check(t, cmp.Equal(f.polls(), n+1), "a blurred chat stopped polling")

		m, _ = m.Focus()
		assert.Check(t, m.input.Focused(), "Focus did not focus the input")
		m = typed(m, "hi")
		assert.Check(t, cmp.Equal(m.input.Value(), "hi"))
	})
}

func TestUnavailableChatFocusDoesNothing(t *testing.T) {
	m, cmd := Unavailable("no repo").Focus()
	assert.Check(t, cmd == nil)
	assert.Check(t, !m.input.Focused())
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

// clipboardOf is the text a command puts on the clipboard, or "" when it
// puts none.
func clipboardOf(cmd tea.Cmd) string {
	if cmd == nil {
		return ""
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok && len(batch) > 0 && batch[0] != nil {
		msg = batch[0]()
	}
	// The message is tea's own, of an unexported type that is a string.
	if v := reflect.ValueOf(msg); v.Kind() == reflect.String {
		return v.String()
	}
	return ""
}

// rowOf is the screen row of the chat whose text contains s.
func rowOf(t *testing.T, m Model, s string) int {
	t.Helper()
	for y, l := range strings.Split(content(m), "\n") {
		if strings.Contains(l, s) {
			return y
		}
	}
	t.Fatalf("no row has %q:\n%s", s, content(m))
	return -1
}

func press(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func motion(x, y int) tea.MouseMotionMsg {
	return tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft}
}

func release(x, y int) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// dragged drags from one screen cell to another and returns the chat
// after the release, what it copied, and the command it answered with.
func dragged(m Model, fromX, fromY, toX, toY int) (Model, string, tea.Cmd) {
	m, _ = m.Update(press(fromX, fromY))
	m, _ = m.Update(motion(toX, toY))
	m, cmd := m.Update(release(toX, toY))
	return m, clipboardOf(cmd), cmd
}

// chatWith is an open chat showing entries.
func chatWith(t *testing.T, entries ...Entry) Model {
	f := &fake{}
	f.queued(result{u: Update{LogID: "a", Next: len(entries), Entries: entries}})
	m, _ := open(t, newChat(f))
	return m
}

func TestDraggingOverTheChatCopiesTheText(t *testing.T) {
	m := chatWith(t, Entry{Reply: "One question is open."})
	y := rowOf(t, m, "One question is open.")
	x := strings.Index(strings.Split(content(m), "\n")[y], "question")

	m, got, _ := dragged(m, x, y, x+7, y)

	assert.Check(t, cmp.Equal(got, "question"))
	assert.Check(t, cmp.Contains(content(m), "Copied 8 characters"))
}

func TestAClickWithoutADragInTheChatCopiesNothing(t *testing.T) {
	m := chatWith(t, Entry{Reply: "One question is open."})
	y := rowOf(t, m, "One question is open.")

	m, got, cmd := dragged(m, 5, y, 5, y)

	assert.Check(t, cmp.Equal(got, ""))
	assert.Check(t, cmd == nil)
	assert.Check(t, !strings.Contains(content(m), "Copied"))
}

func TestDraggingOverTheChatCopiesAMessageWithoutItsFrame(t *testing.T) {
	m := chatWith(t, Entry{Message: "give me a test"})
	y := rowOf(t, m, "give me a test")

	_, got, _ := dragged(m, 0, y-1, m.width-1, y+1)

	assert.Check(t, cmp.Equal(got, "give me a test"))
}

func TestAPressOnTheHeaderStatusOrInputBeginsNoSelection(t *testing.T) {
	m := chatWith(t, Entry{Reply: "One question is open."})
	// The status line is the row under the conversation's last.
	status := headerRows + m.view.Height()
	for _, y := range []int{0, status, m.height - 1} {
		_, got, _ := dragged(m, 3, y, 20, rowOf(t, m, "One question"))
		assert.Check(t, cmp.Equal(got, ""), "a drag from row %d copied %q", y, got)
	}
}

func TestTheChatSelectionIsDrawnAndAKeyOrClickDropsItAndTheNote(t *testing.T) {
	for name, drop := range map[string]func(Model) Model{
		"key":   func(m Model) Model { return typed(m, "x") },
		"click": func(m Model) Model { m, _ = m.Update(press(2, 1)); return m },
	} {
		t.Run(name, func(t *testing.T) {
			m := chatWith(t, Entry{Reply: "One question is open."})
			y := rowOf(t, m, "One question is open.")
			m, _, _ = dragged(m, 2, y, 8, y)
			assert.Check(t, cmp.Contains(m.Content(), "\x1b[7m"), "the selection was not drawn in reverse")

			m = drop(m)

			assert.Check(t, !strings.Contains(m.Content(), "\x1b[7m"))
			assert.Check(t, !strings.Contains(content(m), "Copied"))
		})
	}
}

func TestAPollKeepsTheChatSelectionUnlessItsLinesChange(t *testing.T) {
	m := chatWith(t, Entry{Reply: "One question is open."})
	y := rowOf(t, m, "One question is open.")
	m, _, _ = dragged(m, 2, y, 8, y)

	// A poll that adds an entry below leaves the selected lines as they were.
	m, _ = m.Update(polledMsg{seq: m.seq, after: m.next, update: Update{LogID: m.logID, Next: m.next + 1, Entries: []Entry{{Activity: "Read the plan"}}}})
	assert.Check(t, cmp.Contains(m.Content(), "\x1b[7m"), "a poll that added below dropped the selection")

	// A read from 0 of a different conversation changes them.
	m, _ = m.Update(polledMsg{seq: m.seq, after: 0, update: Update{LogID: m.logID, Next: 1, Entries: []Entry{{Reply: "Something else entirely."}}}})
	assert.Check(t, !strings.Contains(m.Content(), "\x1b[7m"), "the selection outlived its text")
}

func TestResizingTheChatDropsASelectionWhoseLinesRewrap(t *testing.T) {
	long := strings.Repeat("word ", 30)
	m := chatWith(t, Entry{Reply: long})
	y := rowOf(t, m, "word word")
	m, _, _ = dragged(m, 2, y, 8, y)

	m = m.SetSize(40, 30)

	assert.Check(t, !strings.Contains(m.Content(), "\x1b[7m"))
}

func TestTheCopyNoteShowsOverTheSpinnerAndEndsOnItsOwn(t *testing.T) {
	f := &fake{}
	f.queued(result{u: Update{LogID: "a", Next: 1, Busy: true, Activity: "Reading", Entries: []Entry{{Reply: "One question is open."}}}})
	m, _ := open(t, newChat(f))
	y := rowOf(t, m, "One question is open.")
	m, _, _ = dragged(m, 2, y, 8, y)
	assert.Check(t, cmp.Contains(content(m), "Copied"), "the note did not take over the busy status line")

	m, _ = m.Update(noteMsg{seq: m.noteSeq})

	assert.Check(t, !strings.Contains(content(m), "Copied"))
	assert.Check(t, cmp.Contains(content(m), "Reading"), "the spinner line did not come back")
}

func TestAnEarlierCopysExpiryDoesNotEndALaterNote(t *testing.T) {
	assert.Check(t, IsMsg(noteMsg{}))
	m := chatWith(t, Entry{Reply: "One question is open."})
	y := rowOf(t, m, "One question is open.")
	m, _, _ = dragged(m, 2, y, 8, y)
	first := m.noteSeq
	m = m.Close()
	m, _ = m.Open()
	m, _, _ = dragged(m, 2, y, 5, y)

	m, _ = m.Update(noteMsg{seq: first})

	assert.Check(t, cmp.Contains(content(m), "Copied 4 characters"))
}

func TestClosingTheChatDropsTheSelectionAndTheNote(t *testing.T) {
	m := chatWith(t, Entry{Reply: "One question is open."})
	y := rowOf(t, m, "One question is open.")
	m, _ = m.Update(press(2, y))
	m, _ = m.Update(motion(8, y))

	m = m.Close()
	m, cmd := m.Update(release(8, y))

	assert.Check(t, cmd == nil, "a release after closing copied")
	assert.Check(t, !strings.Contains(m.Content(), "\x1b[7m"))
}
