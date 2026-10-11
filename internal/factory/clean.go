package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/gitexec"
)

// RunState says what became of a run.
type RunState string

// The states a run in the inventory can be in.
const (
	// StateRunning means an active daemon session holds one of the run's IDs.
	StateRunning RunState = "running"
	// StateFinished means the latest record has a result.
	StateFinished RunState = "finished"
	// StateAbandoned means a record exists with no result and no active session.
	StateAbandoned RunState = "abandoned"
	// StateOrphaned means a worktree or branch exists but no record does.
	StateOrphaned RunState = "orphaned"
)

// runIDLayout is the UTC timestamp a run ID is made from.
const runIDLayout = "20060102-150405"

// RunEntry is a run, or a chain of continued runs that share one worktree and
// branch, found in a project.
type RunEntry struct {
	// RunIDs are the runs in the entry, oldest first.
	RunIDs []string `json:"run_ids"`
	// RunID is the newest of RunIDs, for callers that do not parse the list.
	RunID    string   `json:"run_id"`
	Branch   string   `json:"branch,omitempty"`
	Worktree string   `json:"worktree,omitempty"`
	State    RunState `json:"state"`
	// Result and Rounds come from the latest record, if there is one.
	Result Result `json:"result,omitempty"`
	Rounds int    `json:"rounds,omitempty"`
	// WorktreeExists and Dirty describe the worktree directory. Dirty means
	// uncommitted changes, or a status that could not be read.
	WorktreeExists bool `json:"worktree_exists"`
	Dirty          bool `json:"dirty"`
	// StartedAt is when the latest run started, read from its ID. It is zero
	// when the ID is not a timestamp.
	StartedAt time.Time `json:"started_at,omitzero"`
	// Prompt is the first line of the request.
	Prompt string `json:"prompt,omitempty"`

	recordIDs []string
}

// ID is the entry's latest run ID.
func (e RunEntry) ID() string {
	if len(e.RunIDs) == 0 {
		return ""
	}
	return e.RunIDs[len(e.RunIDs)-1]
}

// Has reports whether runID is one of the entry's runs.
func (e RunEntry) Has(runID string) bool {
	for _, id := range e.RunIDs {
		if id == runID {
			return true
		}
	}
	return false
}

// Inventory finds every factory run in the project at root by joining the run
// records, the git worktrees on factory branches and the factory branches
// themselves, so a run is found when any one of them is gone. Runs sharing a
// worktree or branch are one entry. active is the set of run IDs with a live
// daemon session; the newest entries come first.
func Inventory(ctx context.Context, root string, active map[string]bool) ([]RunEntry, error) {
	dataDir, err := config.ProjectDataDir(root)
	if err != nil {
		return nil, fmt.Errorf("find smelt's data directory for the project: %w", err)
	}
	records, err := listRecords(dataDir)
	if err != nil {
		return nil, err
	}
	git := gitexec.Runner{Dir: root}
	worktrees, err := factoryWorktrees(ctx, git)
	if err != nil {
		return nil, err
	}
	branches, err := factoryBranches(ctx, git)
	if err != nil {
		return nil, err
	}

	var g grouper
	for _, r := range records {
		grp := g.add(r.Branch, r.Worktree)
		grp.records = append(grp.records, r)
	}
	for _, w := range worktrees {
		g.add(w.branch, w.path)
	}
	for _, b := range branches {
		g.add(b, "")
	}

	entries := make([]RunEntry, 0, len(g.groups))
	for _, grp := range g.groups {
		entries = append(entries, grp.entry(ctx, active))
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].ID() > entries[j].ID() })
	return entries, nil
}

// listRecords reads the run records in the project's data directory. A record
// that cannot be read is skipped: it must not stop the rest being listed.
func listRecords(dataDir string) ([]Record, error) {
	files, err := filepath.Glob(filepath.Join(dataDir, "factory", "*.json"))
	if err != nil {
		return nil, fmt.Errorf("list run records: %w", err)
	}
	var records []Record
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var r Record
		if json.Unmarshal(b, &r) != nil {
			continue
		}
		// The file name is the record's identity: it is what is deleted.
		r.RunID = strings.TrimSuffix(filepath.Base(f), ".json")
		records = append(records, r)
	}
	return records, nil
}

type worktreeInfo struct{ path, branch string }

// factoryWorktrees lists the worktrees checked out on factory branches.
func factoryWorktrees(ctx context.Context, git gitexec.Runner) ([]worktreeInfo, error) {
	out, err := git.Output(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}
	var found []worktreeInfo
	var cur worktreeInfo
	flush := func() {
		if strings.HasPrefix(cur.branch, branchPrefix) {
			found = append(found, cur)
		}
		cur = worktreeInfo{}
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case line == "":
			flush()
		case strings.HasPrefix(line, "worktree "):
			cur.path = strings.TrimPrefix(line, "worktree ")
		case strings.HasPrefix(line, "branch refs/heads/"):
			cur.branch = strings.TrimPrefix(line, "branch refs/heads/")
		}
	}
	flush()
	return found, nil
}

// factoryBranches lists the local factory branches.
func factoryBranches(ctx context.Context, git gitexec.Runner) ([]string, error) {
	out, err := git.Output(ctx, "for-each-ref", "--format=%(refname:strip=2)", "refs/heads/"+branchPrefix)
	if err != nil {
		return nil, fmt.Errorf("list factory branches: %w", err)
	}
	return strings.Fields(string(out)), nil
}

// group is the sources that turned out to describe one run, or one chain.
type group struct {
	branch, worktree string
	records          []Record
}

// grouper joins sources that share a branch or a worktree.
type grouper struct{ groups []*group }

// add returns the group a source belongs to, making it if it is new and
// merging groups the source links together.
func (g *grouper) add(branch, worktree string) *group {
	var home *group
	kept := g.groups[:0]
	for _, grp := range g.groups {
		same := (branch != "" && grp.branch == branch) ||
			(worktree != "" && grp.worktree != "" && samePath(grp.worktree, worktree))
		switch {
		case !same:
			kept = append(kept, grp)
		case home == nil:
			home = grp
			kept = append(kept, grp)
		default:
			home.records = append(home.records, grp.records...)
			if home.branch == "" {
				home.branch = grp.branch
			}
			if home.worktree == "" {
				home.worktree = grp.worktree
			}
		}
	}
	g.groups = kept
	if home == nil {
		home = &group{}
		g.groups = append(g.groups, home)
	}
	if home.branch == "" {
		home.branch = branch
	}
	if home.worktree == "" {
		home.worktree = worktree
	}
	return home
}

// samePath compares paths by where they lead, since git reports a worktree by
// its resolved path.
func samePath(a, b string) bool {
	return resolvePath(a) == resolvePath(b)
}

func resolvePath(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved
	}
	return filepath.Clean(p)
}

// entry works out the group's state from its records and its worktree.
func (g *group) entry(ctx context.Context, active map[string]bool) RunEntry {
	sort.Slice(g.records, func(i, j int) bool { return g.records[i].RunID < g.records[j].RunID })
	e := RunEntry{Branch: g.branch, Worktree: g.worktree, State: StateOrphaned}
	for _, r := range g.records {
		e.RunIDs = append(e.RunIDs, r.RunID)
		e.recordIDs = append(e.recordIDs, r.RunID)
	}
	if len(g.records) == 0 && g.branch != "" {
		e.RunIDs = []string{ParseRunID(g.branch)}
	}
	if len(g.records) > 0 {
		latest := g.records[len(g.records)-1]
		e.Result, e.Rounds = latest.Result, latest.Rounds
		e.Prompt = firstLine(latest.Prompt)
		e.State = StateAbandoned
		if latest.Result != "" {
			e.State = StateFinished
		}
	}
	for _, id := range e.RunIDs {
		if active[id] {
			e.State = StateRunning
		}
	}
	e.RunID = e.ID()
	if t, err := time.Parse(runIDLayout, e.ID()); err == nil {
		e.StartedAt = t.UTC()
	}
	if e.Worktree != "" {
		if info, err := os.Stat(e.Worktree); err == nil && info.IsDir() {
			e.WorktreeExists = true
			e.Dirty = worktreeDirty(ctx, e.Worktree)
		}
	}
	return e
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(line)
}

// worktreeDirty reports uncommitted changes in the worktree. A status that
// cannot be read counts as dirty, so unknown work is never discarded.
func worktreeDirty(ctx context.Context, path string) bool {
	out, err := (gitexec.Runner{Dir: path}).Output(ctx, "status", "--porcelain")
	return err != nil || strings.TrimSpace(string(out)) != ""
}

// Removal is what RemoveRun took away. Parts already gone are not counted.
type Removal struct {
	Worktree bool
	Branch   bool
	Records  int
}

// ErrRunning means the run is in progress and is never removed.
var ErrRunning = errors.New("the run is still in progress")

// ErrDirty means the worktree holds uncommitted changes that force would discard.
var ErrDirty = errors.New("the worktree has uncommitted changes")

// RemoveRun deletes the entry's worktree, branch and records. It never removes
// a running entry, and refuses a dirty worktree unless force is set. Each step
// tolerates what is already gone, so a clean that was interrupted can be run
// again. The branch is deleted with -D because a factory branch is never merged
// in git's sense; the caller's confirmation is what makes that safe.
func RemoveRun(ctx context.Context, root string, e RunEntry, force bool) (Removal, error) {
	var done Removal
	if e.State == StateRunning {
		return done, fmt.Errorf("run %s: %w", e.ID(), ErrRunning)
	}
	if e.Dirty && !force {
		return done, fmt.Errorf("run %s: %w at %s", e.ID(), ErrDirty, e.Worktree)
	}
	if e.Branch != "" && !strings.HasPrefix(e.Branch, branchPrefix) {
		return done, fmt.Errorf("run %s: %q is not a factory branch", e.ID(), e.Branch)
	}
	dataDir, err := config.ProjectDataDir(root)
	if err != nil {
		return done, fmt.Errorf("find smelt's data directory for the project: %w", err)
	}
	git := gitexec.Runner{Dir: root}

	if e.Worktree != "" {
		if done.Worktree, err = removeEntryWorktree(ctx, git, e, force); err != nil {
			return done, err
		}
	}
	if e.Branch != "" {
		if done.Branch, err = removeEntryBranch(ctx, git, e.Branch); err != nil {
			return done, err
		}
	}
	for _, id := range e.recordIDs {
		err := os.Remove(recordPath(dataDir, id))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return done, fmt.Errorf("delete the record of run %s: %w", id, err)
		}
		done.Records++
	}
	return done, nil
}

func removeEntryWorktree(ctx context.Context, git gitexec.Runner, e RunEntry, force bool) (bool, error) {
	if _, err := os.Stat(e.Worktree); errors.Is(err, os.ErrNotExist) {
		if out, err := git.CombinedOutput(ctx, "worktree", "prune"); err != nil {
			return false, fmt.Errorf("prune worktrees: %w: %s", err, strings.TrimSpace(string(out)))
		}
		return false, nil
	}
	args := []string{"worktree", "remove"}
	if force {
		args = append(args, "--force")
	}
	args = append(args, e.Worktree)
	if out, err := git.CombinedOutput(ctx, args...); err != nil {
		return false, fmt.Errorf("remove worktree %s: %w: %s", e.Worktree, err, strings.TrimSpace(string(out)))
	}
	return true, nil
}

func removeEntryBranch(ctx context.Context, git gitexec.Runner, branch string) (bool, error) {
	if git.Run(ctx, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) != nil {
		return false, nil
	}
	if out, err := git.CombinedOutput(ctx, "branch", "-D", branch); err != nil {
		return false, fmt.Errorf("delete branch %s: %w: %s", branch, err, strings.TrimSpace(string(out)))
	}
	return true, nil
}
