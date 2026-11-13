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

// updateChat handles a key while the chat has the keyboard, or ctrl+o to
// show it or give it the keyboard; handled is false for a key the screens
// should have.
func (m Model) updateChat(msg tea.KeyPressMsg) (_ Model, _ tea.Cmd, handled bool) {
	if m.newChat == nil {
		return m, nil, false
	}
	k := m.keys()
	if key.Matches(msg, k.Chat) {
		var cmd tea.Cmd
		switch {
		case !m.chatShown:
			m, cmd = m.showChat()
		case m.chatFocused:
			m, cmd = m.hideChat()
		default:
			m, cmd = m.focusChat()
		}
		return m, cmd, true
	}
	if !m.chatFocused {
		return m, nil, false
	}
	switch {
	case key.Matches(msg, k.Unfocus):
		if m.sideBySide() {
			return m.focusMain(), nil, true
		}
		next, cmd := m.hideChat()
		return next, cmd, true
	case key.Matches(msg, k.ForceQuit):
		next, cmd := m.quitWithPlans()
		if !next.confirmPlanQuit {
			return next, cmd, true // tea.Quit; the chat's session runs on in the daemon
		}
		// The question is asked on Flows: beside the chat, or instead of
		// one that covered the screen.
		var cmds []tea.Cmd
		if next.sideBySide() {
			next = next.focusMain()
		} else {
			next, cmd = next.hideChat()
			cmds = append(cmds, cmd)
		}
		if next.focus == focusPane {
			note := next.note
			next, cmd = next.showFlows()
			next.note = note
			cmds = append(cmds, cmd)
		}
		return next, tea.Batch(cmds...), true
	}
	var cmd tea.Cmd
	*m.chatView, cmd = m.chatView.Update(msg)
	return m, cmd, true
}

// showChat shows the chat beside the screen showing, which is kept as it
// is but narrowed, and gives it the keyboard.
func (m Model) showChat() (Model, tea.Cmd) {
	if m.chatView == nil {
		c := m.newChat()
		if !m.hasDarkBG {
			c, _ = c.Update(tea.BackgroundColorMsg{Color: color.White})
		}
		m.chatView = &c
	}
	place, ctx := m.chatPlace()
	c, open := m.chatView.WithPlace(place, ctx).Open()
	*m.chatView = c
	m.chatShown, m.chatFocused = true, true
	m, size := m.resize()
	return m, tea.Batch(open, size)
}

// hideChat gives the screen its width back. The chat stops polling, and
// its session runs on in the daemon.
func (m Model) hideChat() (Model, tea.Cmd) {
	*m.chatView = m.chatView.Close()
	m.chatShown, m.chatFocused = false, false
	return m.resize()
}

// focusChat gives the keyboard to the shown chat.
func (m Model) focusChat() (Model, tea.Cmd) {
	var cmd tea.Cmd
	*m.chatView, cmd = m.chatView.Focus()
	m.chatFocused = true
	return m, cmd
}

// focusMain gives the keyboard to the screen beside the chat, which keeps
// polling.
func (m Model) focusMain() Model {
	*m.chatView = m.chatView.Blur()
	m.chatFocused = false
	return m
}

// chatPlace is the chat's header place and the context its
// messages carry, from the screen showing.
func (m Model) chatPlace() (place, context string) {
	screen := m.planScreen()
	switch {
	case m.focus == focusPane && screen != nil:
		if screen.ID() == "" {
			return "New plan", ""
		}
		title := oneLine(screen.Title())
		ctx := "[plan] " + screen.ID() + " " + title
		if f := screen.FlowText(); f != "" {
			ctx += "\n[flow] " + f
		}
		return title, ctx
	case m.flows != nil:
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
