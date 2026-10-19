package watch

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
)

// runChange is a finished run's committed change: git's diff of its branch
// against its baseline, or why it could not be read.
type runChange struct {
	loaded bool
	diff   string
	err    error
	lines  *diffCache
}

// diffCache keeps a diff's coloured, wrapped lines, which are costly to make
// for a large change and would otherwise be made again on every redraw. It
// holds one diff, so a new diff gets a new cache.
type diffCache struct {
	compact, dark bool
	width, size   int
	lines         []string
}

// diffLines is diffLines of diff, made again only when the pane's width,
// colours or the way the diff is cut have changed. A nil cache makes it every
// time.
func (c *diffCache) diffLines(st watchStyles, dark bool, diff string, compact bool, width int) []string {
	if c == nil {
		return diffLines(st, diff, compact, width)
	}
	if c.lines == nil || c.compact != compact || c.dark != dark || c.width != width || c.size != len(diff) {
		c.compact, c.dark, c.width, c.size = compact, dark, width, len(diff)
		c.lines = diffLines(st, diff, compact, width)
	}
	return c.lines
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
	args := append([]string{"diff"}, gitexec.PatchFlags...)
	out, err := (gitexec.Runner{Dir: root}).Output(context.Background(), append(args, baseline, branch, "--")...)
	if err != nil {
		return "", fmt.Errorf("read the change: %w", err)
	}
	return gitexec.CapPatch(string(out)), nil
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
	msg.change.lines = &diffCache{}
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
	// The pane is the width renderBody clips it to; the change is indented two.
	width := m.width - leftPaneWidth - 5 - 2
	lines := []string{""}
	for i, l := range wrapText(lastLine(f.Stat), width-len("change  ")) {
		label := st.vdim("change  ")
		if i > 0 {
			label = strings.Repeat(" ", len("change  "))
		}
		lines = append(lines, "  "+label+st.muted(l))
	}
	diff := c.lines.diffLines(st, m.hasDarkBG, c.diff, true, width)
	if fit := room - len(lines); len(diff) > fit {
		// How to see the rest wraps too, so the diff gives up as many lines
		// as it takes.
		more := func(shown int) []string {
			return wrapText(fmt.Sprintf("… %d more lines · git diff %s %s", len(diff)-shown, shortSHA(f.Baseline), f.Branch), width)
		}
		shown := max(fit-len(more(max(fit-1, 0))), 0)
		lines = append(lines, indent(diff[:shown])...)
		for _, l := range more(shown) {
			lines = append(lines, "  "+st.vdim(l))
		}
		return lines[:min(len(lines), room)]
	}
	return append(lines, indent(diff)...)
}

// wrapText wraps s to width columns, at spaces where it can and mid-word
// where a word is wider than that.
func wrapText(s string, width int) []string {
	if width < 1 {
		return []string{s}
	}
	return strings.Split(ansi.Wrap(s, width, ""), "\n")
}

// diffLines colours a diff as git does, wrapped to width columns so no line
// is cut at the pane's edge. A wrapped line continues under its text, past
// its +/- marker. Compact cuts each file's headers to its name.
func diffLines(st watchStyles, diff string, compact bool, width int) []string {
	var lines []string
	add := func(style func(string) string, l string) {
		for _, w := range wrapDiffLine(l, width) {
			lines = append(lines, style(w))
		}
	}
	plain := func(s string) string { return s }
	// A file's lines before its first hunk are git's headers; after it every
	// line starting with + or - is content, even one like "-- comment" that
	// looks like a header.
	inHunk := false
	for _, l := range strings.Split(strings.TrimRight(diff, "\n"), "\n") {
		l = strings.ReplaceAll(l, "\t", "    ")
		if strings.HasPrefix(l, "diff --git") {
			inHunk = false
		}
		switch {
		case inHunk && strings.HasPrefix(l, "+"):
			add(st.success, l)
		case inHunk && strings.HasPrefix(l, "-"):
			add(st.err, l)
		case inHunk && !strings.HasPrefix(l, "@@"):
			add(plain, l)
		case strings.HasPrefix(l, "@@"):
			inHunk = true
			add(st.teal, l)
		case compact && (strings.HasPrefix(l, "diff --git") || strings.HasPrefix(l, "index ") || strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "new file mode") || strings.HasPrefix(l, "deleted file mode")):
		case compact && strings.HasPrefix(l, "+++ "):
			add(st.emphasis, strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/"))
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "diff --git"), strings.HasPrefix(l, "index "):
			add(st.emphasis, l)
		default:
			add(plain, l)
		}
	}
	return lines
}

// wrapDiffLine wraps a diff line to width columns, at spaces where it can.
// A line's continuations are indented one column, so they sit under its text
// rather than under its +/- marker.
func wrapDiffLine(l string, width int) []string {
	if width < 2 || ansi.StringWidth(l) <= width {
		return []string{l}
	}
	_, n := utf8.DecodeRuneInString(l)
	head, rest := l[:n], l[n:]
	wrapped := wrapText(rest, width-1)
	out := make([]string, len(wrapped))
	for i, w := range wrapped {
		if i == 0 {
			out[i] = head + w
			continue
		}
		out[i] = " " + w
	}
	return out
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
