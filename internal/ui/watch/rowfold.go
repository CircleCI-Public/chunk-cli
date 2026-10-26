package watch

import (
	"fmt"
	"maps"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// A row inside a round — the implementer's turn, a check, a review — folds
// open under itself to show what is behind it, so it can be read where the
// run is shown. A fold is capped at maxFoldLines; Enter still opens the
// output screen for all of it, or to follow a review that is still running.

// maxFoldLines caps how many lines a row's fold spends of the run pane.
const maxFoldLines = 15

// foldIndent is where a row's fold content starts, under the row's name.
const foldIndent = "        "

// reviewKey names one review of one round of a run, for the findings
// fetched to show in its fold.
type reviewKey struct {
	runID string
	round int // the round's Number
	name  string
}

// reviewFoldMsg carries a finished review's findings back for its fold.
// found is false when the daemon held none for it yet, so opening the fold
// again asks again.
type reviewFoldMsg struct {
	key   reviewKey
	text  string
	found bool
}

// rowOpen reports whether row it of runID is folded open.
func (m Model) rowOpen(runID string, it runItem) bool {
	return m.rowsOpen[runID][it]
}

// rowFoldable reports whether row it of s has anything to show in a fold:
// the implementer once it has reported, a check whose output was kept, a
// review once it has finished.
func rowFoldable(s chunkd.Session, it runItem) bool {
	if it.round < 0 || it.round >= len(s.Rounds) {
		return false
	}
	r := s.Rounds[it.round]
	switch it.kind { //nolint:exhaustive // only rows inside a round fold this way
	case itemImplement:
		return r.Implement != nil && implementText(r.Implement) != ""
	case itemCheck:
		return it.idx < len(r.Checks) && checkText(r.Checks[it.idx]) != ""
	case itemReview:
		if it.idx >= len(r.Reviews) {
			return false
		}
		st := r.Reviews[it.idx].State
		return st == chunkd.PromptDone || st == chunkd.PromptFailed
	}
	return false
}

// implementText is the implementer's account of its turn: what went wrong,
// what it said it did, and its diff stat.
func implementText(impl *chunkd.RoundImplement) string {
	var parts []string
	for _, s := range []string{impl.Error, impl.Summary, impl.Stat} {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// checkText is what a check left behind: its error and the output kept for
// it, which is only a failing command's.
func checkText(c chunkd.RoundCheck) string {
	text := strings.TrimRight(c.Output, "\n")
	if c.Error != "" {
		text = strings.TrimSpace(c.Error + "\n\n" + text)
	}
	return text
}

// toggleRow folds the selected row inside a round open or shut. A finished
// review's findings are not in the snapshot, so opening its fold the first
// time fetches them.
func (m Model) toggleRow(run *sessionInfo, it runItem) (Model, tea.Cmd) {
	if !rowFoldable(run.s, it) {
		return m, nil
	}
	byRun := maps.Clone(m.rowsOpen)
	if byRun == nil {
		byRun = map[string]map[runItem]bool{}
	}
	rows := maps.Clone(byRun[run.s.ID])
	if rows == nil {
		rows = map[runItem]bool{}
	}
	open := !rows[it]
	rows[it] = open
	byRun[run.s.ID] = rows
	m.rowsOpen = byRun
	if !open || it.kind != itemReview {
		return m, nil
	}
	r := run.s.Rounds[it.round]
	key := reviewKey{runID: run.s.ID, round: r.Number, name: r.Reviews[it.idx].Name}
	if f, ok := m.reviewFolds[key]; ok && f.found {
		return m, nil
	}
	return m, fetchReviewFold(m.detailFn, key, r.Reviews[it.idx])
}

func fetchReviewFold(fetch func(string) (chunkd.SessionDetail, error), key reviewKey, rv chunkd.ReviewPrompt) tea.Cmd {
	return func() tea.Msg {
		d, err := fetch(key.runID)
		if err != nil {
			return reviewFoldMsg{key: key, text: "Could not read the review: " + err.Error()}
		}
		if res, ok := reviewResult(d, key.round, key.name); ok {
			return reviewFoldMsg{key: key, text: reviewText(res), found: true}
		}
		if rv.Error != "" {
			return reviewFoldMsg{key: key, text: "Could not run: " + rv.Error, found: true}
		}
		return reviewFoldMsg{key: key, text: "Its findings are not recorded yet."}
	}
}

// withReviewFold keeps a review's fetched findings for its fold. An answer
// that holds none yet is shown but not trusted, so opening the fold again
// asks again.
func (m Model) withReviewFold(msg reviewFoldMsg) Model {
	folds := maps.Clone(m.reviewFolds)
	if folds == nil {
		folds = map[reviewKey]reviewFold{}
	}
	folds[msg.key] = reviewFold{text: msg.text, found: msg.found}
	m.reviewFolds = folds
	return m
}

// reviewFold is what a review's fold shows once fetched.
type reviewFold struct {
	text  string
	found bool
}

// rowFoldLines draws an open row's fold: its text under the row, cut to
// maxFoldLines. A check's output is a log, read from its end and one line
// per line; the implementer's account and a review's findings are prose,
// read from the start and wrapped.
func (m Model) rowFoldLines(st watchStyles, s chunkd.Session, it runItem) []string {
	width := max(m.runPaneWidth()-len(foldIndent), 20)
	r := s.Rounds[it.round]
	var lines []string
	fromEnd := false
	switch it.kind { //nolint:exhaustive // only rows inside a round fold this way
	case itemImplement:
		lines = strings.Split(lipgloss.Wrap(implementText(r.Implement), width, ""), "\n")
	case itemCheck:
		fromEnd = true
		for _, l := range strings.Split(checkText(r.Checks[it.idx]), "\n") {
			lines = append(lines, truncate(strings.TrimRight(resolveCR(ansi.Strip(l)), " \t"), width))
		}
	case itemReview:
		f, ok := m.reviewFolds[reviewKey{runID: s.ID, round: r.Number, name: r.Reviews[it.idx].Name}]
		if !ok {
			return []string{foldIndent + st.dim("loading…")}
		}
		lines = strings.Split(lipgloss.Wrap(f.text, width, ""), "\n")
	}

	var trailer string
	if extra := len(lines) - maxFoldLines; extra > 0 {
		if fromEnd {
			lines = lines[extra:]
			trailer = fmt.Sprintf("… %d earlier line%s", extra, plural(extra))
		} else {
			lines = lines[:maxFoldLines]
			trailer = fmt.Sprintf("… %d more line%s", extra, plural(extra))
		}
	}
	out := make([]string, 0, len(lines)+1)
	if fromEnd && trailer != "" {
		out = append(out, foldIndent+st.vdim(trailer))
	}
	for _, l := range lines {
		out = append(out, foldIndent+st.muted(l))
	}
	if !fromEnd && trailer != "" {
		out = append(out, foldIndent+st.vdim(trailer))
	}
	return out
}
