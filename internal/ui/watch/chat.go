package watch

import (
	"image/color"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/chat"
)

// WithChat lets ctrl+o open the chat newChat makes,
// made once on first open.
func (m Model) WithChat(newChat func() chat.Model) Model {
	m.newChat = newChat
	if m.flows != nil {
		*m.flows = m.flows.WithChatKey()
	}
	return m
}

// updateChat handles a key while the chat is open, or ctrl+o
// to open it; handled is false for a key the screens below should have.
func (m Model) updateChat(msg tea.KeyPressMsg) (_ Model, _ tea.Cmd, handled bool) {
	if m.newChat == nil {
		return m, nil, false
	}
	k := m.keys()
	if key.Matches(msg, k.Chat) {
		if m.chatOpen {
			return m.closeChat(), nil, true
		}
		next, cmd := m.openChat()
		return next, cmd, true
	}
	if !m.chatOpen {
		return m, nil, false
	}
	switch {
	case key.Matches(msg, k.CloseChat):
		return m.closeChat(), nil, true
	case key.Matches(msg, k.ForceQuit):
		// The dashboard's quit, which asks first on Flows when plans have
		// unsent input, so the question is shown.
		next, cmd := m.closeChat().quitWithPlans()
		if next.confirmPlanQuit && next.planOpen {
			note := next.note
			next, cmd = next.showFlows()
			next.note = note
		}
		return next, cmd, true
	}
	var cmd tea.Cmd
	*m.chatView, cmd = m.chatView.Update(msg)
	return m, cmd, true
}

// openChat shows the chat over the screen showing, which is
// kept as it is, and tells it where it was opened.
func (m Model) openChat() (Model, tea.Cmd) {
	if m.chatView == nil {
		c := m.newChat()
		if !m.hasDarkBG {
			c, _ = c.Update(tea.BackgroundColorMsg{Color: color.White})
		}
		m.chatView = &c
	}
	place, ctx := m.chatPlace()
	c, cmd := m.chatView.WithPlace(place, ctx).SetSize(m.width, m.height).Open()
	*m.chatView = c
	m.chatOpen = true
	return m, cmd
}

// closeChat goes back to the screen it was opened over. The chat stops
// polling, and its session runs on in the daemon.
func (m Model) closeChat() Model {
	if m.chatView != nil {
		*m.chatView = m.chatView.Close()
	}
	m.chatOpen = false
	return m
}

// chatPlace is the chat's header place and the context its
// messages carry, from the screen showing.
func (m Model) chatPlace() (place, context string) {
	switch {
	case m.planOpen && m.planScreen != nil:
		if m.planScreen.ID() == "" {
			return "New plan", ""
		}
		title := oneLine(m.planScreen.Title())
		ctx := "[plan] " + m.planScreen.ID() + " " + title
		if f := m.planScreen.FlowText(); f != "" {
			ctx += "\n[flow] " + f
		}
		return title, ctx
	case m.flowsOpen && m.flows != nil:
		lines := []string{"[flows]"}
		for _, f := range m.flows.Flows() {
			lines = append(lines, f.ID+" "+f.State+" "+oneLine(f.Title))
		}
		if len(lines) > 1 {
			return "Flows", strings.Join(lines, "\n")
		}
	}
	return "Flows", ""
}

// oneLine is s on one line, for a line of the chat's context.
func oneLine(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s))
}
