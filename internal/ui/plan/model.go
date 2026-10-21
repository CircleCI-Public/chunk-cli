// Package plan is the planning screen: a chat with Claude beside the plan
// Claude is writing, rendered as markdown. The developer can comment
// on any part of the plan; comments go to Claude with their next message.
// When the developer approves the plan it is handed to the factory to build.
//
// The screen is a component: its host passes it messages and draws what
// Content returns, and it reports that it is done with a ClosedMsg. Screen
// hosts it as a program of its own.
package plan

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/glamour/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// Turner sends the developer's message to the planning agent and returns its
// reply and the plan as it stands after the turn, reporting what the agent
// does to activity as it goes.
type Turner func(ctx context.Context, message string, activity func(string)) (reply, plan string, err error)

// Submitter hands an approved plan to the factory and returns the run's ID.
type Submitter func(plan string) (string, error)

// pane is which side of the screen the keyboard goes to.
type pane int

const (
	paneChat pane = iota
	panePlan
)

// message is one entry in the chat.
type message struct {
	fromUser bool
	text     string
	// comments are the plan comments a developer's message carried.
	comments []comment
	failed   bool
	// rendered is a reply's markdown as rendered at renderedWidth, kept so
	// the chat is not rendered again on every tick of the spinner.
	rendered      string
	renderedWidth int
}

// renderedBlock is a block of the plan and its lines as glamour renders it.
type renderedBlock struct {
	block
	lines []string
}

type (
	activityMsg string
	turnDoneMsg struct {
		reply, plan string
		err         error
	}
	submittedMsg struct {
		runID string
		err   error
	}
)

// ClosedMsg is the screen asking its host to close it: the plan was sent to
// the factory as run RunID, or, when RunID is "", the developer left.
type ClosedMsg struct {
	RunID string
}

func closed(runID string) tea.Cmd {
	return func() tea.Msg { return ClosedMsg{RunID: runID} }
}

// Model is the planning screen.
type Model struct {
	ctx    context.Context
	turn   Turner
	submit Submitter

	plan     string
	messages []message

	// blocks is renderedFrom, the plan as last rendered, split for
	// commenting and rendered at renderedWidth.
	blocks        []renderedBlock
	renderedFrom  string
	renderedWidth int
	// cursor is the block the plan pane has highlighted.
	cursor int
	// blockTop and blockEnd are each block's first line in the plan pane and
	// the line after its last.
	blockTop, blockEnd []int

	// comments are waiting to go with the next message; inFlight went with
	// the one Claude is answering, and come back if the turn fails.
	comments     []comment
	inFlight     []comment
	commenting   bool
	commentInput textinput.Model

	planView viewport.Model
	chatView viewport.Model
	input    textarea.Model
	spin     spinner.Model

	focus  pane
	width  int
	height int

	busy       bool
	cancelTurn context.CancelFunc
	activity   string
	activityCh chan string

	confirming bool
	// sending is set from the developer's y until the factory answers, so a
	// second send cannot start a second run.
	sending bool
	// quitting is set by a ctrl+c that would drop what the developer has not
	// sent; a second ctrl+c quits.
	quitting bool
	status   string

	// dark is whether the terminal's background is dark, and disambiguates
	// whether it tells shift+enter from enter.
	dark          bool
	disambiguates bool
	// back is whether the host keeps the screen to reopen once it is closed,
	// so leaving it loses nothing and stops nothing.
	back bool
	// RunID is the factory run the plan was sent to, once it has been.
	RunID string

	pending string
}

const (
	chatPlaceholder     = "Describe the change, or give feedback on the plan…"
	commentsPlaceholder = "Press enter to send your comments, or add a message…"
)

// New returns the planning screen. A non-empty first message is sent as soon
// as the screen opens.
func New(ctx context.Context, turn Turner, submit Submitter, first string) Model {
	in := textarea.New()
	in.Placeholder = chatPlaceholder
	in.ShowLineNumbers = false
	in.Prompt = "┃ "
	// Enter sends; a new line is shift+enter or ctrl+j.
	in.KeyMap.InsertNewline.SetKeys("shift+enter", "ctrl+j")
	in.SetHeight(3)
	in.Focus()

	ci := textinput.New()
	ci.Prompt = "💬 "
	ci.Placeholder = "Comment on the highlighted part (enter saves, esc cancels)"

	// The plan's keys are the screen's own, which move the cursor with the
	// view; the viewport's would scroll one without the other, and its d
	// and u would scroll where d deletes a comment.
	planView := viewport.New()
	planView.KeyMap = viewport.KeyMap{}

	return Model{
		ctx:          ctx,
		turn:         turn,
		submit:       submit,
		planView:     planView,
		chatView:     viewport.New(),
		input:        in,
		commentInput: ci,
		spin:         spinner.New(spinner.WithSpinner(spinner.Dot)),
		pending:      strings.TrimSpace(first),
		dark:         true,
	}
}

// WithTerminal returns a copy of m for a terminal with a dark background or
// not, that tells shift+enter from enter or not. The screen learns of a
// change to either from the messages its host passes on.
func (m Model) WithTerminal(dark, disambiguates bool) Model {
	m.dark, m.disambiguates = dark, disambiguates
	return m
}

// WithBack returns a copy of m for a host that keeps the screen when it is
// closed and can open it again as it was: esc, when there is nothing for it
// to stop or cancel, and ctrl+c close it without asking or stopping Claude.
func (m Model) WithBack() Model {
	m.back = true
	return m
}

func (m Model) Init() tea.Cmd {
	return textarea.Blink
}

func (m Model) Update(msg tea.Msg) (Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		if m.pending != "" {
			first := m.pending
			m.pending = ""
			return m.send(first)
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.updateKey(msg)

	case tea.KeyboardEnhancementsMsg:
		m.disambiguates = msg.SupportsKeyDisambiguation()
		return m, nil

	case tea.BackgroundColorMsg:
		if m.dark != msg.IsDark() {
			m.dark = msg.IsDark()
			m.restyle()
		}
		return m, nil

	case activityMsg:
		// A message that was already queued when the turn ended.
		if !m.busy {
			return m, nil
		}
		m.activity = string(msg)
		m.renderChat()
		return m, waitActivity(m.ctx, m.activityCh)

	case turnDoneMsg:
		m.busy, m.activity = false, ""
		if m.cancelTurn != nil {
			m.cancelTurn()
			m.cancelTurn = nil
		}
		switch {
		case errors.Is(msg.err, context.Canceled):
			m.messages = append(m.messages, message{text: "Stopped.", failed: true})
		case msg.err != nil:
			m.messages = append(m.messages, message{text: msg.err.Error(), failed: true})
		default:
			m.messages = append(m.messages, message{text: msg.reply})
		}
		// Comments Claude did not get to answer are the developer's to send
		// again, ahead of any written since.
		if msg.err != nil {
			m.comments = append(m.inFlight, m.comments...)
		}
		m.inFlight = nil
		if msg.plan != "" {
			m.plan = msg.plan
			m.renderPlan()
		}
		m.syncPlaceholder()
		m.renderChat()
		return m, nil

	case submittedMsg:
		m.sending = false
		if msg.err != nil {
			m.status = ui.ErrorStyle.Render("Could not send the plan: " + msg.err.Error())
			return m, nil
		}
		m.RunID = msg.runID
		return m, closed(msg.runID)

	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.renderChat()
		return m, cmd
	}

	return m.forward(msg)
}

func (m Model) updateKey(msg tea.KeyPressMsg) (Model, tea.Cmd) {
	k := m.keys()
	if key.Matches(msg, k.Quit) {
		return m.quit()
	}
	if m.quitting {
		m.quitting, m.status = false, ""
	}
	if m.confirming {
		m.confirming, m.status = false, ""
		if !key.Matches(msg, k.Confirm) {
			return m, nil
		}
		m.sending = true
		m.status = "Sending the plan to the factory…"
		return m, submitCmd(m.submit, m.plan)
	}
	if m.commenting {
		return m.updateCommentKey(msg)
	}

	switch {
	case key.Matches(msg, k.Stop):
		if m.cancelTurn != nil {
			m.cancelTurn()
		}
		return m, nil
	case key.Matches(msg, k.Focus):
		if m.focus == paneChat {
			m.focus = panePlan
			m.input.Blur()
			m.drawPlan()
			return m, nil
		}
		m.focus = paneChat
		m.drawPlan()
		return m, m.input.Focus()
	case key.Matches(msg, k.SendPlan):
		switch {
		case m.sending:
		case m.busy:
			m.status = warn("Wait for Claude to finish, or press esc to stop it.")
		case strings.TrimSpace(m.plan) == "":
			m.status = warn("There is no plan to send yet.")
		case len(m.comments) > 0:
			m.confirming = true
			m.status = ui.TitleStyle.Render(fmt.Sprintf("Your %d %s will not be sent to Claude. Send the plan to the factory anyway? (y/n)",
				len(m.comments), plural(len(m.comments), "comment", "comments")))
		default:
			m.confirming = true
			m.status = ui.TitleStyle.Render("Send this plan to the factory to build? (y/n)")
		}
		return m, nil
	}

	if m.focus == panePlan {
		return m.updatePlanKey(msg)
	}
	switch {
	case key.Matches(msg, k.ChatPageUp):
		m.chatView.PageUp()
		return m, nil
	case key.Matches(msg, k.ChatPageDown):
		m.chatView.PageDown()
		return m, nil
	}
	if key.Matches(msg, k.Send) {
		if m.busy {
			m.status = warn("Claude is still answering; your message is kept until it finishes.")
			return m, nil
		}
		text := strings.TrimSpace(m.input.Value())
		if text == "" && len(m.comments) == 0 {
			return m, nil
		}
		m.input.Reset()
		return m.send(text)
	}
	return m.forward(msg)
}

// quit closes the screen, unless that would drop comments or a message the
// developer has not sent, when it asks for a second ctrl+c first. A screen
// its host keeps loses nothing by closing, so it closes at once.
func (m Model) quit() (Model, tea.Cmd) {
	if m.back {
		return m, closed("")
	}
	if unsent := m.unsent(); unsent != "" && !m.quitting {
		m.quitting, m.confirming = true, false
		m.status = warn(fmt.Sprintf("Your %s will be lost. Press ctrl+c again to quit.", unsent))
		return m, nil
	}
	if m.cancelTurn != nil {
		m.cancelTurn()
	}
	return m, closed("")
}

// unsent names what the developer has written that has not gone to Claude,
// or is "" when there is nothing.
func (m Model) unsent() string {
	n := len(m.comments)
	if m.commenting && strings.TrimSpace(m.commentInput.Value()) != "" && m.commentAt(m.cursor) < 0 {
		n++
	}
	var parts []string
	if n > 0 {
		parts = append(parts, fmt.Sprintf("%d unsent %s", n, plural(n, "comment", "comments")))
	}
	if strings.TrimSpace(m.input.Value()) != "" {
		parts = append(parts, "unsent message")
	}
	return strings.Join(parts, " and ")
}

func warn(s string) string { return ui.WarningStyle.Render(s) }

// forward hands msg to the focused pane's component.
func (m Model) forward(msg tea.Msg) (Model, tea.Cmd) {
	var cmd tea.Cmd
	if m.focus == panePlan {
		m.planView, cmd = m.planView.Update(msg)
		return m, cmd
	}
	var cmds []tea.Cmd
	m.input, cmd = m.input.Update(msg)
	cmds = append(cmds, cmd)
	// The chat scrolls with the mouse wheel while typing.
	if _, ok := msg.(tea.KeyPressMsg); !ok {
		m.chatView, cmd = m.chatView.Update(msg)
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// send starts a turn with text, and any comments waiting, as the developer's
// message.
func (m Model) send(text string) (Model, tea.Cmd) {
	m.messages = append(m.messages, message{fromUser: true, text: text, comments: m.comments})
	prompt := composeMessage(text, m.comments)
	m.inFlight, m.comments = m.comments, nil
	m.status = ""
	ctx, cancel := context.WithCancel(m.ctx)
	m.busy, m.cancelTurn = true, cancel
	m.activityCh = make(chan string, 64)
	m.syncPlaceholder()
	m.drawPlan()
	m.renderChat()
	m.chatView.GotoBottom()
	return m, tea.Batch(turnCmd(ctx, m.turn, prompt, m.activityCh), waitActivity(ctx, m.activityCh), m.spin.Tick)
}

// syncPlaceholder tells the developer that enter sends their comments when
// there are some to send.
func (m *Model) syncPlaceholder() {
	if len(m.comments) > 0 {
		m.input.Placeholder = commentsPlaceholder
		return
	}
	m.input.Placeholder = chatPlaceholder
}

func turnCmd(ctx context.Context, turn Turner, text string, activity chan string) tea.Cmd {
	return func() tea.Msg {
		// Closing the channel ends the wait for activity with the turn.
		defer close(activity)
		reply, plan, err := turn(ctx, text, func(s string) {
			// Activity is only shown, so it is dropped rather than hold up
			// Claude when the screen falls behind.
			select {
			case activity <- s:
			default:
			}
		})
		return turnDoneMsg{reply: reply, plan: plan, err: err}
	}
}

func waitActivity(ctx context.Context, activity chan string) tea.Cmd {
	return func() tea.Msg {
		select {
		case s, ok := <-activity:
			if !ok {
				return nil
			}
			return activityMsg(s)
		case <-ctx.Done():
			return nil
		}
	}
}

func submitCmd(submit Submitter, plan string) tea.Cmd {
	return func() tea.Msg {
		id, err := submit(plan)
		return submittedMsg{runID: id, err: err}
	}
}

// Layout: the chat is on the left and the plan, which needs the room, takes
// the right 60% of the screen. Each pane has a one-cell border with its title
// set into the top edge; the plan has a row under it for comments, and the
// screen a footer row.
const (
	borderSize     = 2
	footerRows     = 1
	inputRows      = 3
	planFooterRows = 1
	planPercent    = 60
)

func (m *Model) planWidth() int { return m.width * planPercent / 100 }
func (m *Model) chatWidth() int { return m.width - m.planWidth() }

func (m *Model) layout() {
	inner := m.height - footerRows - borderSize
	m.planView.SetWidth(m.planWidth() - borderSize)
	m.planView.SetHeight(inner - planFooterRows)
	m.commentInput.SetWidth(m.planWidth() - borderSize - 4)
	m.chatView.SetWidth(m.chatWidth() - borderSize)
	m.chatView.SetHeight(inner - inputRows - 1)
	m.input.SetWidth(m.chatWidth() - borderSize)
	m.renderPlan()
	m.renderChat()
}

// newRenderer is a glamour renderer for the terminal's background that wraps
// at width.
func newRenderer(dark bool, width int) (*glamour.TermRenderer, error) {
	style := "light"
	if dark {
		style = "dark"
	}
	return glamour.NewTermRenderer(glamour.WithStandardStyle(style), glamour.WithWordWrap(width))
}

// renderMarkdown renders md with glamour at width, or returns it as typed
// when it cannot be rendered.
func renderMarkdown(md string, dark bool, width int) string {
	r, err := newRenderer(dark, width)
	if err != nil {
		return md
	}
	out, err := r.Render(md)
	if err != nil {
		return md
	}
	return strings.Trim(out, "\n")
}

// renderLines renders one block with r, without the blank lines glamour puts
// around a document.
func renderLines(r *glamour.TermRenderer, md string) []string {
	out, err := r.Render(md)
	if err != nil {
		return strings.Split(md, "\n")
	}
	lines := strings.Split(out, "\n")
	blank := func(s string) bool { return strings.TrimSpace(ansi.Strip(s)) == "" }
	for len(lines) > 0 && blank(lines[0]) {
		lines = lines[1:]
	}
	for len(lines) > 0 && blank(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// renderPlan splits the plan into blocks and renders each, when the plan or
// the pane's width has changed since it last did.
func (m *Model) renderPlan() {
	w := m.planView.Width() - gutterWidth
	if w <= 0 {
		return
	}
	if strings.TrimSpace(m.plan) == "" {
		m.blocks = nil
		m.planView.SetContent(ui.HelperStyle.Padding(1, 2).Width(m.planView.Width()).
			Render("No plan yet. Tell Claude what you want to change, and the plan it writes appears here."))
		return
	}
	if w == m.renderedWidth && m.blocks != nil && m.renderedFrom == m.plan {
		m.drawPlan()
		return
	}
	r, err := newRenderer(m.dark, w-1)
	if err != nil {
		m.planView.SetContent(m.plan)
		return
	}
	var cursorSource string
	if m.cursor < len(m.blocks) {
		cursorSource = m.blocks[m.cursor].source
	}
	blocks := splitBlocks(m.plan)
	m.blocks = make([]renderedBlock, len(blocks))
	for i, b := range blocks {
		m.blocks[i] = renderedBlock{block: b, lines: renderLines(r, b.source)}
	}
	m.renderedFrom, m.renderedWidth = m.plan, w
	m.reanchor()
	// The cursor stays on its block when Claude left that block as it was,
	// and otherwise on the same place in the plan as near as there is one.
	m.cursor = min(m.cursor, len(m.blocks)-1)
	for i, b := range m.blocks {
		if b.source == cursorSource {
			m.cursor = i
			break
		}
	}
	m.moveCursor(m.cursor)
}

func (m *Model) renderChat() {
	w := m.chatView.Width()
	if w <= 0 {
		return
	}
	wrap := lipgloss.NewStyle().Width(w)
	var b strings.Builder
	for i := range m.messages {
		msg := &m.messages[i]
		switch {
		case msg.fromUser:
			b.WriteString(youStyle.Render("You") + "\n")
			for _, c := range msg.comments {
				quote := ansi.Truncate(strings.SplitN(c.quote, "\n", 2)[0], w-4, "…")
				b.WriteString(ui.HelperStyle.Render("on "+quote) + "\n")
				b.WriteString(commentText.Width(w).Render("💬 "+c.text) + "\n")
			}
			if msg.text != "" {
				b.WriteString(wrap.Render(msg.text))
			}
		case msg.failed:
			b.WriteString(claudeStyle.Render("Claude") + "\n")
			b.WriteString(ui.ErrorStyle.Width(w).Render(msg.text))
		default:
			b.WriteString(claudeStyle.Render("Claude") + "\n")
			if msg.renderedWidth != w {
				msg.rendered, msg.renderedWidth = renderMarkdown(msg.text, m.dark, w-2), w
			}
			b.WriteString(msg.rendered)
		}
		b.WriteString("\n\n")
	}
	if m.busy {
		// The spinner's frames carry their own space after the dot.
		line := m.spin.View() + "Claude is thinking"
		if m.activity != "" {
			line += ": " + m.activity
		}
		b.WriteString(ui.HelperStyle.Width(w).MaxHeight(2).Render(line))
	}
	// The chat follows what is added to it unless the developer has
	// scrolled up to read, when it stays where they are.
	follow := m.chatView.AtBottom()
	m.chatView.SetContent(b.String())
	if follow {
		m.chatView.GotoBottom()
	}
}

// restyle renders the plan and the chat again for a change of background.
func (m *Model) restyle() {
	m.renderedWidth = 0
	for i := range m.messages {
		m.messages[i].renderedWidth = 0
	}
	m.renderPlan()
	m.renderChat()
}

var (
	// accentColor marks what has the keyboard: the focused pane's border,
	// the plan's cursor and the footer's keys.
	accentColor = lipgloss.Color("6")
	youStyle    = lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	claudeStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("5"))
)

// Content is the screen as its host draws it: the panes and the footer,
// filling the size it was last sent.
func (m Model) Content() string {
	if m.width == 0 {
		return ""
	}
	pane := func(focused bool, w int, title, content string) string {
		edge := lipgloss.Color("8")
		if focused {
			edge = accentColor
		}
		s := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(edge).
			Width(w).Height(m.height - footerRows)
		return titled(s.Render(content), title, lipgloss.NewStyle().Foreground(edge).Render)
	}

	planTitle := ui.TitleStyle.Render("Plan")
	if m.focus == panePlan {
		planTitle += ui.HelperStyle.Render(fmt.Sprintf("  %3.f%%", m.planView.ScrollPercent()*100))
	}
	chat := pane(m.focus == paneChat, m.chatWidth(), ui.TitleStyle.Render("Chat"),
		m.chatView.View()+"\n\n"+m.input.View())
	plan := pane(m.focus == panePlan, m.planWidth(), planTitle,
		m.planView.View()+"\n"+m.planFooter())

	footer := m.status
	if footer == "" {
		footer = newHelp(m.dark, m.width).View(m.keys())
	}
	// A footer that wraps would push the panes off the top of the screen.
	footer = ansi.Truncate(footer, m.width, "…")
	return lipgloss.JoinVertical(lipgloss.Left, lipgloss.JoinHorizontal(lipgloss.Top, chat, plan), footer)
}

// titled sets title into the top edge of box, a pane drawn with a rounded
// border, as in "╭─ Plan ─────╮", cut to leave at least one cell of edge
// after it. line draws the edge in the border's colour.
func titled(box, title string, line func(...string) string) string {
	top, rest, _ := strings.Cut(box, "\n")
	n := lipgloss.Width(top) - 2
	if n < 5 {
		return box
	}
	title = ansi.Truncate(title, n-4, "…")
	edge := line("╭─ ") + title + line(" "+strings.Repeat("─", max(n-3-lipgloss.Width(title), 0))+"╮")
	return edge + "\n" + rest
}
