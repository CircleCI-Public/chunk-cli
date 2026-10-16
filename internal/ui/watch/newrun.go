package watch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// Draft is Claude's draft of a change, made on the developer's machine in a
// factory run's worktree: the run, what Claude said it did, and the diff it
// left, summarized per file and in full. Stat is "" when it changed nothing.
type Draft struct {
	RunID   string
	Summary string
	Stat    string
	Diff    string
}

// Drafter has Claude draft prompt in the project at root, reporting what it
// does to activity. It returns the draft as far as it got, with an error, when
// drafting fails or ctx is cancelled. Errors are shown as typed, so they
// should read as a sentence.
type Drafter func(ctx context.Context, root, prompt string, activity func(string)) (Draft, error)

// Sender hands a draft's run to the factory to refine and returns the factory
// run's session ID.
type Sender func(root, runID string) (string, error)

// draftState is where a prompt typed after n has got to.
type draftState int

const (
	// draftNone: no prompt is open and no draft is being made or shown.
	draftNone draftState = iota
	// draftPrompting: the keyboard goes to the prompt.
	draftPrompting
	// draftWorking: Claude is drafting.
	draftWorking
	// draftReady: the draft is shown, to send to the factory or keep.
	draftReady
)

// draftActivityMsg is something Claude did while drafting.
type draftActivityMsg string

// draftDoneMsg is the end of drafting.
type draftDoneMsg struct {
	draft Draft
	err   error
}

// runStartedMsg reports the outcome of sending a draft to the factory.
type runStartedMsg struct {
	id    string
	label string
	err   error
}

// WithDrafts returns a copy of m on which n opens a prompt for Claude to draft
// with draft, and the draft can be sent to the factory with send. Drafts are
// in dir's project, the one the dashboard was opened in, or the selected
// run's when dir is "". Drafting stops when ctx is cancelled.
func (m Model) WithDrafts(ctx context.Context, dir string, draft Drafter, send Sender) Model {
	m.draftCtx, m.startDir, m.drafter, m.sender = ctx, dir, draft, send
	return m
}

// startRoot is the project a draft made now would be in, "" when there is
// none to make one in.
func (m Model) startRoot() string {
	if m.startDir != "" {
		return m.startDir
	}
	if run := m.selectedRun(); run != nil {
		return run.s.ProjectRoot
	}
	return ""
}

// updateDraftKey handles keys while a prompt is open or a draft is being made
// or shown. Each of those owns the keyboard, so no other key does anything.
func (m Model) updateDraftKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	k := m.keys()
	if msg.String() == "ctrl+c" {
		return m, tea.Quit
	}
	switch m.draft {
	case draftNone:
	case draftPrompting:
		return m.updatePromptKey(msg)
	case draftWorking:
		// Claude reports back once it has stopped, so what it did so far
		// is still there to look at.
		if key.Matches(msg, k.StopDraft) && m.draftCancel != nil {
			m.draftCancel()
			m.draftNote = "stopping Claude…"
		}
	case draftReady:
		switch {
		case key.Matches(msg, k.ViewDiff):
			return m.openText("Claude's draft", draftText(m.draftResult)), nil
		case key.Matches(msg, k.SendDraft):
			m.draft, m.draftNote = draftNone, "sending the draft to the factory…"
			return m, sendDraftCmd(m.sender, m.draftRoot, m.draftResult.RunID)
		case key.Matches(msg, k.KeepDraft):
			m.draft = draftNone
			if m.draftResult.Stat != "" {
				m.draftNote = "kept the draft; send it later with chunk factory build --continue " + m.draftResult.RunID
			}
		}
	}
	return m, nil
}

// updatePromptKey takes the prompt as it is typed. Enter has Claude draft it.
func (m Model) updatePromptKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.draft, m.prompt = draftNone, ""
		return m, nil
	case "enter":
		prompt := strings.TrimSpace(m.prompt)
		if prompt == "" {
			return m, nil
		}
		m.draftRoot, m.prompt = m.startRoot(), ""
		m.draft, m.draftLatest, m.draftNote = draftWorking, "", ""
		base := m.draftCtx
		if base == nil {
			base = context.Background()
		}
		ctx, cancel := context.WithCancel(base)
		m.draftCancel, m.draftWait = cancel, ctx
		m.draftActivity = make(chan string, 64)
		return m, tea.Batch(draftCmd(ctx, m.drafter, m.draftRoot, prompt, m.draftActivity), waitDraftActivity(ctx, m.draftActivity))
	case "backspace":
		if r := []rune(m.prompt); len(r) > 0 {
			m.prompt = string(r[:len(r)-1])
		}
		return m, nil
	}
	m.prompt += msg.Text
	return m, nil
}

func draftCmd(ctx context.Context, draft Drafter, root, prompt string, activity chan string) tea.Cmd {
	return func() tea.Msg {
		d, err := draft(ctx, root, prompt, func(s string) {
			// Activity is only shown, so it is dropped rather than hold up
			// Claude when the dashboard falls behind.
			select {
			case activity <- s:
			default:
			}
		})
		return draftDoneMsg{draft: d, err: err}
	}
}

func waitDraftActivity(ctx context.Context, activity chan string) tea.Cmd {
	return func() tea.Msg {
		select {
		case s := <-activity:
			return draftActivityMsg(s)
		case <-ctx.Done():
			return nil
		}
	}
}

func sendDraftCmd(send Sender, root, runID string) tea.Cmd {
	label := filepath.Base(root)
	return func() tea.Msg {
		id, err := send(root, runID)
		return runStartedMsg{id: id, label: label, err: err}
	}
}

// updateDraftMsg handles the messages of drafting and sending a draft: what
// is pasted into the prompt, what Claude does, and how drafting and sending
// ended.
func (m Model) updateDraftMsg(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		if m.draft == draftPrompting {
			m.prompt += msg.Content
		}
	case draftActivityMsg:
		return m.withDraftActivity(msg)
	case draftDoneMsg:
		return m.withDraftDone(msg), nil
	case runStartedMsg:
		return m.withRunStarted(msg), nil
	}
	return m, nil
}

// withDraftActivity shows what Claude is doing, while it is still drafting.
func (m Model) withDraftActivity(msg draftActivityMsg) (Model, tea.Cmd) {
	if m.draft != draftWorking {
		return m, nil
	}
	m.draftLatest = string(msg)
	return m, waitDraftActivity(m.draftWait, m.draftActivity)
}

// withDraftDone shows the draft, or why there is none.
func (m Model) withDraftDone(msg draftDoneMsg) Model {
	if m.draftCancel != nil {
		m.draftCancel()
		m.draftCancel = nil
	}
	m.draftResult = msg.draft
	stopped := errors.Is(msg.err, context.Canceled)
	switch {
	case msg.err != nil && !stopped:
		m.draft = draftNone
		m.draftNote = "Claude could not make a draft: " + msg.err.Error()
		if msg.draft.RunID != "" && msg.draft.Stat != "" {
			m.draftNote += "; what it did is kept in factory run " + msg.draft.RunID
		}
	case msg.draft.RunID == "":
		m.draft, m.draftNote = draftNone, ""
	default:
		m.draft, m.draftNote = draftReady, ""
	}
	return m
}

// withRunStarted notes how sending a draft went. The factory run is selected
// once a poll lists it, with the search cleared so it is not filtered out.
func (m Model) withRunStarted(msg runStartedMsg) Model {
	if msg.err != nil {
		m.draftNote = "could not send the draft to the factory: " + msg.err.Error()
		return m
	}
	m.draftNote = "sent the draft and your prompt to the factory in " + msg.label + "; its run appears in the list in a moment"
	m.pendingRun = msg.id
	m.focusedPane = paneLeft
	return m.setQuery("")
}

// selectPendingRun selects the factory run just started once a poll lists it.
func (m Model) selectPendingRun() Model {
	if m.pendingRun == "" {
		return m
	}
	for i, r := range m.sessions {
		if r.s.ID == m.pendingRun {
			m = m.selectRun(i)
			m.pendingRun, m.draftNote = "", ""
			return m
		}
	}
	return m
}

// draftSteps are a new run's steps, in order, as the footer's map of them
// names them.
var draftSteps = []string{
	"write a prompt",
	"Claude drafts the change",
	"send to the factory (last step)",
}

// draftLines is how many lines the footer gives a new run: which step it is
// on, the map of every step, and the prompt or what Claude is doing.
const draftLines = 3

// stepTitle says which step a new run is on and what that step does.
func stepTitle(n int) string {
	switch n {
	case 1:
		return "Step 1 of 3: write a prompt describing the change you want"
	case 2:
		return "Step 2 of 3: Claude is drafting the code change on your machine, in a copy of your repo"
	}
	return "Step 3 of 3 (last step): send Claude's draft and your prompt to the factory, which finishes the change"
}

// stepsMap is every step in order, with step n in brackets, so a new user can
// see what comes before and after and that sending to the factory is the end.
func stepsMap(n int) string {
	parts := make([]string, len(draftSteps))
	for i, name := range draftSteps {
		parts[i] = fmt.Sprintf("%d. %s", i+1, name)
		if i+1 == n {
			parts[i] = "[" + parts[i] + "]"
		}
	}
	return strings.Join(parts, " → ")
}

// draftFooter is the footer's lines for a new run: which step it is on, the
// map of every step in the accent colour, what the step needs or shows, and
// last the keys for that step (bar). They are as many lines as the footer's bar
// and draftLines.
func (m Model) draftFooter(st watchStyles, bar string) string {
	step, last := 1, ""
	switch m.draft {
	case draftNone:
		return ""
	case draftPrompting:
		last = m.promptField(st)
	case draftWorking:
		step = 2
		line := m.draftNote
		if line == "" {
			line = "Claude is working…"
			if m.draftLatest != "" {
				line += "  " + m.draftLatest
			}
		}
		last = st.muted(clip("› "+line, m.width-2))
	case draftReady:
		step = 3
		line := "Claude made no changes, so there is nothing to send"
		if m.draftResult.Stat != "" {
			line = "Draft ready: " + lastLine(m.draftResult.Stat) + " · press d to view the change"
		}
		last = st.muted(clip("› "+line, m.width-2))
	}
	return "  " + st.muted(clip(stepTitle(step), m.width-2)) + "\n" +
		"  " + st.teal(clip(stepsMap(step), m.width-2)) + "\n" +
		"  " + last + "\n" +
		"  " + bar + "\n"
}

// promptField is the prompt as it is typed, on a faint background across the
// footer's width, so where to type is easy to find without drawing a box
// that would take rows of its own.
func (m Model) promptField(st watchStyles) string {
	field := lipgloss.NewStyle().Background(lipgloss.LightDark(m.hasDarkBG)(lipgloss.Color("254"), lipgloss.Color("236")))
	width := max(m.width-4, 4)
	room := width - 2 - 1 // the "› " in front and the cursor
	var text string
	if m.prompt == "" {
		// Says what to type until something is, so an empty field is not
		// mistaken for a note.
		text = field.Inherit(st.Muted).Render("› ▏") + field.Inherit(st.VDim).Render(clip(promptPlaceholder, room))
	} else {
		text = field.Inherit(st.Muted).Render("› " + tailFit(strings.ReplaceAll(m.prompt, "\n", " "), room) + "▏")
	}
	if pad := width - ansi.StringWidth(text); pad > 0 {
		text += field.Render(strings.Repeat(" ", pad))
	}
	return text
}

// promptPlaceholder is what the empty prompt shows.
const promptPlaceholder = "type the change you want (e.g. add a --verbose flag)"

// draftText is what the diff viewer shows: what Claude said it did, then the
// summary and the whole diff.
func draftText(d Draft) string {
	var b strings.Builder
	if s := strings.TrimSpace(d.Summary); s != "" {
		b.WriteString(s + "\n\n")
	}
	b.WriteString(d.Stat + "\n\n" + d.Diff)
	return b.String()
}

// tailFit keeps the end of s within room columns, so the cursor stays in view
// however long a prompt grows.
func tailFit(s string, room int) string {
	room = max(room, 1)
	if r := []rune(s); len(r) > room {
		return "…" + string(r[len(r)-room+1:])
	}
	return s
}

// lastLine is the last line of s, which for git's --stat is the totals.
func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}
