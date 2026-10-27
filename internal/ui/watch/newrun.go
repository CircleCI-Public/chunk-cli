package watch

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
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

// draftTickMsg redraws a draft's timer while Claude drafts.
type draftTickMsg struct{}

// draftTickEvery is how often a draft's timer is redrawn. Shortened in tests.
var draftTickEvery = time.Second

func draftTick() tea.Cmd {
	return tea.Tick(draftTickEvery, func(time.Time) tea.Msg { return draftTickMsg{} })
}

// clock is d as minutes and seconds, as a timer shows it.
func clock(d time.Duration) string {
	s := int(d.Round(time.Second) / time.Second)
	return fmt.Sprintf("%d:%02d", s/60, s%60)
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
		case key.Matches(msg, k.ScrollDraft):
			switch msg.String() {
			case "up", "k":
				return m.scrollDraft(-1), nil
			case "down", "j":
				return m.scrollDraft(1), nil
			case "pgup":
				return m.scrollDraft(-(m.contentHeight() - 2)), nil
			}
			return m.scrollDraft(m.contentHeight() - 2), nil
		case key.Matches(msg, k.ToggleSection):
			return m.toggleDraftSection(), nil
		case key.Matches(msg, k.FocusSection):
			return m.focusDraftSection(), nil
		case key.Matches(msg, k.SendDraft):
			m.draft, m.draftNote, m.sendingDraft = draftNone, "sending the draft to the factory…", true
			return m, sendDraftCmd(m.sender, m.draftRoot, m.draftResult.RunID)
		case key.Matches(msg, k.KeepDraft):
			m.draft, m.draftNote = draftNone, ""
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
		m.draftLog, m.draftScroll = nil, 0
		m = m.resetDraftSections()
		m.draftStarted, m.draftTook = time.Now(), 0
		base := m.draftCtx
		if base == nil {
			base = context.Background()
		}
		ctx, cancel := context.WithCancel(base)
		m.draftCancel, m.draftWait = cancel, ctx
		m.draftActivity = make(chan string, 64)
		return m, tea.Batch(draftCmd(ctx, m.drafter, m.draftRoot, prompt, m.draftActivity), waitDraftActivity(ctx, m.draftActivity), draftTick())
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
	case draftTickMsg:
		if m.draft == draftWorking {
			return m, draftTick()
		}
	}
	return m, nil
}

// withDraftActivity shows what Claude is doing, while it is still drafting.
func (m Model) withDraftActivity(msg draftActivityMsg) (Model, tea.Cmd) {
	if m.draft != draftWorking {
		return m, nil
	}
	m.draftLatest = string(msg)
	m.draftLog = append(m.draftLog, string(msg))
	if len(m.draftLog) > maxDraftLog {
		m.draftLog = m.draftLog[len(m.draftLog)-maxDraftLog:]
	}
	return m, waitDraftActivity(m.draftWait, m.draftActivity)
}

// withDraftDone shows the draft, or why there is none.
func (m Model) withDraftDone(msg draftDoneMsg) Model {
	if m.draftCancel != nil {
		m.draftCancel()
		m.draftCancel = nil
	}
	m.draftResult, m.draftDiff = msg.draft, &diffCache{}
	if !m.draftStarted.IsZero() {
		m.draftTook = time.Since(m.draftStarted)
	}
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
	m.sendingDraft = false
	if msg.err != nil {
		// The draft stays in view to send again, rather than being lost
		// with only a note that goes at the next key.
		m.draft = draftReady
		m.draftNote = "could not send the draft to the factory: " + msg.err.Error() + " · press enter to try again, or later with chunk factory build --continue " + m.draftResult.RunID
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
	"send to the factory",
}

// draftLines is how many lines the footer gives a new run: the map of every
// step, and the prompt or what Claude is doing.
const draftLines = 2

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

// stepHint is the one fact about step n that its name alone does not say:
// drafting does not touch the user's working tree, and sending to the
// factory is what finishes the change. Appended to the breadcrumb so the
// banner stays one line without losing either.
func stepHint(n int) string {
	switch n {
	case 2:
		return "on a copy of your repo"
	case 3:
		return "finishes the change"
	}
	return ""
}

// draftFooter is the footer's lines for a new run, pinned under the panes so
// they never scroll out of view: the map of every step with its stepHint, in
// the accent colour, what the step needs or shows, and last the keys for that
// step (bar) — each as a button when it is the one key to press next, dimmed
// otherwise, so no key is named twice. They are as many lines as the footer's
// bar and draftLines.
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
			line = "Claude is working… " + clock(time.Since(m.draftStarted))
			if m.draftLatest != "" {
				line += "  " + m.draftLatest
			}
		}
		last = st.muted(clip("› "+line, m.width-2))
	case draftReady:
		step = 3
		bar = clip(m.draftActionBar(st), m.width-2)
		if m.draftNote != "" {
			last = st.muted(clip("› "+m.draftNote, m.width-2))
		}
	}
	banner := stepsMap(step)
	if hint := stepHint(step); hint != "" {
		banner += "  ·  " + hint
	}
	return "  " + st.brand(clip(banner, m.width-2)) + "\n" +
		"  " + last + "\n" +
		"  " + bar + "\n"
}

// draftActionBar is the ready step's keys, named once, as buttons: the
// primary action (sending the draft to the factory) is filled in the accent
// colour, and the rest — keeping it for later, folding, switching and
// scrolling its sections — are dimmed and bracketed, so the one key to press
// next stands out from the others.
func (m Model) draftActionBar(st watchStyles) string {
	k := m.keys()
	if !k.SendDraft.Enabled() {
		return actionHint(st, false, k.KeepDraft)
	}
	return actionHint(st, true, k.SendDraft) + "   " +
		actionHint(st, false, k.KeepDraft) + "   " +
		actionHint(st, false, k.ToggleSection) + "   " +
		actionHint(st, false, k.FocusSection) + "   " +
		actionHint(st, false, k.ScrollDraft)
}

// actionHint names one key once: a filled button for the primary action, or a
// dimmed, bracketed key for a secondary one.
func actionHint(st watchStyles, primary bool, b key.Binding) string {
	h := b.Help()
	if primary {
		return st.Accent.Padding(0, 1).Render("⏎ " + h.Desc)
	}
	return st.vdim("[" + h.Key + "] " + h.Desc)
}

// promptField is the prompt as it is typed, on a faint background across the
// footer's width, so where to type is easy to find without drawing a box
// that would take rows of its own.
func (m Model) promptField(st watchStyles) string {
	field := st.Field
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

// handingOff reports whether the right pane shows a draft being sent to the
// factory: from enter until the factory's run is listed, when the pane shows
// the run instead, or until a key is pressed, which goes back to the dashboard
// as it was (the run is still selected once it is listed).
func (m Model) handingOff() bool {
	return m.draft == draftNone && m.draftResult.RunID != "" &&
		(m.sendingDraft || m.pendingRun != "" && m.draftNote != "")
}

// renderSentPane is the right pane while a draft is handed to the factory: it
// says so, and keeps the draft in view, rather than going back to whichever
// run was selected before until the new one is listed.
func (m Model) renderSentPane(st watchStyles, maxLines int) []string {
	head := st.vdim("draft  ") + st.muted(filepath.Base(m.draftRoot))
	if m.draftTook > 0 {
		head += st.vdim(" · ") + st.brand("drafted in "+m.draftTook.Round(time.Second).String())
	}
	status := []string{
		st.running("→ Sending the draft and your prompt to the factory…"),
		"",
	}
	if !m.sendingDraft {
		status = []string{
			st.success(ui.IconOK + " Sent to the factory: your draft and prompt are on their way"),
			st.muted("  Its run appears in the list in a moment."),
		}
	}
	lines := append([]string{head}, status...)
	body := m.draftBody(st, m.draftPaneWidth())
	if room := maxLines - len(lines); len(body) > room {
		body = body[:max(room, 0)]
	}
	return append(lines, body...)
}

// promptPlaceholder is what the empty prompt shows.
const promptPlaceholder = "type the change you want (e.g. add a --verbose flag)"

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

// maxDraftLog is how many of Claude's actions the draft pane keeps.
const maxDraftLog = 500

// draftPaneWidth is the right pane's width, as renderBody pads it.
func (m Model) draftPaneWidth() int { return max(m.width-leftPaneWidth-5, 10) }

// renderDraftPane is the right pane while a draft is made or shown: what
// Claude is doing as it drafts, then what it said and the whole change, so
// the draft is read where it is shown rather than behind a key.
func (m Model) renderDraftPane(st watchStyles, maxLines int) []string {
	project := filepath.Base(m.draftRoot)
	width := m.draftPaneWidth()
	if m.draft == draftWorking {
		log := m.draftLog
		if room := maxLines - 2; len(log) > room {
			log = log[len(log)-max(room, 0):]
		}
		lines := make([]string, 0, 2+len(log))
		lines = append(lines, st.vdim("draft  ")+st.muted("Claude is drafting in "+project+"… ")+st.brand(clock(time.Since(m.draftStarted))), "")
		for _, a := range log {
			lines = append(lines, st.muted("  "+a))
		}
		return lines
	}
	head := st.vdim("draft  ") + st.muted(project)
	if m.draftTook > 0 {
		head += st.vdim(" · ") + st.brand("drafted in "+m.draftTook.Round(time.Second).String())
	}
	body := m.draftBody(st, width)
	room := max(maxLines-draftPaneTop, 1)
	scroll := min(m.draftScroll, max(len(body)-room, 0))
	end := min(scroll+room, len(body))
	lines := append(append([]string{head}, m.draftAction(st)...), body[scroll:end]...)
	if end < len(body) {
		lines[len(lines)-1] = st.vdim("  ↓ more")
	}
	return lines
}

// draftPaneTop is how many lines the draft pane keeps above the draft, in
// view however far it is scrolled: its heading, then draftAction's status
// line. What to press next is said once, in the action bar pinned under the
// panes, not repeated here.
const draftPaneTop = 2

// draftAction says, in the pane beside the draft, what the draft is: ready to
// send, or empty. It is draftPaneTop-1 lines, short enough for a narrow pane.
func (m Model) draftAction(st watchStyles) []string {
	if m.draftResult.Stat == "" {
		return []string{st.warning("Claude made no changes, so there is nothing to send")}
	}
	return []string{st.success(ui.IconOK + " Draft ready: " + lastLine(m.draftResult.Stat))}
}

// draftSection is one of the two parts the draft is shown in.
type draftSection int

const (
	// sectionPlan is what Claude said it did, sectionChanges the diffstat and
	// diff it left.
	sectionPlan draftSection = iota
	sectionChanges
)

// resetDraftSections puts the sections back as a new draft first shows them.
func (m Model) resetDraftSections() Model {
	m.draftFocus, m.draftPlanShut, m.draftChangesOpen = sectionPlan, false, false
	return m
}

// draftSectionOpen reports whether sec shows its content.
func (m Model) draftSectionOpen(sec draftSection) bool {
	if sec == sectionPlan {
		return !m.draftPlanShut
	}
	return m.draftChangesOpen
}

// toggleDraftSection folds or unfolds the section in focus. Folding takes
// lines away, so the scroll is brought back within what is left.
func (m Model) toggleDraftSection() Model {
	if m.draftFocus == sectionPlan {
		m.draftPlanShut = !m.draftPlanShut
	} else {
		m.draftChangesOpen = !m.draftChangesOpen
	}
	return m.scrollDraft(0)
}

// focusDraftSection moves the focus to the other section, which is where tab
// and shift+tab both go with two. A draft that changed nothing has only its
// plan, and keys leaves FocusSection off for it.
func (m Model) focusDraftSection() Model {
	m.draftFocus = 1 - m.draftFocus
	return m
}

var statCounts = regexp.MustCompile(`(\d+) files? changed(?:, (\d+) insertions?\(\+\))?(?:, (\d+) deletions?\(-\))?`)

// statSummary is git's --stat totals as a short line, such as "1 file, +7 −2".
// Totals it cannot read are shown as git wrote them.
func statSummary(stat string) string {
	last := lastLine(stat)
	g := statCounts.FindStringSubmatch(last)
	if g == nil {
		return last
	}
	files := g[1] + " files"
	if g[1] == "1" {
		files = "1 file"
	}
	var counts []string
	if g[2] != "" {
		counts = append(counts, "+"+g[2])
	}
	if g[3] != "" {
		counts = append(counts, "−"+g[3])
	}
	if len(counts) == 0 {
		return files
	}
	return files + ", " + strings.Join(counts, " ")
}

// sectionHeader is a section's heading: a marker for whether it is open, its
// name, and, when folded, what it holds in a few words. The focused one is
// highlighted.
func (m Model) sectionHeader(st watchStyles, sec draftSection, name, folded string) string {
	text := foldHeader(m.draftSectionOpen(sec), name, folded)
	if m.draftFocus == sec {
		return st.Brand.Bold(true).Render(text)
	}
	return st.muted(text)
}

// draftBody is the draft as the pane shows it, in two sections: the plan,
// Claude's summary wrapped to the pane, and the changes, the per-file summary
// and the diff coloured as git colours them. A section that is folded shows
// its heading only.
func (m Model) draftBody(st watchStyles, width int) []string {
	d := m.draftResult
	lines := []string{"", m.sectionHeader(st, sectionPlan, "Plan", "")}
	if m.draftSectionOpen(sectionPlan) {
		if s := strings.TrimSpace(d.Summary); s != "" {
			for _, l := range strings.Split(ansi.Wordwrap(s, width-2, ""), "\n") {
				lines = append(lines, "  "+l)
			}
		} else {
			lines = append(lines, st.vdim("  Claude gave no summary"))
		}
	}
	if d.Stat == "" {
		return lines
	}
	lines = append(lines, "", m.sectionHeader(st, sectionChanges, "Changes", statSummary(d.Stat)))
	if !m.draftSectionOpen(sectionChanges) {
		return lines
	}
	for _, l := range strings.Split(d.Stat, "\n") {
		lines = append(lines, st.muted(l))
	}
	lines = append(lines, "")
	lines = append(lines, m.draftDiff.diffLines(st, m.hasDarkBG, d.Diff, false, width)...)
	return lines
}

// scrollDraft moves the shown draft by delta lines, kept within it.
func (m Model) scrollDraft(delta int) Model {
	room := max(m.contentHeight()+1-draftPaneTop, 1) // the pane's title is in the frame
	limit := max(len(m.draftBody(m.styles(), m.draftPaneWidth()))-room, 0)
	m.draftScroll = min(max(m.draftScroll+delta, 0), limit)
	return m
}

// renderNewRunPane is the right pane while a prompt is typed: a clean page
// for the new run, saying what happens next, rather than the last run still
// on show behind the prompt.
func (m Model) renderNewRunPane(st watchStyles) []string {
	key := st.Brand.Bold(true).Render
	return []string{
		st.vdim("new run  ") + st.muted(filepath.Base(m.startRoot())),
		"",
		st.muted("Describe the change you want below, then press ") + key("enter") + st.muted("."),
		st.muted("Claude drafts it on your machine, you check the diff here,"),
		st.muted("then send it to the factory."),
	}
}
