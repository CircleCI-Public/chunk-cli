package plan

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// editedMsg is the end of an editor session on a part of the plan: text is
// what the editor left in the file at path, or err says why there is none.
type editedMsg struct {
	path string
	// before is the plan as the screen had it when the editor opened, and
	// start and end where the part being edited sits in it.
	before     string
	start, end int
	// block is the first block edited.
	block int
	text  string
	err   error
}

// editSavedMsg is the end of saving an edit to factory: text was saved in
// place of the part at block, or err says why not.
type editSavedMsg struct {
	path  string
	block int
	text  string
	err   error
}

var (
	errEditNotWaiting = errors.New("factory is no longer waiting on you, so the plan can't be edited now")
	errEditChanged    = errors.New("the plan changed while you were editing it")
)

// cannotEdit says why the plan can't be edited now, or is "" when it can. A
// plan is edited only while factory waits on the developer: at any other time
// the agent may be writing it too, and the later write would replace the
// earlier.
func (m Model) cannotEdit() string {
	switch {
	case m.busy:
		return "Wait for chunk to finish."
	case !m.takesComments():
		return "The plan can be edited once factory is waiting on you."
	}
	return ""
}

// editorCommand is the user's editor, $VISUAL or $EDITOR as git reads them,
// opened on path. The editor is run through sh, as git does, since it may
// carry arguments of its own.
func editorCommand(path string) *exec.Cmd {
	editor := cmp.Or(os.Getenv("VISUAL"), os.Getenv("EDITOR"), "vi")
	return exec.Command("sh", "-c", editor+` "$@"`, editor, path) //nolint:gosec // running the user's own editor is the point
}

// startEdit opens the highlighted part of the plan, or the run of parts
// selected, in the developer's editor. The screen waits while it is open.
func (m Model) startEdit() (Model, tea.Cmd) {
	if reason := m.cannotEdit(); reason != "" {
		m.status = warn(reason)
		return m, nil
	}
	lo, hi := m.selection()
	start, end, ok := m.spanOf(lo, hi)
	if !ok {
		m.status = warn("That part can't be found in the plan as it is, so it can't be edited. Comment on it instead.")
		return m, nil
	}
	f, err := os.CreateTemp("", "chunk-plan-*.md")
	if err != nil {
		m.status = ui.ErrorStyle.Render("Could not open the editor: " + err.Error())
		return m, nil
	}
	path := f.Name()
	before := m.renderedFrom
	_, werr := f.WriteString(before[start:end] + "\n")
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		_ = os.Remove(path)
		m.status = ui.ErrorStyle.Render("Could not open the editor: " + werr.Error())
		return m, nil
	}

	m.editing, m.status, m.anchor = true, "", -1
	return m, tea.ExecProcess(editorCommand(path), func(err error) tea.Msg {
		msg := editedMsg{path: path, before: before, start: start, end: end, block: lo}
		if err != nil {
			msg.err = fmt.Errorf("the editor failed: %w", err)
			return msg
		}
		b, err := os.ReadFile(path)
		if err != nil {
			msg.err = fmt.Errorf("read what the editor left: %w", err)
			return msg
		}
		msg.text = string(b)
		return msg
	})
}

// applyEdited handles the editor closing: an edit that changed something is
// saved to factory.
func (m Model) applyEdited(msg editedMsg) (Model, tea.Cmd) {
	if msg.err != nil {
		m.editing = false
		m.status = ui.ErrorStyle.Render(msg.err.Error() + ". Nothing was saved.")
		_ = os.Remove(msg.path)
		return m, nil
	}
	// The part's own markdown has no blank lines around it; the file's
	// trailing newline is the editor's, not the developer's.
	text := strings.TrimRight(strings.TrimLeft(msg.text, "\n"), "\n ")
	switch text {
	case msg.before[msg.start:msg.end]:
		m.editing = false
		m.status = "No changes."
		_ = os.Remove(msg.path)
		return m, nil
	case "":
		m.editing = false
		m.status = warn("Nothing was left of that part, so nothing was saved. Comment on it to have chunk remove it.")
		_ = os.Remove(msg.path)
		return m, nil
	}
	m.status = "Saving your edit…"
	return m, saveEditCmd(m.ctx, m.store, msg, text)
}

// saveEditCmd saves msg's edit, text, to factory in place of the part it was
// of. Factory takes only a whole plan, so the plan is read again first, and
// the edit is saved only if it is as the screen had it and factory still
// waits on the developer: an agent that wrote it meanwhile would be
// overwritten.
func saveEditCmd(ctx context.Context, s Store, msg editedMsg, text string) tea.Cmd {
	return func() tea.Msg {
		save := func() error {
			flow, err := s.Flow(ctx)
			if err != nil {
				return err
			}
			if handOffEventOf(flow.Events) == "" {
				return errEditNotWaiting
			}
			now, err := s.Text(ctx)
			if err != nil {
				return err
			}
			if now != msg.before {
				return errEditChanged
			}
			return s.SetText(ctx, msg.before[:msg.start]+text+msg.before[msg.end:])
		}
		return editSavedMsg{path: msg.path, block: msg.block, text: text, err: save()}
	}
}

// rereadNote is the comment a saved edit queues on the part it changed, so
// the planning session reads the part again before it next writes near it.
const rereadNote = "I edited this part myself; re-read it before changing anything near it."

// applyEditSaved handles the end of saving an edit. A saved edit queues a
// comment asking the planning session to re-read the part, which would
// otherwise go on from the plan it last read.
func (m Model) applyEditSaved(msg editSavedMsg) (Model, tea.Cmd) {
	m.editing = false
	if msg.err != nil {
		m.status = ui.ErrorStyle.Render(fmt.Sprintf("Could not save your edit: %s. It is kept in %s.", msg.err, msg.path))
		return m, refreshCmd(m.ctx, m.store)
	}
	_ = os.Remove(msg.path)
	m.queueRereadNote(msg.block, msg.text)
	m.status = "Saved your edit. A note asking the planner to re-read it is queued; press s to send it."
	return m, refreshCmd(m.ctx, m.store)
}

// queueRereadNote queues rereadNote on the first part of text, the edit
// just saved at block. A queued re-read note on that block already is moved
// to the new text rather than doubled.
func (m *Model) queueRereadNote(block int, text string) {
	parts := splitBlocks(text)
	if len(parts) == 0 {
		return
	}
	quote := parts[0].source
	for i, c := range m.comments {
		if c.id == "" && c.text == rereadNote && c.block == block {
			m.comments[i].quote = quote
			return
		}
	}
	m.comments = append(m.comments, comment{block: block, span: 1, quote: quote, text: rereadNote})
	m.syncPlaceholder()
	m.renderChat()
}
