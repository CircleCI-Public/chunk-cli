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
	assert.NilError(t, os.WriteFile(path, []byte(text), 0o600))
	return editedMsg{path: path, before: m.renderedFrom, start: start, end: end, heading: m.headingAbove(lo), text: text}
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

func TestEditedPartIsSavedInPlaceAndTheAgentToldOfIt(t *testing.T) {
	m, store, agent := planInPlan(t)
	m.editing = true
	msg := editedFrom(t, m, 1, 1, "The flag exists and is documented.\n")
	m = drive(t, m, msg)

	assert.Assert(t, cmp.Len(store.Set, 1))
	assert.Check(t, cmp.Equal(store.Set[0],
		"## 1. Outcome\n\nThe flag exists and is documented.\n\n## 2. Spec\n\n* [AGENT] parse it in main.go"))
	assert.Check(t, !m.editing)
	assert.Check(t, cmp.Equal(ansi.Strip(m.status), "Saved your edit."))
	sent := agent.Sent()
	assert.Assert(t, cmp.Len(sent, 1))
	assert.Check(t, cmp.Contains(sent[0], "I edited the plan myself"))
	assert.Check(t, cmp.Contains(sent[0], `"1. Outcome"`))
	_, err := os.Stat(msg.path)
	assert.Check(t, os.IsNotExist(err), "the editor's file was left behind")
}

func TestEditedPartIsNotSavedWhenNothingChanged(t *testing.T) {
	m, store, agent := planInPlan(t)
	m.editing = true
	msg := editedFrom(t, m, 1, 1, "The flag exists.\n")
	m = drive(t, m, msg)

	assert.Check(t, cmp.Len(store.Set, 0))
	assert.Check(t, cmp.Len(agent.Sent(), 0))
	assert.Check(t, !m.editing)
	assert.Check(t, cmp.Equal(ansi.Strip(m.status), "No changes."))
}

func TestEditedPartLeftEmptyIsNotSaved(t *testing.T) {
	m, store, _ := planInPlan(t)
	m.editing = true
	m = drive(t, m, editedFrom(t, m, 1, 1, "\n\n"))

	assert.Check(t, cmp.Len(store.Set, 0))
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
		{name: "visual first", visual: "code --wait", editor: "nano", want: "code --wait"},
		{name: "editor", editor: "nano", want: "nano"},
		{name: "vi when none is set", want: "vi"},
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
	assert.NilError(t, editorCommand(path).Run())
	_, err := os.Stat(path)
	assert.Check(t, err == nil, "the editor was not run on the path")
}
