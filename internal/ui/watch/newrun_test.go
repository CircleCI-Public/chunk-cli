package watch

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// fakeDrafts stands in for Claude and the factory: drafts report activity and
// answer with draft, or err; sends answer with runID, or sendErr.
type fakeDrafts struct {
	root, prompt string
	activity     []string
	draft        Draft
	err          error
	blockUntil   bool // wait for ctx to end before answering, as a stopped Claude does

	sentRoot, sentRun string
	runID             string
	sendErr           error
}

func (f *fakeDrafts) makeDraft(ctx context.Context, root, prompt string, activity func(string)) (Draft, error) {
	f.root, f.prompt = root, prompt
	for _, a := range f.activity {
		activity(a)
	}
	if f.blockUntil {
		<-ctx.Done()
		return f.draft, ctx.Err()
	}
	return f.draft, f.err
}

func (f *fakeDrafts) send(root, runID string) (string, error) {
	f.sentRoot, f.sentRun = root, runID
	return f.runID, f.sendErr
}

func draftModel(t *testing.T, f *fakeDrafts) Model {
	t.Helper()
	return searchModel().WithDrafts(t.Context(), "/src/chunk-cli", f.makeDraft, f.send)
}

// deliver runs a command the model returned and hands its message back, as the
// program would. A batch delivers the first message that is not activity
// waiting on a quiet channel.
func deliver(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	assert.Assert(t, cmd != nil, "a command was returned")
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			m = deliver(t, m, c)
		}
		return m
	}
	next, _ := m.Update(msg)
	return next.(Model)
}

func view(m Model) string { return stripANSI(m.render()) }

var sampleDraft = Draft{
	RunID:   "run-1",
	Summary: "Added a --verbose flag.",
	Stat:    " internal/cmd/validate.go | 12 ++++++++++--\n 1 file changed, 10 insertions(+), 2 deletions(-)",
	Diff:    "diff --git a/internal/cmd/validate.go b/internal/cmd/validate.go\n+\tverbose bool\n",
}

// n opens a prompt; Claude drafts it, the draft's diff can be read, and Enter
// sends it to the factory, whose run is selected once a poll lists it.
func TestNDraftsAPromptWithClaudeAndSendsItToTheFactory(t *testing.T) {
	f := &fakeDrafts{draft: sampleDraft, runID: "factory-run"}
	m := draftModel(t, f)
	assert.Assert(t, strings.Contains(view(m), "n new run"), "starting a run is discoverable:\n%s", view(m))

	m, _ = press(m, 'n')
	out := view(m)
	assert.Assert(t, strings.Contains(out, "Step 1 of 3: write a prompt describing the change you want\n  [1. write a prompt] → 2. Claude drafts the change → 3. send to the factory (last step)\n  › ▏type the change you want (e.g. add a --verbose flag)"), "the empty prompt says what to type:\n%s", out)
	m = typeText(m, "add a --verbose flag")
	out = view(m)
	assert.Assert(t, strings.Contains(out, "› add a --verbose flag▏"), out)
	assert.Assert(t, !strings.Contains(out, "type the change you want"), "the placeholder goes once something is typed:\n%s", out)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.Equal(t, strings.TrimSpace(lines[len(lines)-1]), "Enter = go to step 2  ·  Esc = cancel", "the keys are the bottom line:\n%s", out)
	assert.Assert(t, strings.Contains(m.render(), m.styles().teal(stepsMap(1))), "the map of steps is in the accent colour")
	// The prompt is a field across the footer, so where to type stands out.
	for _, l := range lines {
		if strings.Contains(l, "› ▏") {
			assert.Equal(t, ansi.StringWidth(l), m.width-2, "the field spans the footer: %q", l)
		}
	}

	// While typing, keys are text: q does not quit.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	assert.Assert(t, cmd == nil)

	m, cmd = press(m, tea.KeyEnter)
	assert.Equal(t, m.draft, draftWorking)
	assert.Assert(t, strings.Contains(view(m), "Step 2 of 3: Claude is drafting the code change on your machine, in a copy of your repo\n  1. write a prompt → [2. Claude drafts the change] → 3. send to the factory (last step)\n  › Claude is working…"), view(m))
	assert.Assert(t, strings.Contains(view(m), "Esc = stop Claude"), view(m))
	m = deliver(t, m, cmd)
	assert.Equal(t, f.root, "/src/chunk-cli")
	assert.Equal(t, f.prompt, "add a --verbose flag")

	assert.Equal(t, m.draft, draftReady)
	out = view(m)
	assert.Assert(t, strings.Contains(out, "Step 3 of 3 (last step): send Claude's draft and your prompt to the factory, which finishes the change\n  1. write a prompt → 2. Claude drafts the change → [3. send to the factory (last step)]\n  › Draft ready: 1 file changed, 10 insertions(+), 2 deletions(-)\n  Enter = send to factory (last step)  ·  d = view the draft's diff  ·  Esc = keep for later"), out)
	assert.Assert(t, strings.Contains(out, "Enter = send to factory (last step)"), out)
	assert.Assert(t, strings.Contains(out, "d = view the draft's diff"), out)
	assert.Assert(t, strings.Contains(out, "Esc = keep for later"), out)

	// d opens the diff, with what Claude said it did; closing it comes back
	// to the draft.
	m, _ = press(m, 'd')
	assert.Assert(t, m.output != nil)
	out = view(m)
	assert.Assert(t, strings.Contains(out, "Added a --verbose flag."), out)
	assert.Assert(t, strings.Contains(out, "+\tverbose bool"), out)
	m, _ = press(m, tea.KeyEscape)
	assert.Assert(t, m.output == nil)
	assert.Equal(t, m.draft, draftReady)

	m, cmd = press(m, tea.KeyEnter)
	assert.Assert(t, strings.Contains(view(m), "sending the draft to the factory…"))
	m = deliver(t, m, cmd)
	assert.Equal(t, f.sentRoot, "/src/chunk-cli")
	assert.Equal(t, f.sentRun, "run-1")
	assert.Assert(t, strings.Contains(view(m), "sent the draft and your prompt to the factory in chunk-cli"))

	started := factoryRun("factory-run", chunkd.SessionRunning, time.Now())
	m = poll(m, started, m.allRuns[0], m.allRuns[1])
	assert.Equal(t, m.selRun, "factory-run", "the factory run is selected once it is listed")
	assert.Assert(t, !strings.Contains(view(m), "sent the draft and your prompt"), "the note goes once the run is shown")
}

// What Claude is doing is shown as it drafts.
func TestClaudesActivityIsShownWhileItDrafts(t *testing.T) {
	m := draftModel(t, &fakeDrafts{draft: sampleDraft})
	m.draft = draftWorking
	m, _ = m.withDraftActivity(draftActivityMsg("Edit internal/cmd/validate.go"))

	assert.Assert(t, strings.Contains(view(m), "› Claude is working…"), view(m))
	assert.Assert(t, strings.Contains(view(m), "Edit internal/cmd/validate.go"), view(m))
}

// Esc while Claude drafts stops it, and what it did so far is shown as the
// draft.
func TestStoppingClaudeKeepsWhatItDid(t *testing.T) {
	f := &fakeDrafts{draft: sampleDraft, blockUntil: true}
	m, _ := press(draftModel(t, f), 'n')
	m = typeText(m, "add a flag")
	m, _ = press(m, tea.KeyEnter)
	// The draft runs off the test's goroutine, as the program runs it, so
	// it gets its own copies of what it needs from the model.
	wait, activity := m.draftWait, m.draftActivity
	done := make(chan tea.Msg, 1)
	go func() { done <- draftCmd(wait, f.makeDraft, "/src/chunk-cli", "add a flag", activity)() }()

	m, _ = press(m, tea.KeyEscape)
	assert.Assert(t, strings.Contains(view(m), "stopping Claude…"), view(m))

	next, _ := m.Update(<-done)
	m = next.(Model)
	assert.Equal(t, m.draft, draftReady)
	assert.Assert(t, strings.Contains(view(m), "› Draft ready: 1 file changed"), view(m))
}

// A draft with no changes has nothing for the factory to refine, so it can
// only be closed.
func TestADraftWithNoChangesCanOnlyBeClosed(t *testing.T) {
	f := &fakeDrafts{draft: Draft{RunID: "run-1", Summary: "Nothing to change."}}
	m, _ := press(draftModel(t, f), 'n')
	m = typeText(m, "add a flag")
	m, cmd := press(m, tea.KeyEnter)
	m = deliver(t, m, cmd)

	out := view(m)
	assert.Assert(t, strings.Contains(out, "› Claude made no changes, so there is nothing to send"), out)
	assert.Assert(t, !strings.Contains(out, "Enter = send to factory"), out)
	assert.Assert(t, strings.Contains(out, "Esc = close"), out)

	m, cmd = press(m, tea.KeyEnter)
	assert.Assert(t, cmd == nil, "Enter sends nothing")
	m, _ = press(m, tea.KeyEscape)
	assert.Equal(t, m.draft, draftNone)
	assert.Equal(t, f.sentRun, "")
}

// Esc on a draft keeps it for later, and says how to send it.
func TestKeepingADraftForLater(t *testing.T) {
	f := &fakeDrafts{draft: sampleDraft}
	m, _ := press(draftModel(t, f), 'n')
	m = typeText(m, "add a flag")
	m, cmd := press(m, tea.KeyEnter)
	m = deliver(t, m, cmd)

	m, _ = press(m, tea.KeyEscape)
	assert.Equal(t, m.draft, draftNone)
	assert.Assert(t, strings.Contains(view(m), "chunk factory build --continue run-1"), view(m))
	assert.Equal(t, f.sentRun, "")
}

// Why there is no draft, or why it could not be sent, is shown in the footer.
func TestFailuresSayWhy(t *testing.T) {
	f := &fakeDrafts{err: errors.New("No validate commands configured. Run 'chunk init' first")}
	m, _ := press(draftModel(t, f), 'n')
	m = typeText(m, "add a flag")
	m, cmd := press(m, tea.KeyEnter)
	m = deliver(t, m, cmd)
	assert.Equal(t, m.draft, draftNone)
	assert.Assert(t, strings.Contains(view(m), "Claude could not make a draft: No validate commands configured"), view(m))
	m, _ = press(m, tea.KeyDown)
	assert.Assert(t, !strings.Contains(view(m), "could not make a draft"), "the note goes with the next key")

	f = &fakeDrafts{draft: sampleDraft, sendErr: errors.New("a factory run is already active for this project")}
	m, _ = press(draftModel(t, f), 'n')
	m = typeText(m, "add a flag")
	m, cmd = press(m, tea.KeyEnter)
	m = deliver(t, m, cmd)
	m, cmd = press(m, tea.KeyEnter)
	m = deliver(t, m, cmd)
	assert.Assert(t, strings.Contains(view(m), "could not send the draft to the factory: a factory run is already active"), view(m))
	assert.Equal(t, m.pendingRun, "")
}

func TestEscDropsThePromptAndAnEmptyOneIsNotSent(t *testing.T) {
	f := &fakeDrafts{draft: sampleDraft}
	m, _ := press(draftModel(t, f), 'n')
	m = typeText(m, "   ")
	m, cmd := press(m, tea.KeyEnter)
	assert.Assert(t, cmd == nil)
	assert.Equal(t, m.draft, draftPrompting, "the prompt stays open")

	m, cmd = press(m, tea.KeyEscape)
	assert.Assert(t, cmd == nil, "Esc does not quit while prompting")
	assert.Equal(t, m.draft, draftNone)
	assert.Equal(t, f.prompt, "", "Claude was not asked")
}

func TestBackspaceAndPasteEditThePrompt(t *testing.T) {
	m, _ := press(draftModel(t, &fakeDrafts{}), 'n')
	next, _ := m.Update(tea.PasteMsg{Content: "add a flagg"})
	m = next.(Model)
	m, _ = press(m, tea.KeyBackspace)

	assert.Equal(t, m.prompt, "add a flag")
}

// A dashboard with nothing to draft with, such as one watching a remote
// daemon, offers no n.
func TestNIsOnlyOfferedWithSomewhereToDraft(t *testing.T) {
	m := searchModel()
	assert.Assert(t, !strings.Contains(view(m), "new run"))
	m, _ = press(m, 'n')
	assert.Equal(t, m.draft, draftNone)

	// Opened outside a project, the draft is in the selected run's.
	f := &fakeDrafts{}
	m = searchModel().WithDrafts(t.Context(), "", f.makeDraft, f.send)
	for i := range m.sessions {
		m.sessions[i].s.ProjectRoot = "/src/other"
	}
	m, _ = press(m, 'n')
	assert.Equal(t, m.draft, draftPrompting)
	assert.Equal(t, m.startRoot(), "/src/other")
}

// Sending a draft clears a search, so the factory run is not filtered out of
// the list it is about to be selected in.
func TestSendingADraftClearsTheSearch(t *testing.T) {
	f := &fakeDrafts{draft: sampleDraft, runID: "factory-run"}
	m := draftModel(t, f)
	m, _ = press(m, '/')
	m = typeText(m, "cache")
	m, _ = press(m, tea.KeyEnter)

	m, _ = press(m, 'n')
	m = typeText(m, "add a flag")
	m, cmd := press(m, tea.KeyEnter)
	m = deliver(t, m, cmd)
	m, cmd = press(m, tea.KeyEnter)
	m = deliver(t, m, cmd)

	assert.Equal(t, m.query, "")
}

// A long prompt shows its end, where the typing is, and keeps the footer to
// one line.
// A new run's steps take a line of their own, and the dashboard stays the
// terminal's height.
func TestANewRunsStepsKeepTheDashboardsHeight(t *testing.T) {
	m := draftModel(t, &fakeDrafts{})
	m, _ = press(m, 'n')
	for _, width := range []int{60, 110} {
		m.width = width
		out := m.render()
		assert.Equal(t, strings.Count(out, "\n"), m.height, "width %d:\n%s", width, out)
	}
}

func TestALongPromptKeepsItsEndInView(t *testing.T) {
	m, _ := press(draftModel(t, &fakeDrafts{}), 'n')
	m.width = 60
	m = typeText(m, strings.Repeat("word ", 30)+"END")

	out := m.render()
	assert.Equal(t, strings.Count(out, "\n"), m.height, out)
	assert.Assert(t, strings.Contains(stripANSI(out), "END▏"), out)
}

// A terminal that reports more rows than it shows gets its last rows left
// blank, so the footer is drawn where it can be seen.
func TestABottomMarginKeepsTheFooterInView(t *testing.T) {
	m := searchModel().WithBottomMargin(2)
	next, _ := m.Update(tea.WindowSizeMsg{Width: 110, Height: 40})
	m = next.(Model)

	out := strings.TrimRight(m.render(), "\n")
	lines := strings.Split(out, "\n")
	assert.Equal(t, len(lines), 38, out)
	assert.Assert(t, strings.Contains(stripANSI(lines[len(lines)-1]), "q quit"), "the footer is the last line drawn:\n%s", out)
}
