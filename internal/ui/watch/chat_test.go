package watch

import (
	"context"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/chat"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/flowlist"
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
	assert.Assert(t, m.chatOpen, "ctrl+o did not open the chat")
	if cmd == nil {
		return m
	}
	return deliver(t, m, cmd)
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
	{ID: "b2", Title: "Retry\nuploads", State: "Planning - Agent"},
}

func TestCtrlOOpensTheChatFromEachScreen(t *testing.T) {
	for _, closer := range []tea.KeyPressMsg{ctrlO, esc} {
		t.Run("Flows, closed by "+closer.String(), func(t *testing.T) {
			m := chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...)
			m, _ = pressKey(m, tea.KeyPressMsg{Code: tea.KeyDown})
			before := screen(m)
			assert.Assert(t, cmp.Contains(before, "▶ Retry uploads"))

			m = openChat(t, m)
			assert.Check(t, cmp.Contains(screen(m), "chat · "))
			m, _ = pressKey(m, closer)
			assert.Check(t, !m.chatOpen && m.flowsOpen)
			assert.Check(t, cmp.Equal(screen(m), before))
		})
		t.Run("a plan screen, closed by "+closer.String(), func(t *testing.T) {
			m := chatModel(t, &fakePlans{plan: "# Retry uploads"}, &fakeChat{})
			m = openFlow(t, m, "p1", "Retry uploads")
			key := m.planKey
			before := screen(m)

			m = openChat(t, m)
			assert.Check(t, cmp.Contains(screen(m), "chat · "))
			m, _ = pressKey(m, closer)
			assert.Check(t, !m.chatOpen && m.planOpen)
			assert.Check(t, cmp.Equal(m.planKey, key))
			assert.Check(t, cmp.Equal(screen(m), before))
		})
	}
}

func TestChatHeaderNamesThePlace(t *testing.T) {
	m := chatModel(t, &fakePlans{}, &fakeChat{}, twoFlows...)
	m = openChat(t, m)
	assert.Check(t, cmp.Contains(screen(m), "chat · Flows"))
	m, _ = pressKey(m, esc)

	m = openFlow(t, m, "p1", "Retry uploads")
	m = openChat(t, m)
	assert.Check(t, cmp.Contains(screen(m), "chat · Retry uploads"))
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

		m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Poll()))
		flow := m.planScreen.FlowText()
		assert.Assert(t, flow != "", "the poll read no flow")
		m = say(t, openChat(t, m), "hi")
		assert.Check(t, cmp.Equal(c.lastContext(t), "[plan] p1 Title\n[flow] "+flow))
	})
	t.Run("Flows", func(t *testing.T) {
		c := &fakeChat{}
		m := chatModel(t, &fakePlans{}, c, twoFlows...)
		say(t, openChat(t, m), "hi")
		assert.Check(t, cmp.Equal(c.lastContext(t), "[flows]\na1 Review - Human Alpha\nb2 Planning - Agent Retry uploads"))
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
	p := &fakePlans{plan: "# Retry uploads\n\nRetry transient failures."}
	m := chatModel(t, p, &fakeChat{})
	m = openFlow(t, m, "p1", "Retry uploads")
	m = deliver(t, m, workspaceCmd(m.planKey, m.planScreen.Poll()))
	m, _ = pressKey(m, tea.KeyPressMsg{Code: 'c', Text: "c"})
	m = typeInto(m, "half a comment")
	before := screen(m)
	assert.Assert(t, cmp.Contains(before, "half a comment"))

	m = openChat(t, m)
	m = typeInto(m, "a question")
	m, _ = pressKey(m, esc)
	assert.Check(t, !m.chatOpen && m.planOpen)
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
		assert.Check(t, m.chatOpen, "%s closed the chat", k.String())
		assert.Check(t, m.flowsOpen && !m.planOpen && !m.planOpening && !m.HasPlan(), "%s left Flows", k.String())
	}
	assert.Check(t, cmp.Contains(screen(m), "qn"), "the letters were not typed into the chat:\n%s", screen(m))

	_, cmd := pressKey(m, tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl})
	assert.Assert(t, cmd != nil, "ctrl+c did nothing")
	_, quits := cmd().(tea.QuitMsg)
	assert.Check(t, quits, "ctrl+c did not quit")
}

func TestChatPollsOnlyWhileOpen(t *testing.T) {
	c := &fakeChat{}
	m := chatModel(t, &fakePlans{}, c)
	_, polls := c.calls()
	assert.Assert(t, cmp.Equal(polls, 0), "polled before it was opened")

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

	m, _ = pressKey(m, esc)
	// The ticks the open chat asked for arrive after it closed.
	for _, follow := range follows {
		if follow == nil {
			continue
		}
		next, cmd := m.Update(follow())
		m = next.(Model)
		assert.Check(t, cmd == nil, "a stale tick did something")
	}
	_, polls = c.calls()
	assert.Check(t, cmp.Equal(polls, 1), "it polled while closed")
}

func TestQuittingLeavesTheChatRunning(t *testing.T) {
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
	t.Run("after closing it", func(t *testing.T) {
		c := &fakeChat{}
		m := say(t, openChat(t, chatModel(t, &fakePlans{}, c)), "hi")
		m, _ = pressKey(m, esc)
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
	assert.Assert(t, m.chatOpen)
	assert.Check(t, cmp.Contains(screen(m), reason))
	m = typeInto(m, "hi")
	m, _ = pressKey(m, enter)
	assert.Check(t, !strings.Contains(screen(m), "hi"), screen(m))
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
