package watch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/chat"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/flowlist"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/onboarding"
)

var (
	ctrlO = tea.KeyPressMsg{Code: 'o', Mod: tea.ModCtrl}
	esc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
)

// fakeChat stands in for the daemon's chat session: it records what
// is sent, with its context, and each send shows in the conversation.
type fakeChat struct {
	mu      sync.Mutex
	sent    [][2]string
	afters  []int
	entries []chat.Entry
}

func (f *fakeChat) send(_ context.Context, text, context string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, [2]string{text, context})
	f.entries = append(f.entries, chat.Entry{Message: text})
	return nil
}

func (f *fakeChat) poll(_ context.Context, after int) (chat.Update, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.afters = append(f.afters, after)
	after = min(after, len(f.entries))
	return chat.Update{LogID: "log-1", Next: len(f.entries), Entries: append([]chat.Entry(nil), f.entries[after:]...)}, nil
}

// calls is how many sends and polls there have been.
func (f *fakeChat) calls() (sends, polls int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent), len(f.afters)
}

func (f *fakeChat) lastContext(t *testing.T) string {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	assert.Assert(t, len(f.sent) > 0, "nothing was sent")
	return f.sent[len(f.sent)-1][1]
}

// chatModel is a dashboard opened in /src/chunk-cli on a list of
// flows, whose ctrl+o opens a chat with c, read from the list.
func chatModel(t *testing.T, p *fakePlans, c *fakeChat, flows ...circleci.Flow) Model {
	t.Helper()
	m := New().WithConnection(chunkd.Connection{}).
		WithPlans(t.Context(), "/src/chunk-cli", p.planner).WithFlowList(flowList(t, flows...)).
		WithChat(func() chat.Model { return chat.New(t.Context(), c.send, c.poll) })
	m = sized(m)
	if len(flows) > 0 {
		m = deliverThen(t, m, m.flows.Init(), 1)
	}
	return m
}

// openFlow opens flow id's screen, as enter on the list does.
func openFlow(t *testing.T, m Model, id, title string) Model {
	t.Helper()
	next, cmd := m.Update(flowlist.OpenMsg{ID: id, Title: title})
	return deliver(t, next.(Model), cmd)
}

// openChat presses ctrl+o and delivers the first poll.
func openChat(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := pressKey(m, ctrlO)
	assert.Assert(t, m.chatShown && m.chatFocused, "ctrl+o did not show the chat with the keyboard")
	if cmd == nil {
		return m
	}
	return deliver(t, m, cmd)
}

// resized is m after the terminal reports w columns and 40 rows.
func resized(m Model, w int) Model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: 40})
	return next.(Model)
}

// assertRuleAt checks every one of the screen's 40 rows has the sidebar's
// rule at display column col.
func assertRuleAt(t *testing.T, m Model, col int) {
	t.Helper()
	rows := strings.Split(screen(m), "\n")
	assert.Assert(t, cmp.Len(rows, 40))
	for i, row := range rows {
		assert.Check(t, cmp.Equal(ansi.Cut(row, col, col+1), "│"), "row %d: %q", i, row)
	}
}

// ruleRows is how many of the screen's rows have a rule at column col.
func ruleRows(m Model, col int) int {
	n := 0
	for _, row := range strings.Split(screen(m), "\n") {
		if ansi.Cut(row, col, col+1) == "│" {
			n++
		}
	}
	return n
}

// say sends text from the open chat.
func say(t *testing.T, m Model, text string) Model {
	t.Helper()
	m = typeInto(m, text)
	m, cmd := pressKey(m, enter)
	return deliver(t, m, cmd)
}

var twoFlows = []circleci.Flow{
	{ID: "a1", Title: "Alpha", State: "Review - Human"},
	{ID: "b2", Title: "Retry\nuploads", State: "refine-proposal"},
}

func TestCtrlOShowsAndHidesTheChatOnEachScreen(t *testing.T) {
	t.Run("Flows", func(t *testing.T) {
		m := chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...)
		m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
		before := screen(m)
		assert.Assert(t, cmp.Contains(before, "▶ Retry uploads"))

		m = openChat(t, m)
		assert.Check(t, cmp.Contains(screen(m), "▶ Retry uploads"))
		assert.Check(t, cmp.Contains(screen(m), "chat · "))
		assert.Check(t, cmp.Equal(m.View().WindowTitle, "chunk watch — flows"))
		m, _ = pressKey(m, esc)
		assert.Check(t, m.chatShown, "esc hid the chat beside the screen")
		assert.Check(t, cmp.Equal(m.View().WindowTitle, "chunk watch — flows"))

		m, _ = pressKey(m, ctrlO)
		assert.Check(t, m.chatShown && m.chatFocused)
		m, _ = pressKey(m, ctrlO)
		assert.Check(t, !m.chatShown && !m.chatFocused && m.flowsOpen)
		assert.Check(t, cmp.Equal(screen(m), before))
	})
	t.Run("a plan screen", func(t *testing.T) {
		m := chatModel(t, &fakePlans{plan: "# Retry uploads"}, &fakeChat{})
		m = openFlow(t, m, "p1", "Retry uploads")
		key := m.planKey
		before := screen(m)

		m = openChat(t, m)
		assert.Check(t, cmp.Contains(screen(m), "chat · "))
		m, _ = pressKey(m, ctrlO)
		assert.Check(t, !m.chatShown && m.planOpen)
		assert.Check(t, cmp.Equal(m.planKey, key))
		assert.Check(t, cmp.Equal(screen(m), before))
	})
}

func TestTheChatSitsBesideTheScreen(t *testing.T) {
	m := openChat(t, chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...))
	m, _ = pressKey(m, esc)
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	assertRuleAt(t, m, 73)
	var left, right strings.Builder
	for _, row := range strings.Split(screen(m), "\n") {
		left.WriteString(ansi.Cut(row, 0, 73) + "\n")
		right.WriteString(ansi.Cut(row, 74, 110) + "\n")
	}
	assert.Check(t, cmp.Contains(left.String(), "▶ Retry uploads"))
	assert.Check(t, cmp.Contains(right.String(), "chat · Flows"))

	m = resized(m, 100)
	assertRuleAt(t, m, 63)
}

func TestEscGivesTheScreenTheKeyboardAndLeavesTheChat(t *testing.T) {
	m := openChat(t, chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...))
	m, _ = pressKey(m, esc)
	assert.Check(t, m.chatShown && !m.chatFocused)
	assert.Check(t, cmp.Contains(screen(m), "chat · "))

	assert.Check(t, !strings.Contains(screen(m), "▶ Retry uploads"))
	m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
	assert.Check(t, cmp.Contains(screen(m), "▶ Retry uploads"), "↓ did not reach the flow list")

	m, _ = pressKey(m, ctrlO)
	assert.Check(t, m.chatShown && m.chatFocused)
	m = typeInto(m, "qn")
	assert.Check(t, cmp.Contains(screen(m), "qn"), "the letters were not typed into the chat:\n%s", screen(m))

	m, _ = pressKey(m, ctrlO)
	assert.Check(t, !m.chatShown && !m.chatFocused)
}

func TestChatHeaderNamesThePlace(t *testing.T) {
	m := chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...)
	m = openChat(t, m)
	assert.Check(t, cmp.Contains(screen(m), "chat · Flows"))
	m, _ = pressKey(m, esc)

	// The place follows the screen beside the chat, without reopening it.
	m = openFlow(t, m, "p1", "Retry uploads")
	assert.Check(t, cmp.Contains(screen(m), "chat · Retry uploads"))
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
	assert.Check(t, m.flowsOpen)
	assert.Check(t, cmp.Contains(screen(m), "chat · Flows"))
}

func TestANarrowTerminalCoversTheScreenWithTheChat(t *testing.T) {
	t.Run("covering", func(t *testing.T) {
		m := resized(chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...), 90)
		m = openChat(t, m)
		assert.Check(t, cmp.Contains(screen(m), "chat · Flows"))
		assert.Check(t, !strings.Contains(screen(m), "Retry uploads"), screen(m))
		assert.Check(t, cmp.Equal(m.View().WindowTitle, "chunk watch — chat"))
		m, _ = pressKey(m, esc)
		assert.Check(t, !m.chatShown && !m.chatFocused)
	})
	t.Run("covering, the mouse", func(t *testing.T) {
		m := openChat(t, resized(chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...), 90))
		next, _ := m.Update(tea.MouseClickMsg{X: 0, Y: 5, Button: tea.MouseLeft})
		m = next.(Model)
		assert.Check(t, m.chatFocused)
		assert.Check(t, cmp.Equal(m.drag, dragChat))
	})
	t.Run("resized across the threshold", func(t *testing.T) {
		m := openChat(t, chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...))
		m, _ = pressKey(m, esc)
		assert.Assert(t, !m.chatFocused)
		m = resized(m, 90)
		assert.Check(t, m.chatShown && m.chatFocused, "the chat covers the screen without the keyboard")
		m = resized(m, 110)
		assert.Check(t, m.chatShown && m.chatFocused)
		assertRuleAt(t, m, 73)
	})
	for _, tc := range []struct {
		width   int
		beside  bool
		ruleCol int
	}{
		{
			width: 99,
		},
		{
			width:   100,
			beside:  true,
			ruleCol: 63,
		},
	} {
		t.Run(fmt.Sprintf("%d wide", tc.width), func(t *testing.T) {
			m := resized(chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...), tc.width)
			m = openChat(t, m)
			assert.Check(t, cmp.Contains(screen(m), "chat · Flows"))
			if !tc.beside {
				assert.Check(t, !strings.Contains(screen(m), "▶ Alpha"), screen(m))
				assert.Check(t, ruleRows(m, 63) < 40, "a rule beside a chat that covers the screen")
				return
			}
			assertRuleAt(t, m, tc.ruleCol)
			assert.Check(t, cmp.Contains(screen(m), "▶ Alpha"))
		})
	}
}

func TestChatContextFromEachScreen(t *testing.T) {
	t.Run("a plan screen", func(t *testing.T) {
		c := &fakeChat{}
		m := chatModel(t, &fakePlans{plan: "# Title"}, c)
		m = openFlow(t, m, "p1", "Title")

		// Before the screen has polled, it has no flow to tell.
		m = say(t, openChat(t, m), "hi")
		assert.Check(t, cmp.Equal(c.lastContext(t), "[plan] p1 Title"))
		m, _ = pressKey(m, esc)

		// A status read while the chat shows is in its next message.
		m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Poll()))
		flow := m.planScreen.FlowText()
		assert.Assert(t, flow != "", "the poll read no flow")
		m, _ = pressKey(m, ctrlO)
		assert.Assert(t, m.chatFocused)
		m = say(t, m, "hi")
		assert.Check(t, cmp.Equal(c.lastContext(t), "[plan] p1 Title\n[flow] "+flow))
	})
	t.Run("Flows", func(t *testing.T) {
		c := &fakeChat{}
		m := chatModel(t, &fakePlans{}, c, twoFlows...)
		say(t, openChat(t, m), "hi")
		assert.Check(t, cmp.Equal(c.lastContext(t), "[flows]\na1 Review - Human Alpha\nb2 refine-proposal Retry uploads"))
	})
	t.Run("Flows with none read", func(t *testing.T) {
		c := &fakeChat{}
		m := chatModel(t, &fakePlans{}, c)
		say(t, openChat(t, m), "hi")
		assert.Check(t, cmp.Equal(c.lastContext(t), ""))
	})
	t.Run("a new plan's screen", func(t *testing.T) {
		c := &fakeChat{}
		m := openPlanScreen(t, chatModel(t, &fakePlans{}, c))
		assert.Assert(t, m.planOpen && m.planScreen.ID() == "")
		m = openChat(t, m)
		assert.Check(t, cmp.Contains(screen(m), "chat · New plan"))
		say(t, m, "hi")
		assert.Check(t, cmp.Equal(c.lastContext(t), ""))
	})
}

func TestCtrlOKeepsWhatIsBeingTyped(t *testing.T) {
	p := &fakePlans{plan: "# Retry uploads\n\n## 1. Outcome\n\nRetry transient failures."}
	m := chatModel(t, p, &fakeChat{})
	m = openFlow(t, m, "p1", "Retry uploads")
	m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Poll()))
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeInto(m, "half a comment")
	before := screen(m)
	assert.Assert(t, cmp.Contains(before, "half a comment"))

	m = openChat(t, m)
	m = typeInto(m, "a question")
	m, _ = pressKey(m, ctrlO)
	assert.Check(t, !m.chatShown && m.planOpen)
	assert.Check(t, cmp.Equal(screen(m), before))
	assert.Check(t, cmp.Equal(m.planScreen.Unsent(), "1 unsent comment"))
	assert.Check(t, cmp.Equal(p.requests, 0), "the plan's agent was sent something")
}

func TestOpenChatTakesTheKeyboard(t *testing.T) {
	m := chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...)
	m = openChat(t, m)
	for _, k := range []tea.KeyPressMsg{
		{Code: 'q', Text: "q"}, {Code: tea.KeyTab}, {Code: 'p', Mod: tea.ModCtrl}, {Code: 'n', Text: "n"},
	} {
		m, _ = pressKey(m, k)
		assert.Check(t, m.chatShown && m.chatFocused, "%s took the keyboard from the chat", k.String())
		assert.Check(t, m.flowsOpen && !m.planOpen && !m.planOpening && !m.HasPlan(), "%s left Flows", k.String())
	}
	assert.Check(t, cmp.Contains(screen(m), "qn"), "the letters were not typed into the chat:\n%s", screen(m))

	_, cmd := pressKey(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	assert.Assert(t, cmd != nil, "ctrl+c did nothing")
	_, quits := cmd().(tea.QuitMsg)
	assert.Check(t, quits, "ctrl+c did not quit")
}

// shownChat is a dashboard whose chat with c was just shown, with its first
// poll delivered, and what the chat asked for next.
func shownChat(t *testing.T, c *fakeChat) (Model, []tea.Cmd) {
	t.Helper()
	m := chatModel(t, &fakePlans{}, c)
	_, polls := c.calls()
	assert.Assert(t, cmp.Equal(polls, 0), "polled before it was shown")

	m, open := pressKey(m, ctrlO)
	batch, ok := open().(tea.BatchMsg)
	assert.Assert(t, ok)
	var follows []tea.Cmd
	for _, cmd := range batch {
		next, follow := m.Update(cmd())
		m = next.(Model)
		follows = append(follows, follow)
	}
	_, polls = c.calls()
	assert.Assert(t, cmp.Equal(polls, 1))
	return m, follows
}

func TestChatPollsOnlyWhileShown(t *testing.T) {
	t.Run("hidden", func(t *testing.T) {
		c := &fakeChat{}
		m, follows := shownChat(t, c)
		m, _ = pressKey(m, ctrlO)
		assert.Assert(t, !m.chatShown)
		// The ticks the shown chat asked for arrive after it was hidden.
		for _, follow := range follows {
			if follow == nil {
				continue
			}
			next, cmd := m.Update(follow())
			m = next.(Model)
			assert.Check(t, cmd == nil, "a stale tick did something")
		}
		_, polls := c.calls()
		assert.Check(t, cmp.Equal(polls, 1), "it polled while hidden")
	})
	t.Run("beside the screen without the keyboard", func(t *testing.T) {
		c := &fakeChat{}
		m, follows := shownChat(t, c)
		m, _ = pressKey(m, esc)
		assert.Assert(t, m.chatShown && !m.chatFocused)
		for _, follow := range follows {
			if follow == nil {
				continue
			}
			next, cmd := m.Update(follow())
			m = next.(Model)
			if cmd != nil {
				m = deliver(t, m, cmd)
			}
		}
		_, polls := c.calls()
		assert.Check(t, polls > 1, "an unfocused sidebar stopped polling")
	})
}

// withUnsentComment is a dashboard on plan p1's screen, with a comment
// typed and not sent.
func withUnsentComment(t *testing.T, c *fakeChat) Model {
	t.Helper()
	p := &fakePlans{plan: "# Retry uploads\n\n## 1. Outcome\n\nRetry transient failures."}
	m := chatModel(t, p, c)
	m = openFlow(t, m, "p1", "Retry uploads")
	m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Poll()))
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeInto(m, "half a comment")
	assert.Assert(t, cmp.Equal(m.planScreen.Unsent(), "1 unsent comment"))
	return m
}

func TestQuittingLeavesTheChatRunning(t *testing.T) {
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	t.Run("with unsent input", func(t *testing.T) {
		m := openChat(t, withUnsentComment(t, &fakeChat{}))
		m, _ = pressKey(m, ctrlC)
		assert.Check(t, m.confirmPlanQuit && m.flowsOpen && m.chatShown && !m.chatFocused)
		assert.Check(t, cmp.Contains(screen(m), "unsent input"))
	})
	t.Run("with unsent input, narrow", func(t *testing.T) {
		m := openChat(t, resized(withUnsentComment(t, &fakeChat{}), 90))
		m, _ = pressKey(m, ctrlC)
		assert.Check(t, !m.chatShown && m.confirmPlanQuit && m.flowsOpen)
		assert.Check(t, cmp.Contains(screen(m), "unsent input"))
	})
	t.Run("beside the screen", func(t *testing.T) {
		c := &fakeChat{}
		m := say(t, openChat(t, chatModel(t, &fakePlans{}, c)), "hi")
		m, _ = pressKey(m, esc)
		sends, polls := c.calls()
		m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'q', Text: "q"})
		assert.Assert(t, cmd != nil, "q did nothing")
		next, cmd := m.Update(cmd())
		assert.Assert(t, cmd != nil, "the flow list's quit did nothing")
		_, quits := cmd().(tea.QuitMsg)
		assert.Assert(t, quits)
		s, p := c.calls()
		assert.Check(t, cmp.Equal(s, sends))
		assert.Check(t, cmp.Equal(p, polls))
		assert.Check(t, next.(Model).chatView != nil)
	})
	t.Run("while open", func(t *testing.T) {
		c := &fakeChat{}
		m := say(t, openChat(t, chatModel(t, &fakePlans{}, c)), "hi")
		sends, polls := c.calls()
		m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
		_, quits := cmd().(tea.QuitMsg)
		assert.Assert(t, quits)
		s, p := c.calls()
		assert.Check(t, cmp.Equal(s, sends))
		assert.Check(t, cmp.Equal(p, polls))
		assert.Check(t, m.chatView != nil)
	})
	t.Run("after hiding it", func(t *testing.T) {
		c := &fakeChat{}
		m := say(t, openChat(t, chatModel(t, &fakePlans{}, c)), "hi")
		m, _ = pressKey(m, ctrlO)
		sends, polls := c.calls()
		next, cmd := m.Update(flowlist.QuitMsg{})
		_, quits := cmd().(tea.QuitMsg)
		assert.Assert(t, quits)
		s, p := c.calls()
		assert.Check(t, cmp.Equal(s, sends))
		assert.Check(t, cmp.Equal(p, polls))
		assert.Check(t, next.(Model).chatView != nil)
	})
}

func TestChatUnavailable(t *testing.T) {
	const reason = "Open chunk watch in a repository to use chat."
	m := New().WithConnection(chunkd.Connection{}).
		WithPlans(t.Context(), "", (&fakePlans{}).planner).WithFlowList(flowList(t)).
		WithChat(func() chat.Model { return chat.Unavailable(reason) })
	m = sized(m)
	m, cmd := pressKey(m, ctrlO)
	assert.Check(t, cmd == nil)
	assert.Assert(t, m.chatShown && m.chatFocused)
	// Wrapped to the sidebar.
	assert.Check(t, cmp.Contains(strings.Join(strings.Fields(ansi.Strip(m.chatView.Content())), " "), reason))
	assert.Check(t, cmp.Contains(screen(m), "chunk watch in a"))
	m = typeInto(m, "hi")
	m, _ = pressKey(m, enter)
	assert.Check(t, !strings.Contains(screen(m), "hi"), screen(m))

	// It does not poll with the keyboard or without it.
	m, cmd = pressKey(m, esc)
	assert.Check(t, cmd == nil)
	assert.Check(t, m.chatShown && !m.chatFocused)
	m, cmd = pressKey(m, ctrlO)
	assert.Check(t, cmd == nil)
	assert.Check(t, m.chatFocused)
}

// The footers offer ctrl+o only where it opens something.
func TestChatKeyHints(t *testing.T) {
	m := chatModel(t, &fakePlans{plan: "# A plan"}, &fakeChat{})
	assert.Check(t, cmp.Contains(screen(m), "ctrl+o chat"))
	m = openFlow(t, m, "p1", "A plan")
	assert.Check(t, cmp.Contains(screen(m), "ctrl+o chat"))

	plain := openPlanScreen(t, planModel(t, &fakePlans{}))
	assert.Check(t, !strings.Contains(screen(plain), "ctrl+o"), screen(plain))
	_, cmd := pressKey(plain, ctrlO)
	assert.Check(t, cmd == nil)
}

func TestADragInTheOpenChatSelectsItsText(t *testing.T) {
	// The conversation is there before the chat opens, so its first poll shows it.
	c := &fakeChat{entries: []chat.Entry{{Reply: "One question is open."}}}
	m := openChat(t, chatModel(t, &fakePlans{}, c, twoFlows...))
	y := -1
	for i, l := range strings.Split(screen(m), "\n") {
		if strings.Contains(l, "One question is open.") {
			y = i
		}
	}
	assert.Assert(t, y >= 0, "the message is not shown")
	assert.Assert(t, !strings.Contains(m.View().Content, "\x1b[7m"), "text is selected before the drag")

	next, _ := m.Update(tea.MouseClickMsg{X: m.mainWidth() + 1, Y: y, Button: tea.MouseLeft})
	next, _ = next.(Model).Update(tea.MouseMotionMsg{X: m.width - 1, Y: y, Button: tea.MouseLeft})

	assert.Check(t, cmp.Contains(next.(Model).View().Content, "\x1b[7m"), "the drag did not select the chat's text")
}

// click is m after a left press and release at x, y.
func click(t *testing.T, m Model, x, y int) Model {
	t.Helper()
	next, _ := m.Update(tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft})
	next, _ = next.(Model).Update(tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseLeft})
	return next.(Model)
}

// rowOf is the screen row showing text.
func rowOf(t *testing.T, m Model, text string) int {
	t.Helper()
	for i, l := range strings.Split(screen(m), "\n") {
		if strings.Contains(l, text) {
			return i
		}
	}
	t.Fatalf("%q is not shown:\n%s", text, screen(m))
	return -1
}

// halves is the screen's columns left of col, and those right of it.
func halves(m Model, col int) (left, right string) {
	var l, r strings.Builder
	for _, row := range strings.Split(screen(m), "\n") {
		l.WriteString(ansi.Cut(row, 0, col) + "\n")
		r.WriteString(ansi.Cut(row, col+1, m.width) + "\n")
	}
	return l.String(), r.String()
}

func TestAClickGivesItsPaneTheKeyboard(t *testing.T) {
	m := openChat(t, chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...))
	edge := m.mainWidth()
	m = click(t, m, edge, 5) // the rule
	assert.Check(t, m.chatFocused, "a click on the rule moved the keyboard")
	m = click(t, m, 5, 5)
	assert.Check(t, !m.chatFocused, "a click on the screen left the chat the keyboard")
	m = click(t, m, edge+5, 5)
	assert.Check(t, m.chatFocused, "a click in the chat did not give it the keyboard")
}

func TestADragStaysWithThePaneItBeganIn(t *testing.T) {
	t.Run("from the screen", func(t *testing.T) {
		m := openChat(t, chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...))
		edge := m.mainWidth()
		next, _ := m.Update(tea.MouseClickMsg{X: 5, Y: 5, Button: tea.MouseLeft})
		m = next.(Model)
		assert.Check(t, cmp.Equal(m.drag, dragMain))
		next, _ = m.Update(tea.MouseMotionMsg{X: edge + 10, Y: 5, Button: tea.MouseLeft})
		next, _ = next.(Model).Update(tea.MouseReleaseMsg{X: edge + 10, Y: 5, Button: tea.MouseLeft})
		m = next.(Model)
		assert.Check(t, cmp.Equal(m.drag, dragNone))
		assert.Check(t, !m.chatFocused, "a drag from the screen gave the chat the keyboard")
		assert.Check(t, !strings.Contains(m.chatView.Content(), "\x1b[7m"), "a drag from the screen selected chat text")
	})
	t.Run("from the chat over the screen", func(t *testing.T) {
		c := &fakeChat{entries: []chat.Entry{{Reply: "One question is open."}}}
		m := openChat(t, chatModel(t, &fakePlans{}, c, twoFlows...))
		edge := m.mainWidth()
		y := rowOf(t, m, "One question is open.")
		next, _ := m.Update(tea.MouseClickMsg{X: edge + 5, Y: y, Button: tea.MouseLeft})
		next, _ = next.(Model).Update(tea.MouseMotionMsg{X: 2, Y: y, Button: tea.MouseLeft})
		next, _ = next.(Model).Update(tea.MouseReleaseMsg{X: 2, Y: y, Button: tea.MouseLeft})
		m = next.(Model)
		assert.Check(t, m.chatFocused)
		assert.Check(t, cmp.Contains(m.View().Content, "\x1b[7m"), "the drag did not select the chat's text")
		assert.Check(t, cmp.Contains(screen(m), "▶ Alpha"), "the drag moved the flow list's selection")
	})
	t.Run("hidden mid-drag", func(t *testing.T) {
		m := openChat(t, chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...))
		edge := m.mainWidth()
		next, _ := m.Update(tea.MouseClickMsg{X: edge + 5, Y: 5, Button: tea.MouseLeft})
		m = next.(Model)
		assert.Check(t, cmp.Equal(m.drag, dragChat))
		m, _ = pressKey(m, ctrlO)
		next, _ = m.Update(tea.MouseReleaseMsg{X: 5, Y: 5, Button: tea.MouseLeft})
		m = next.(Model)
		assert.Check(t, !m.chatShown)
		assert.Check(t, cmp.Equal(m.drag, dragNone))
		assert.Check(t, m.flowsOpen)
	})
	t.Run("resized mid-drag", func(t *testing.T) {
		m := openChat(t, chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...))
		edge := m.mainWidth()
		next, _ := m.Update(tea.MouseClickMsg{X: edge + 5, Y: 5, Button: tea.MouseLeft})
		next, _ = next.(Model).Update(tea.WindowSizeMsg{Width: 150, Height: 40})
		next, _ = next.(Model).Update(tea.MouseReleaseMsg{X: 2, Y: 5, Button: tea.MouseLeft})
		m = next.(Model)
		assert.Check(t, cmp.Equal(m.drag, dragNone))
		assert.Check(t, !strings.Contains(m.chatView.Content(), "\x1b[7m"))
	})
}

func TestAWheelScrollsThePaneUnderThePointer(t *testing.T) {
	// A wheel over the main screen is checked only to leave the chat as it
	// was.
	var entries []chat.Entry
	for i := range 60 {
		entries = append(entries, chat.Entry{Reply: fmt.Sprintf("line %d", i)})
	}
	var flows []circleci.Flow
	for i := range 60 {
		flows = append(flows, circleci.Flow{ID: fmt.Sprintf("f%d", i), Title: fmt.Sprintf("Flow %d", i), State: "refine-proposal"})
	}
	m := openChat(t, chatModel(t, &fakePlans{}, &fakeChat{entries: entries}, flows...))
	m, _ = pressKey(m, esc)
	edge := m.mainWidth()
	wheel := func(m Model, x int, b tea.MouseButton) Model {
		for range 5 {
			next, _ := m.Update(tea.MouseWheelMsg{X: x, Y: 10, Button: b})
			m = next.(Model)
		}
		return m
	}

	_, right := halves(m, edge)
	m = wheel(m, 5, tea.MouseWheelDown)
	left, r := halves(m, edge)
	assert.Check(t, cmp.Equal(r, right), "a wheel over the screen scrolled the chat")

	m = wheel(m, edge+5, tea.MouseWheelUp)
	l, r := halves(m, edge)
	assert.Check(t, r != right, "a wheel over the chat did not scroll it")
	assert.Check(t, cmp.Equal(l, left), "a wheel over the chat changed the screen")
	left, right = l, r

	m = wheel(m, edge, tea.MouseWheelUp)
	l, r = halves(m, edge)
	assert.Check(t, cmp.Equal(l, left), "a wheel on the rule changed the screen")
	assert.Check(t, cmp.Equal(r, right), "a wheel on the rule scrolled the chat")
	assert.Check(t, !m.chatFocused, "the wheel gave the chat the keyboard")
}

func TestAPasteGoesToThePaneWithTheKeyboard(t *testing.T) {
	t.Run("Flows", func(t *testing.T) {
		m := openChat(t, chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...))
		next, _ := m.Update(tea.PasteMsg{Content: "pasted"})
		m = next.(Model)
		assert.Check(t, cmp.Contains(screen(m), "pasted"))
		m, _ = pressKey(m, esc)
		next, _ = m.Update(tea.PasteMsg{Content: "zz"})
		assert.Check(t, !strings.Contains(screen(next.(Model)), "zz"), "a paste reached the chat without the keyboard")
	})
	t.Run("a plan screen", func(t *testing.T) {
		m := openChat(t, openPlanScreen(t, chatModel(t, &fakePlans{}, &fakeChat{})))
		m, _ = pressKey(m, esc)
		next, _ := m.Update(tea.PasteMsg{Content: "zz"})
		m = next.(Model)
		assert.Check(t, cmp.Contains(screen(m), "zz"), "the paste did not reach the new plan's input")
		assert.Check(t, !strings.Contains(m.chatView.Content(), "zz"), "a paste reached the chat without the keyboard")
	})
}

// onboardingChatModel is a dashboard on onboarding, 110 wide, whose ctrl+o
// opens a chat with c.
func onboardingChatModel(t *testing.T, c *fakeChat) Model {
	t.Helper()
	m := chatModel(t, &fakePlans{}, c).WithOnboarding(fakeOnboarding(t, onboarding.State{}))
	return resized(m, 110)
}

func TestOnboardingBesideTheChat(t *testing.T) {
	down := tea.KeyPressMsg{Code: tea.KeyDown}
	first, second := "Add keyboard shortcuts", "Export a table to CSV"
	tests := []struct {
		name string
		run  func(t *testing.T, m Model)
	}{
		{
			name: "narrowed beside the chat, and widened again",
			run: func(t *testing.T, m Model) {
				m = openChat(t, m)
				assertRuleAt(t, m, 73)
				left, _ := halves(m, 73)
				assert.Check(t, cmp.Contains(left, "What should we change first?"))
				assert.Check(t, cmp.Equal(m.onboarding.Width(), 73))
				for i, row := range strings.Split(m.onboarding.Content(), "\n") {
					w := ansi.StringWidth(row)
					assert.Check(t, w <= 73, "row %d is wider than the main pane: %q", i, row)
				}
				m, _ = pressKey(m, ctrlO)
				assert.Check(t, cmp.Equal(m.chatShown, false))
				assert.Check(t, cmp.Equal(m.onboarding.Width(), 110))
			},
		},
		{
			name: "keys go to the chat while it has the keyboard",
			run: func(t *testing.T, m Model) {
				m = openChat(t, m)
				m = typeInto(m, "q")
				m, _ = pressKey(m, down)
				assert.Check(t, cmp.Contains(m.chatView.Content(), "q"))
				assert.Check(t, cmp.Equal(onboardingFocus(m), first), "a key for the chat reached onboarding")
				m, _ = pressKey(m, esc)
				m, _ = pressKey(m, down)
				assert.Check(t, cmp.Equal(onboardingFocus(m), second), "↓ did not reach onboarding")
			},
		},
		{
			name: "a click on the main pane gives onboarding the keyboard",
			run: func(t *testing.T, m Model) {
				m = click(t, openChat(t, m), 5, 5)
				assert.Check(t, cmp.Equal(m.chatFocused, false))
				m, _ = pressKey(m, down)
				assert.Check(t, cmp.Equal(onboardingFocus(m), second))
			},
		},
		{
			name: "the wheel scrolls the timeline under the pointer",
			run: func(t *testing.T, m Model) {
				// A terminal shorter than the timeline, so it scrolls.
				next, _ := m.Update(tea.WindowSizeMsg{Width: 110, Height: 8})
				m = openChat(t, next.(Model))
				m, _ = pressKey(m, esc)
				firstRow := func(m Model) string {
					left, _ := halves(m, 73)
					return strings.Split(left, "\n")[1]
				}
				before := firstRow(m)
				next, _ = m.Update(tea.MouseWheelMsg{X: 80, Y: 3, Button: tea.MouseWheelDown})
				m = next.(Model)
				assert.Check(t, cmp.Equal(firstRow(m), before), "a wheel over the chat scrolled onboarding")
				next, _ = m.Update(tea.MouseWheelMsg{X: 5, Y: 3, Button: tea.MouseWheelDown})
				m = next.(Model)
				after := firstRow(m)
				assert.Check(t, after != before, "a wheel over onboarding did not scroll it")
			},
		},
		{
			name: "the chat's header reads Set up",
			run: func(t *testing.T, m Model) {
				m = openChat(t, m)
				assert.Check(t, cmp.Contains(screen(m), "chat · Set up"))
			},
		},
		{
			name: "a narrow terminal covers onboarding with the chat",
			run: func(t *testing.T, m Model) {
				m = openChat(t, resized(m, 90))
				s := screen(m)
				assert.Check(t, !strings.Contains(s, "What should we change first?"), s)
				m, _ = pressKey(m, down)
				assert.Check(t, cmp.Equal(onboardingFocus(m), first), "a key reached the covered onboarding")
			},
		},
		{
			name: "ctrl+c asks first over unsent input",
			run: func(t *testing.T, m Model) {
				for range 3 {
					m, _ = pressKey(m, down)
				}
				m = typeInto(m, "add a theme")
				unsent := m.onboarding.Unsent()
				assert.Assert(t, unsent != "", "nothing was typed into onboarding")
				m, cmd := pressKey(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
				assert.Check(t, cmp.Equal(m.confirmPlanQuit, true))
				assert.Check(t, cmp.Nil(cmd), "ctrl+c quit over unsent input")
				assert.Check(t, cmp.Contains(screen(m), "ctrl+c again"))
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.run(t, onboardingChatModel(t, &fakeChat{}))
		})
	}
}
