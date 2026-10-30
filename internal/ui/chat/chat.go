// Package chat is a conversation with an agent: the developer's messages, the
// agent's activity and replies, and an input. It reads the conversation
// through a poll function and sends through a send function, so its host
// decides where the agent runs. Its host forwards the messages IsMsg reports
// as the chat's own, and renders Content.
package chat

import (
	"context"
	"strings"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/ui/theme"
)

// pollInterval is how long the chat waits after a poll's result before it
// polls again, while it is open.
const pollInterval = 200 * time.Millisecond

// unreachable is the note under the conversation while the last poll failed.
const unreachable = "can't reach the chunk daemon"

// Entry is one thing in the conversation, as the chat shows it. Exactly one
// field is set.
type Entry struct {
	Message, Activity, Reply, Problem string
}

// Update is what a poll read: the entries from the cursor asked for, as a
// chunkd.SessionLog has them.
type Update struct {
	Entries  []Entry
	Busy     bool
	Activity string
	// Next is the cursor to poll from for what comes after these entries.
	Next int
	// LogID names the conversation the entries are from; a new one means it
	// restarted.
	LogID string
}

// tickMsg asks for the next poll of chain seq.
type tickMsg struct{ seq int }

// polledMsg is the result of a poll of chain seq from after.
type polledMsg struct {
	seq    int
	after  int
	update Update
	err    error
}

// sentMsg is the result of sending text.
type sentMsg struct {
	text string
	err  error
}

// spinMsg is a tick of the chat's spinner while chain seq is current.
type spinMsg struct {
	seq  int
	tick tea.Msg
}

// IsMsg is whether msg is one of the chat's own, for a host to route to it.
func IsMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case tickMsg, polledMsg, sentMsg, spinMsg:
		return true
	}
	return false
}

// entry is an Entry with its reply rendered at width, so a reply is not
// rendered again on every poll.
type entry struct {
	Entry
	rendered string
	width    int
}

// Model is a chat with an agent: the conversation, an input, and the agent's
// activity while it works.
type Model struct {
	ctx  context.Context
	send func(ctx context.Context, text, context string) error
	poll func(ctx context.Context, after int) (Update, error)
	// reason is why the chat is not available, "" when it is.
	reason string

	place, context string

	entries []entry
	// logID and next are the conversation the entries are from and the
	// cursor to poll from next.
	logID string
	next  int
	// busy and activity are the agent's, as the last poll read them.
	busy     bool
	activity string
	// sending is how many of the chat's own sends wait on the daemon.
	sending int
	sendErr string
	// pollFailed is whether the last poll failed.
	pollFailed bool
	// seq is the current poll chain's; a result or tick of another is
	// stale, so bumping it ends the chain.
	seq int

	input  textarea.Model
	view   viewport.Model
	spin   spinner.Model
	width  int
	height int
	dark   bool
}

// New is a chat that sends with send, the developer's text and the place's
// context, and reads with poll. A poll with a new LogID means the
// conversation restarted, and the chat reads it again from 0.
func New(ctx context.Context, send func(ctx context.Context, text, context string) error,
	poll func(ctx context.Context, after int) (Update, error)) Model {
	in := textarea.New()
	in.Placeholder = "Ask about your plans…"
	in.ShowLineNumbers = false
	in.Prompt = ""
	in.KeyMap.InsertNewline.SetKeys("shift+enter", "ctrl+j")
	in.SetHeight(inputRows)
	inStyles := in.Styles()
	inStyles.Focused.CursorLine = lipgloss.NewStyle()
	in.SetStyles(inStyles)

	view := viewport.New()
	// The input has the keyboard; the conversation scrolls with the page
	// keys alone.
	view.KeyMap = viewport.KeyMap{PageDown: view.KeyMap.PageDown, PageUp: view.KeyMap.PageUp}

	return Model{
		ctx:   ctx,
		send:  send,
		poll:  poll,
		input: in,
		view:  view,
		spin:  spinner.New(spinner.WithSpinner(spinner.Dot)),
		dark:  true,
	}
}

// Unavailable is a chat that says why and takes no input.
func Unavailable(reason string) Model {
	m := New(context.Background(), nil, nil)
	m.reason = reason
	return m
}

// WithPlace sets the header's place and the context each message is sent with.
func (m Model) WithPlace(place, context string) Model {
	m.place, m.context = place, context
	return m
}

// Open starts polling from 0, so the whole conversation shows, and focuses
// the input. A host calls it each time the chat is shown.
func (m Model) Open() (Model, tea.Cmd) {
	if m.reason != "" {
		return m, nil
	}
	m.seq++
	m.input.Focus()
	return m, tea.Batch(m.pollFrom(0), m.spinTick())
}

// Close stops polling; the conversation shown is kept.
func (m Model) Close() Model {
	m.seq++
	m.input.Blur()
	return m
}

// SetSize lays the chat out for a w×h screen.
func (m Model) SetSize(w, h int) Model {
	m.width, m.height = w, h
	m.input.SetWidth(max(w-borderSize-2*padX, 1))
	m.view.SetWidth(w)
	m.view.SetHeight(max(h-headerRows-statusRows-inputRows-borderSize, 1))
	m.render()
	return m
}

const (
	headerRows = 1
	// statusRows is the line under the conversation for the agent at work.
	statusRows = 1
	inputRows  = 3
	borderSize = 2
	padX       = 1
	// bubbleChrome is a bubble's border and padding.
	bubbleChrome = borderSize + 2
)

func (m Model) pollFrom(after int) tea.Cmd {
	ctx, poll, seq := m.ctx, m.poll, m.seq
	return func() tea.Msg {
		u, err := poll(ctx, after)
		return polledMsg{seq: seq, after: after, update: u, err: err}
	}
}

func (m Model) spinTick() tea.Cmd {
	seq, tick := m.seq, m.spin.Tick
	return func() tea.Msg { return spinMsg{seq: seq, tick: tick()} }
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.dark = msg.IsDark()
		for i := range m.entries {
			m.entries[i].width = 0
		}
		m.render()
		return m, nil
	case tickMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		return m, m.pollFrom(m.next)
	case polledMsg:
		return m.polled(msg)
	case sentMsg:
		m.sending--
		if msg.err != nil {
			m.sendErr = msg.err.Error()
			// The text goes back in the input, before anything typed since.
			text := msg.text
			if v := m.input.Value(); v != "" {
				text += "\n" + v
			}
			m.input.SetValue(text)
		}
		m.render()
		return m, nil
	case spinMsg:
		if msg.seq != m.seq {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg.tick)
		seq := m.seq
		return m, func() tea.Msg {
			if cmd == nil {
				return nil
			}
			return spinMsg{seq: seq, tick: cmd()}
		}
	case tea.KeyPressMsg:
		return m.key(msg)
	case tea.PasteMsg:
		if m.reason != "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		return m, cmd
	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.view, cmd = m.view.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) polled(msg polledMsg) (Model, tea.Cmd) {
	if msg.seq != m.seq {
		return m, nil
	}
	next := tea.Tick(pollInterval, func(time.Time) tea.Msg { return tickMsg{seq: msg.seq} })
	if msg.err != nil {
		m.pollFailed = true
		m.render()
		return m, next
	}
	u := msg.update
	switch {
	case msg.after == 0:
		// A read from 0 is the whole conversation the daemon keeps.
		m.entries = nil
	case u.LogID != m.logID:
		// The conversation restarted, so its seqs are not the ones shown.
		m.entries, m.logID, m.next = nil, "", 0
		m.pollFailed = false
		m.render()
		return m, m.pollFrom(0)
	}
	for _, e := range u.Entries {
		m.entries = append(m.entries, entry{Entry: e})
	}
	m.logID, m.next = u.LogID, u.Next
	m.busy, m.activity = u.Busy, u.Activity
	m.pollFailed = false
	m.render()
	return m, next
}

func (m Model) key(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	if m.reason != "" {
		return m, nil
	}
	switch msg.String() {
	case "enter":
		text := strings.TrimSpace(m.input.Value())
		if text == "" {
			return m, nil
		}
		m.input.Reset()
		m.sending++
		m.sendErr = ""
		m.render()
		ctx, send, context := m.ctx, m.send, m.context
		return m, func() tea.Msg { return sentMsg{text: text, err: send(ctx, text, context)} }
	case "pgup", "pgdown":
		var cmd tea.Cmd
		m.view, cmd = m.view.Update(msg)
		return m, cmd
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// render lays the conversation out in the view, which follows what is added
// to it unless the developer has scrolled up to read.
func (m *Model) render() {
	w := m.view.Width() - 2*padX
	if w <= 0 {
		return
	}
	st := theme.New(m.dark)
	parts := make([]string, 0, len(m.entries)+2)
	for i := range m.entries {
		e := &m.entries[i]
		switch {
		case e.Message != "":
			parts = append(parts, m.bubble(e.Message, w))
		case e.Activity != "":
			parts = append(parts, st.Success.Render("✓ ")+st.Dim.Render(ansi.Truncate(e.Activity, w-2, "…")))
		case e.Reply != "":
			if e.width != w {
				e.rendered, e.width = RenderMarkdown(e.Reply, m.dark, w), w
			}
			parts = append(parts, e.rendered)
		case e.Problem != "":
			parts = append(parts, ui.ErrorStyle.Width(w).Render(e.Problem))
		}
	}
	if m.sendErr != "" {
		parts = append(parts, ui.ErrorStyle.Width(w).Render(m.sendErr))
	}
	if m.pollFailed {
		parts = append(parts, st.VDim.Render(unreachable))
	}
	follow := m.view.AtBottom()
	m.view.SetContent(lipgloss.NewStyle().Padding(0, padX).Render(strings.Join(parts, "\n\n")))
	if follow {
		m.view.GotoBottom()
	}
}

// bubble is text against the right edge of the conversation, in a rounded
// border, so it reads as the developer's side of it.
func (m Model) bubble(text string, w int) string {
	inner := max(w*85/100-bubbleChrome, 1)
	fit := min(lipgloss.Width(text), inner)
	b := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.New(m.dark).VDim.GetForeground()).
		Padding(0, 1).Width(fit + bubbleChrome).Render(text)
	return lipgloss.PlaceHorizontal(w, lipgloss.Right, b)
}

// status is the line under the conversation: the agent's activity with the
// spinner while it works or a send waits, and blank otherwise.
func (m Model) status() string {
	if !m.busy && m.sending == 0 {
		return ""
	}
	a := m.activity
	if a == "" || !m.busy {
		a = "thinking…"
	}
	// The spinner's frames carry their own space after the dot.
	line := m.spin.View() + a
	return theme.New(m.dark).Running.Render(ansi.Truncate(line, max(m.width-2*padX, 1), "…"))
}

func (m Model) header() string {
	h := "chat"
	if m.place != "" {
		h += " · " + m.place
	}
	return theme.New(m.dark).Brand.Bold(true).Render(ansi.Truncate(h, max(m.width, 1), "…"))
}

func (m Model) Content() string {
	if m.reason != "" {
		return m.header() + "\n\n" + theme.New(m.dark).Dim.Render(m.reason)
	}
	pad := lipgloss.NewStyle().Padding(0, padX)
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(theme.New(m.dark).VDim.GetForeground()).
		Padding(0, padX).Width(max(m.width, 1)).Render(m.input.View())
	return lipgloss.JoinVertical(lipgloss.Left, m.header(), m.view.View(), pad.Render(m.status()), box)
}

// NewRenderer is a glamour renderer for the terminal's background that wraps
// at width.
func NewRenderer(dark bool, width int) (*glamour.TermRenderer, error) {
	style := "light"
	if dark {
		style = "dark"
	}
	return glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(width))
}

// RenderMarkdown renders md with glamour at width, or returns it as typed
// when it cannot be rendered.
func RenderMarkdown(md string, dark bool, width int) string {
	r, err := NewRenderer(dark, width)
	if err != nil {
		return md
	}
	out, err := r.Render(md)
	if err != nil {
		return md
	}
	return strings.Trim(out, "\n")
}
