package factory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// ErrNoRecord means a run left no record to continue it from: there was no
// such run, or chunk made it before runs kept one.
var ErrNoRecord = errors.New("no record of the run")

// Record is what a later run needs to continue a run: the request, and where
// its work is and what it is measured against. It is kept in the project's
// data directory beside the run's worktree, so it outlives the daemon's
// sessions.
type Record struct {
	RunID string `json:"run_id"`
	// Prompt is the original request. A continued run keeps it, so a chain of
	// runs is all one change.
	Prompt   string `json:"prompt"`
	Worktree string `json:"worktree"`
	Branch   string `json:"branch"`
	Baseline string `json:"baseline"`
	Head     string `json:"head"`
	// ContinuesRunID is the run this one continued, if it did.
	ContinuesRunID string `json:"continues_run_id,omitempty"`
	// Result and Rounds are how the run ended, once it has.
	Result Result `json:"result,omitempty"`
	Rounds int    `json:"rounds,omitempty"`
}

// ParseRunID accepts a run's ID or its branch, as chunk factory prints both.
// Factory branch names end in the stable run ID; older branches contain only
// that ID after branchPrefix.
func ParseRunID(s string) string {
	run := strings.TrimPrefix(strings.TrimSpace(s), branchPrefix)
	if i := strings.LastIndexByte(run, '/'); i >= 0 {
		return run[i+1:]
	}
	return run
}

func recordPath(dataDir, runID string) string {
	return filepath.Join(dataDir, "factory", runID+".json")
}

// LoadRecord reads the record of the run runID in the project at root.
func LoadRecord(root, runID string) (Record, error) {
	dataDir, err := config.ProjectDataDir(root)
	if err != nil {
		return Record{}, fmt.Errorf("find chunk's data directory for the project: %w", err)
	}
	b, err := os.ReadFile(recordPath(dataDir, runID))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, fmt.Errorf("run %s: %w", runID, ErrNoRecord)
	}
	if err != nil {
		return Record{}, fmt.Errorf("read the record of run %s: %w", runID, err)
	}
	var r Record
	if err := json.Unmarshal(b, &r); err != nil {
		return Record{}, fmt.Errorf("read the record of run %s: %w", runID, err)
	}
	return r, nil
}

// saveRecord writes r to the project's data directory, replacing what an
// earlier save wrote.
func saveRecord(dataDir string, r Record) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the run's record: %w", err)
	}
	p := recordPath(dataDir, r.RunID)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return fmt.Errorf("save the run's record: %w", err)
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("save the run's record: %w", err)
	}
	if err := os.Rename(tmp, p); err != nil {
		return fmt.Errorf("save the run's record: %w", err)
	}
	return nil
}
