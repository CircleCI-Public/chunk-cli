package watch

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
)

// runChange is a finished run's committed change: git's diff of its branch
// against its baseline, or why it could not be read.
type runChange struct {
	loaded bool
	diff   string
	err    error
}

// runChangeMsg is a run's change, read.
type runChangeMsg struct {
	id     string
	change runChange
}

// readRunChange reads a finished run's change from the project's repository.
// The daemon reports only its size; the change itself is on this machine, on
// the run's branch. Swapped out in tests.
var readRunChange = func(root, baseline, branch string) (string, error) {
	out, err := (gitexec.Runner{Dir: root}).Output(context.Background(), "diff", baseline, branch)
	if err != nil {
		return "", fmt.Errorf("read the change: %w", err)
	}
	return string(out), nil
}

// loadSelectedChange starts reading the selected run's change, if it has
// finished with work committed and its change has not been read yet.
func (m Model) loadSelectedChange() (Model, tea.Cmd) {
	run := m.selectedRun()
	if run == nil || !run.s.State.Finished() {
		return m, nil
	}
	f := run.s.Factory
	if f == nil || !f.Committed || f.Stat == "" || f.Branch == "" || f.Baseline == "" {
		return m, nil
	}
	if _, ok := m.runChanges[run.s.ID]; ok {
		return m, nil
	}
	if m.runChanges == nil {
		m.runChanges = map[string]runChange{}
	}
	m.runChanges[run.s.ID] = runChange{}
	id, root, baseline, branch := run.s.ID, run.s.ProjectRoot, f.Baseline, f.Branch
	return m, func() tea.Msg {
		diff, err := readRunChange(root, baseline, branch)
		return runChangeMsg{id: id, change: runChange{loaded: true, diff: diff, err: err}}
	}
}

// withRunChange keeps a run's change, read.
func (m Model) withRunChange(msg runChangeMsg) Model {
	if m.runChanges == nil {
		m.runChanges = map[string]runChange{}
	}
	m.runChanges[msg.id] = msg.change
	return m
}

// runChangeLines is a finished run's change as its pane shows it under the
// outcome, in at most room lines: a heading, then the diff, coloured, with
// git's headers cut to the file's name so more of the change fits. A change
// longer than that ends in how to see the rest.
func (m Model) runChangeLines(st watchStyles, run sessionInfo, room int) []string {
	f := run.s.Factory
	c, ok := m.runChanges[run.s.ID]
	if f == nil || !ok || !c.loaded || c.err != nil || strings.TrimSpace(c.diff) == "" || room < 3 {
		return nil
	}
	head := "  " + st.vdim("change  ") + st.muted(lastLine(f.Stat))
	diff := diffLines(st, c.diff, true)
	lines := []string{"", head}
	if fit := room - len(lines); len(diff) > fit {
		shown := max(fit-1, 0)
		lines = append(lines, indent(diff[:shown])...)
		return append(lines, "  "+st.vdim(fmt.Sprintf("… %d more lines · git diff %s %s", len(diff)-shown, shortSHA(f.Baseline), f.Branch)))
	}
	return append(lines, indent(diff)...)
}

// diffLines colours a diff as git does. Compact cuts each file's headers to
// its name.
func diffLines(st watchStyles, diff string, compact bool) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		switch {
		case compact && (strings.HasPrefix(l, "diff --git") || strings.HasPrefix(l, "index ") || strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "new file mode") || strings.HasPrefix(l, "deleted file mode")):
		case compact && strings.HasPrefix(l, "+++ "):
			lines = append(lines, st.emphasis(strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")))
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "diff --git"), strings.HasPrefix(l, "index "):
			lines = append(lines, st.emphasis(l))
		case strings.HasPrefix(l, "@@"):
			lines = append(lines, st.teal(l))
		case strings.HasPrefix(l, "+"):
			lines = append(lines, st.success(l))
		case strings.HasPrefix(l, "-"):
			lines = append(lines, st.err(l))
		default:
			lines = append(lines, l)
		}
	}
	return lines
}

func indent(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = "  " + l
	}
	return out
}

// shortSHA is the first seven characters of a commit's SHA.
func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// withSelectedChange adds reading the selected run's change, when it needs
// reading, to what an update returns.
func withSelectedChange(next tea.Model, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	m, ok := next.(Model)
	if !ok {
		return next, cmd
	}
	m, read := m.loadSelectedChange()
	return m, tea.Batch(cmd, read)
}
