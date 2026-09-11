package watchd

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// historyFile is where a project's finished validate runs are kept, alongside
// its event log.
const historyFile = "risk-history.jsonl"

// maxHistory is how many recent runs are kept per project. Enough to say
// something about a repo's habits, short enough that a habit which has changed
// stops counting fairly quickly.
const maxHistory = 200

// minSimilar is how many comparable runs a project needs before its history is
// allowed to influence anything. Below it, one unlucky afternoon would look
// like a pattern.
const minSimilar = 5

// unusuallyLarge is the percentile past which a change counts as large *for
// this repo*, whatever it measures in absolute lines.
const unusuallyLarge = 90

// historyRecord is one finished validate run.
//
// It records the change, not the diff: sizes and a verdict, no paths and no
// content. That is all the questions this store answers need, and it keeps a
// file that lives in the project's data directory from becoming a copy of the
// developer's code.
type historyRecord struct {
	At     time.Time `json:"at"`
	Lines  int       `json:"lines"`
	Files  int       `json:"files"`
	Inert  bool      `json:"inert,omitempty"`
	Passed bool      `json:"passed"`
}

// riskHistory remembers how validate runs have gone for each project.
//
// It exists to answer the question an absolute threshold cannot: is this change
// risky *for this repo*. Five hundred lines is a rewrite in one codebase and a
// Tuesday in another, and "which of these two changes is riskier" has no answer
// at all without something to compare against.
//
// What it is allowed to do with that answer is deliberately one-directional:
// history can make the daemon more cautious, never less. A repo whose changes
// are all enormous must not thereby teach the daemon that enormous is fine —
// that is how a heuristic learns its way into missing failures. So history can
// hold a run the rules would have released, and can raise a score, and can do
// nothing else.
type riskHistory struct {
	mu     sync.Mutex
	loaded map[string][]historyRecord // canonical project root → recent runs, oldest first
}

func newRiskHistory() *riskHistory {
	return &riskHistory{loaded: make(map[string][]historyRecord)}
}

// record files how a run went. Best-effort: a project with no writable data
// directory keeps working, it just learns nothing.
func (h *riskHistory) record(root string, risk RiskSummary, passed bool) {
	if root == "" {
		return
	}
	rec := historyRecord{
		At:     time.Now(),
		Lines:  risk.Lines,
		Files:  risk.Files,
		Inert:  risk.Inert,
		Passed: passed,
	}
	canonical := config.CanonicalProjectRoot(root)

	h.mu.Lock()
	defer h.mu.Unlock()
	recs := append(h.load(canonical, root), rec)
	if len(recs) > maxHistory {
		recs = recs[len(recs)-maxHistory:]
	}
	h.loaded[canonical] = recs
	h.append(root, rec)
}

// evidence summarises what this project's past runs say about a change of this
// size. A project with too little history reports nothing, which every caller
// reads as "no opinion".
func (h *riskHistory) evidence(root string, risk RiskSummary) historyEvidence {
	if root == "" {
		return historyEvidence{}
	}
	canonical := config.CanonicalProjectRoot(root)

	h.mu.Lock()
	recs := h.load(canonical, root)
	h.mu.Unlock()

	if len(recs) < minSimilar {
		return historyEvidence{}
	}

	var ev historyEvidence
	smaller := 0
	for _, r := range recs {
		if r.Lines < risk.Lines {
			smaller++
		}
		// Comparable means within a factor of two either way. A band rather than
		// an exact size, because no two changes are the same size and a rule that
		// needs them to be would never fire.
		if r.Lines*2 >= risk.Lines && risk.Lines*2 >= r.Lines {
			ev.Similar++
			if !r.Passed {
				ev.Failed++
			}
		}
	}
	ev.Percentile = smaller * 100 / len(recs)
	return ev
}

// load returns the records for a canonical root, reading them off disk once.
// Callers hold h.mu.
func (h *riskHistory) load(canonical, root string) []historyRecord {
	if recs, ok := h.loaded[canonical]; ok {
		return recs
	}
	recs := readHistory(root)
	h.loaded[canonical] = recs
	return recs
}

// append adds one record to the project's history file.
func (h *riskHistory) append(root string, rec historyRecord) {
	dir, err := config.ProjectDataDir(root)
	if err != nil {
		return
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	// The data directory is created on demand, like the event log's: a project
	// the daemon has never written to does not have one yet.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, historyFile), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = f.Write(append(line, '\n'))
}

// readHistory reads the most recent records for root, newest last. A missing or
// unreadable file is an empty history, not an error: nothing here is worth
// failing a validate run over.
func readHistory(root string) []historyRecord {
	dir, err := config.ProjectDataDir(root)
	if err != nil {
		return nil
	}
	f, err := os.Open(filepath.Join(dir, historyFile))
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()

	var recs []historyRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var rec historyRecord
		// A truncated final line — a daemon killed mid-write — is skipped rather
		// than stopping the read, so one bad line costs one record.
		if err := json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			continue
		}
		recs = append(recs, rec)
	}
	if len(recs) > maxHistory {
		recs = recs[len(recs)-maxHistory:]
	}
	sort.SliceStable(recs, func(i, j int) bool { return recs[i].At.Before(recs[j].At) })
	return recs
}

// historyEvidence is what a project's past runs say about a change like the one
// in hand. The zero value means "no opinion", which is what too little history
// produces.
type historyEvidence struct {
	// Similar is how many recent runs measured a change of comparable size, and
	// Failed how many of those failed.
	Similar int
	Failed  int
	// Percentile is the share of recent changes smaller than this one, 0–100.
	Percentile int
}

// suggestsCaution reports whether comparable changes have failed often enough
// here that this one is worth waiting for.
//
// Half is the line. Below it, holding every run would punish a repo for having
// any failures at all; at or above it, "this size of change usually fails here"
// is a fact about the repo worth acting on.
func (e historyEvidence) suggestsCaution() bool {
	return e.Similar >= minSimilar && e.Failed*2 >= e.Similar
}

// unusual reports whether this change is large for this repo, whatever it
// measures in absolute lines.
func (e historyEvidence) unusual() bool {
	return e.Similar > 0 && e.Percentile >= unusuallyLarge
}
