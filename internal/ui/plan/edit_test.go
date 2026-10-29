package plan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// editorFiles is the files the screen has opened an editor on, in a
// directory this test owns.
func editorFiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	return dir
}

// planInPlan is planModel with the plan pane focused, and the agent's first
// exchange forgotten.
func planInPlan(t *testing.T) (Model, *fakeStore, *fakeAgent) {
	t.Helper()
	m, store := planModel(t)
	agent := m.agent.(*fakeAgent)
	agent.reset()
	m = drive(t, m, press("tab"))
	return m, store, agent
}

// editedFrom is the message the editor closing on blocks lo to hi of m's plan
// would send, with the file holding text.
func editedFrom(t *testing.T, m Model, lo, hi int, text string) editedMsg {
	t.Helper()
	start, end, ok := m.spanOf(lo, hi)
	assert.Assert(t, ok)
	path := filepath.Join(t.TempDir(), "edit.md")
	err := os.WriteFile(path, []byte(text), 0o600)
	assert.NilError(t, err)
	return editedMsg{path: path, before: m.renderedFrom, start: start, end: end, block: lo, text: text}
}

func TestEOpensTheHighlightedPartInTheEditor(t *testing.T) {
	dir := editorFiles(t)
	m, _, _ := planInPlan(t)
	m = drive(t, m, press("down"))
	m = drive(t, m, press("e"))

	assert.Check(t, m.editing)
	files, err := filepath.Glob(filepath.Join(dir, "chunk-plan-*.md"))
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(files, 1))
	got, err := os.ReadFile(files[0])
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(string(got), "The flag exists.\n"))
}

func TestEOpensASelectionOfPartsTogether(t *testing.T) {
	dir := editorFiles(t)
	m, _, _ := planInPlan(t)
	m = drive(t, m, press("shift+down"))
	drive(t, m, press("e"))

	files, err := filepath.Glob(filepath.Join(dir, "chunk-plan-*.md"))
	assert.NilError(t, err)
	assert.Assert(t, cmp.Len(files, 1))
	got, err := os.ReadFile(files[0])
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(string(got), "## 1. Outcome\n\nThe flag exists.\n"))
}

func TestEExplainsWhyItCannotEditWhileFactoryWorks(t *testing.T) {
	dir := editorFiles(t)
	m, store, _ := planInPlan(t)
	store.setEvents()
	m = refreshed(t, m)
	m = drive(t, m, press("e"))

	assert.Check(t, !m.editing)
	assert.Check(t, cmp.Contains(ansi.Strip(m.status), "once factory is waiting on you"))
	files, _ := filepath.Glob(filepath.Join(dir, "chunk-plan-*.md"))
	assert.Check(t, cmp.Len(files, 0))
}

func TestEditedPartIsSavedInPlaceWithAReReadNoteQueued(t *testing.T) {
	m, store, agent := planInPlan(t)
	m.editing = true
	msg := editedFrom(t, m, 1, 1, "The flag exists and is documented.\n")
	m = drive(t, m, msg)

	assert.Assert(t, cmp.Len(store.Set, 1))
	assert.Check(t, cmp.Equal(store.Set[0],
		"## 1. Outcome\n\nThe flag exists and is documented.\n\n## 2. Spec\n\n* [AGENT] parse it in main.go"))
	assert.Check(t, !m.editing)
	assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Saved your edit."))
	assert.Check(t, cmp.Len(agent.Sent(), 0), "nothing goes to the session")
	queued := queuedComments(m.comments)
	assert.Assert(t, cmp.Len(queued, 1))
	assert.Check(t, cmp.Equal(queued[0].text, rereadNote))
	assert.Check(t, cmp.Equal(queued[0].quote, "The flag exists and is documented."))
	assert.Check(t, cmp.Equal(queued[0].block, 1))
	_, err := os.Stat(msg.path)
	assert.Check(t, os.IsNotExist(err), "the editor's file was left behind")
}

// saveEdit drives an edit of block i of m's plan to text, as the editor
// closing on it would.
func saveEdit(t *testing.T, m Model, i int, text string) Model {
	t.Helper()
	m.editing = true
	return drive(t, m, editedFrom(t, m, i, i, text))
}

func TestEditsQueueOneReReadNotePerPart(t *testing.T) {
	m, store, _ := planInPlan(t)
	m = saveEdit(t, m, 1, "The flag exists and is documented.\n")
	m = saveEdit(t, m, 3, "* [AGENT] parse it in main.go and cmd.go\n")
	assert.Assert(t, cmp.Len(store.Set, 2))
	assert.Assert(t, cmp.Len(queuedComments(m.comments), 2))

	// The same first part, with a part added after it.
	m = saveEdit(t, m, 1, "The flag exists and is documented.\n\nIt is in the README.\n")
	assert.Assert(t, cmp.Len(store.Set, 3))
	assert.Assert(t, cmp.Len(queuedComments(m.comments), 2))

	m = saveEdit(t, m, 1, "The flag is documented.\n\nIt is in the README.\n")
	assert.Assert(t, cmp.Len(store.Set, 4))
	queued := queuedComments(m.comments)
	assert.Assert(t, cmp.Len(queued, 2))
	var quotes []string
	for _, c := range queued {
		assert.Check(t, cmp.Equal(c.text, rereadNote))
		quotes = append(quotes, c.quote)
		if c.block == 1 {
			assert.Check(t, cmp.Equal(c.quote, "The flag is documented."))
		}
	}
	assert.Check(t, cmp.Contains(quotes, "The flag is documented."))
	assert.Check(t, cmp.Contains(quotes, "* [AGENT] parse it in main.go and cmd.go"))
}

func TestReReadNoteGoesWithTheQueuedCommentsOnS(t *testing.T) {
	t.Run("sent", func(t *testing.T) {
		m, store, agent := planInPlan(t)
		m = saveEdit(t, m, 1, "The flag exists and is documented.\n")
		m = drive(t, m, press("s"))

		assert.Check(t, cmp.DeepEqual(store.Added, [][2]string{{"The flag exists and is documented.", rereadNote}}))
		assert.Check(t, cmp.DeepEqual(store.HandedOff, []string{"Planning - Human"}))
		assert.Check(t, cmp.Len(queuedComments(m.comments), 0))
		assert.Check(t, cmp.Len(agent.Sent(), 0), "nothing goes to the session")
	})
	t.Run("dropped", func(t *testing.T) {
		m, store, agent := planInPlan(t)
		m = saveEdit(t, m, 1, "The flag exists and is documented.\n")
		m.moveCursor(1)
		assert.Assert(t, m.commentAt(m.cursor) >= 0, "the cursor is not on the note")
		m = drive(t, m, press("d"))
		assert.Assert(t, cmp.Len(queuedComments(m.comments), 0))
		m = drive(t, m, press("s"))

		assert.Check(t, cmp.Len(store.Added, 0))
		assert.Check(t, cmp.Len(store.HandedOff, 0))
		assert.Check(t, cmp.Len(agent.Sent(), 0))
	})
}

func TestEditedPartIsNotSavedWhenNothingChanged(t *testing.T) {
	m, store, agent := planInPlan(t)
	m.editing = true
	msg := editedFrom(t, m, 1, 1, "The flag exists.\n")
	m = drive(t, m, msg)

	assert.Check(t, cmp.Len(store.Set, 0))
	assert.Check(t, cmp.Len(agent.Sent(), 0))
	assert.Check(t, cmp.Len(queuedComments(m.comments), 0))
	assert.Check(t, !m.editing)
	assert.Check(t, cmp.Equal(ansi.Strip(m.status), "No changes."))
}

func TestEditedPartLeftEmptyIsNotSaved(t *testing.T) {
	m, store, _ := planInPlan(t)
	m.editing = true
	m = drive(t, m, editedFrom(t, m, 1, 1, "\n\n"))

	assert.Check(t, cmp.Len(store.Set, 0))
	assert.Check(t, cmp.Len(queuedComments(m.comments), 0))
	assert.Check(t, cmp.Contains(ansi.Strip(m.status), "Nothing was left of that part"))
}

func TestEditIsRefusedWhenAnotherWriterChangedThePlan(t *testing.T) {
	m, store, agent := planInPlan(t)
	m.editing = true
	msg := editedFrom(t, m, 1, 1, "The flag exists and is documented.\n")
	// The agent wrote the plan while the editor was open.
	store.TextV = twoParts + "\n* [AGENT] and more"
	m = drive(t, m, msg)

	assert.Check(t, cmp.Len(store.Set, 0))
	assert.Check(t, cmp.Len(agent.Sent(), 0))
	assert.Check(t, !m.editing)
	status := ansi.Strip(m.status)
	assert.Check(t, cmp.Contains(status, "the plan changed while you were editing it"))
	assert.Check(t, cmp.Contains(status, msg.path))
	_, err := os.Stat(msg.path)
	assert.Check(t, err == nil, "the edit was not kept for the developer")
}

func TestEditIsRefusedOnceFactoryStopsWaiting(t *testing.T) {
	m, store, _ := planInPlan(t)
	m.editing = true
	msg := editedFrom(t, m, 1, 1, "The flag exists and is documented.\n")
	store.setEvents()
	m = drive(t, m, msg)

	assert.Check(t, cmp.Len(store.Set, 0))
	assert.Check(t, cmp.Contains(ansi.Strip(m.status), "factory is no longer waiting on you"))
}

func TestEditIsSavedWhileFactoryWaitsOnOpenQuestions(t *testing.T) {
	m, store, _ := planInPlan(t)
	store.setEvents("answer-open-questions", "abandon")
	m = refreshed(t, m)
	m.editing = true
	msg := editedFrom(t, m, 1, 1, "The flag exists and is documented.\n")
	m = drive(t, m, msg)

	assert.Check(t, cmp.Len(store.Set, 1))
	assert.Check(t, strings.HasPrefix(ansi.Strip(m.status), "Saved your edit."), "status: %q", m.status)
}

func TestFailedEditorSavesNothing(t *testing.T) {
	m, store, _ := planInPlan(t)
	m.editing = true
	msg := editedFrom(t, m, 1, 1, "x")
	msg.err = os.ErrPermission
	m = drive(t, m, msg)

	assert.Check(t, cmp.Len(store.Set, 0))
	assert.Check(t, !m.editing)
	assert.Check(t, strings.Contains(ansi.Strip(m.status), "Nothing was saved."))
}

func TestEditorCommandIsTheUsersEditor(t *testing.T) {
	tests := []struct {
		name           string
		visual, editor string
		want           string
	}{
		{
			name:   "visual first",
			visual: "code --wait",
			editor: "nano",
			want:   "code --wait",
		},
		{
			name:   "editor",
			editor: "nano",
			want:   "nano",
		},
		{
			name: "vi when none is set",
			want: "vi",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("VISUAL", tc.visual)
			t.Setenv("EDITOR", tc.editor)
			cmd := editorCommand("/tmp/plan.md")
			assert.Check(t, cmp.DeepEqual(cmd.Args, []string{"sh", "-c", tc.want + ` "$@"`, tc.want, "/tmp/plan.md"}))
		})
	}
}

func TestEditorCommandRunsTheEditorOnThePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan with spaces.md")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "touch")
	err := editorCommand(path).Run()
	assert.NilError(t, err)
	_, err = os.Stat(path)
	assert.Check(t, err == nil, "the editor was not run on the path")
}
