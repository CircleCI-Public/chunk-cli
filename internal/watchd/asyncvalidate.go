package watchd

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// MaxTasksPerProject caps retained validation tasks per project. Only finished
// tasks are evicted, so a project cannot lose a run that is still going.
const MaxTasksPerProject = 20

// TaskState is a validation task as reported to a caller.
type TaskState struct {
	ID          string    `json:"id"`
	ProjectRoot string    `json:"project_root"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Running     bool      `json:"running"`
	// ExitCode is the validate run's exit status. Meaningful only once the task
	// has finished.
	ExitCode int `json:"exit_code"`
	// Output is what the run printed, held for whoever collects the result.
	Output string `json:"output,omitempty"`
	// Stale reports that the working tree changed while the run was in flight,
	// so the result describes code that is no longer on disk.
	Stale bool `json:"stale"`
}

// Passed reports whether a finished task validated the tree successfully.
func (t TaskState) Passed() bool {
	return !t.Running && t.ExitCode == 0
}

// taskEntry is a validation task in flight or finished, with the tree it began
// against.
type taskEntry struct {
	state TaskState
	// start fingerprints the tree the run was launched on. Comparing it against
	// a fresh fingerprint when the run ends is the whole of the staleness check:
	// two Worktrees with equal Head and Digest describe identical content, so an
	// inequality means an edit landed mid-run.
	start  gitutil.Worktree
	cancel context.CancelFunc
}

// runFn executes a validation run and reports its exit code and output. It
// exists so tests can drive the store without a real validate run.
type runFn func(ctx context.Context) (exitCode int, output string)

// taskStore tracks asynchronous validation runs the daemon has in flight.
//
// It exists because async validation breaks the assumption every other validate
// path relies on: that somebody is waiting for the answer. A synchronous run
// hands its result straight back down the connection and is then over, so
// nothing needs to remember it. Release the caller and the run becomes
// homeless — something has to hold which project it was for, whether it is still
// going, and what it concluded, until a later turn comes to collect it.
//
// It is deliberately in-memory, like outputStore: a daemon restart loses
// in-flight tasks. Surviving a restart needs a durable record of what was
// running and a way to decide whether re-running is wanted, which is a larger
// question than this store answers. A lost task reports nothing rather than
// reporting wrongly, which is the safe direction to fail.
type taskStore struct {
	// parent bounds every run this store starts, tying a run's lifetime to the
	// daemon's structurally rather than relying on stopAll being deferred.
	parent context.Context

	mu        sync.Mutex
	tasks     map[string]*taskEntry
	byProject map[string][]string // canonical project root → task IDs, oldest first

	// fingerprint and now are fields so tests can drive staleness and timing
	// without a git tree or a clock.
	fingerprint func(dir string) (gitutil.Worktree, error)
	now         func() time.Time
}

func newTaskStore(parent context.Context) *taskStore {
	if parent == nil {
		parent = context.Background()
	}
	return &taskStore{
		parent:      parent,
		tasks:       make(map[string]*taskEntry),
		byProject:   make(map[string][]string),
		fingerprint: gitutil.Fingerprint,
		now:         time.Now,
	}
}

// start launches run for root in the background and returns the new task's ID.
//
// It refuses a tree it cannot fingerprint, because staleness is only detectable
// against a recorded fingerprint: without one, a result that finishes after an
// edit is indistinguishable from one that finishes before it, and would be
// reported as if it described the current tree. Callers are expected to fall
// back to running synchronously, where the answer reaches whoever asked for it
// while it is still true.
func (s *taskStore) start(root string, run runFn) (string, error) {
	root = config.CanonicalProjectRoot(root)
	start, err := s.fingerprint(root)
	if err != nil {
		return "", err
	}

	id := uuid.NewString()
	ctx, cancel := context.WithCancel(s.parent)
	entry := &taskEntry{
		state: TaskState{
			ID:          id,
			ProjectRoot: root,
			StartedAt:   s.now(),
			Running:     true,
		},
		start:  start,
		cancel: cancel,
	}

	s.mu.Lock()
	s.tasks[id] = entry
	s.byProject[root] = append(s.byProject[root], id)
	s.evictLocked(root)
	s.mu.Unlock()

	go func() {
		defer cancel()
		exitCode, output := run(ctx)
		s.finish(id, exitCode, output)
	}()

	return id, nil
}

// finish records a run's result and decides whether it still describes the tree.
func (s *taskStore) finish(id string, exitCode int, output string) {
	// Fingerprinted before taking the lock: reading the tree shells out to git,
	// and holding the store's mutex across that would stall every reader.
	s.mu.Lock()
	entry, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	root := entry.state.ProjectRoot
	before := entry.start
	s.mu.Unlock()

	after, err := s.fingerprint(root)
	// A tree that cannot be fingerprinted now is treated as stale. The run began
	// against a known tree and ends against an unknown one, so there is no
	// evidence the result still holds — and claiming it does is the one outcome
	// worse than staying quiet.
	stale := err != nil || after.Head != before.Head || after.Digest != before.Digest

	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok = s.tasks[id]
	if !ok {
		return
	}
	entry.state.Running = false
	entry.state.FinishedAt = s.now()
	entry.state.ExitCode = exitCode
	entry.state.Output = output
	entry.state.Stale = stale
}

// collect returns the finished tasks for root and forgets them, so a result
// reaches a caller once and is not repeated on the following turn.
//
// Stale tasks are dropped rather than returned: the current policy is to discard
// an answer about code that has since changed. They are still removed, because a
// task nobody will ever be told about is only taking up room.
func (s *taskStore) collect(root string) []TaskState {
	root = config.CanonicalProjectRoot(root)

	s.mu.Lock()
	defer s.mu.Unlock()

	ids := s.byProject[root]
	kept := make([]string, 0, len(ids))
	var out []TaskState
	for _, id := range ids {
		entry, ok := s.tasks[id]
		if !ok {
			continue
		}
		if entry.state.Running {
			kept = append(kept, id)
			continue
		}
		if !entry.state.Stale {
			out = append(out, entry.state)
		}
		delete(s.tasks, id)
	}
	if len(kept) == 0 {
		delete(s.byProject, root)
	} else {
		s.byProject[root] = kept
	}
	return out
}

// inFlight reports the tasks still running for root, for the dashboard and for
// callers deciding whether to queue another run.
func (s *taskStore) inFlight(root string) []TaskState {
	root = config.CanonicalProjectRoot(root)

	s.mu.Lock()
	defer s.mu.Unlock()

	var out []TaskState
	for _, id := range s.byProject[root] {
		if entry, ok := s.tasks[id]; ok && entry.state.Running {
			out = append(out, entry.state)
		}
	}
	return out
}

// evictLocked drops finished tasks until root is under MaxTasksPerProject.
// A running task is never evicted: cancelling a run to make room would lose
// work that is about to produce an answer.
func (s *taskStore) evictLocked(root string) {
	ids := s.byProject[root]
	for len(ids) > MaxTasksPerProject {
		oldest := -1
		for i, id := range ids {
			if entry, ok := s.tasks[id]; ok && !entry.state.Running {
				oldest = i
				break
			}
		}
		if oldest == -1 {
			// Every retained task is still running. Going over the cap is better
			// than dropping a run that has not reported yet.
			break
		}
		delete(s.tasks, ids[oldest])
		ids = append(ids[:oldest], ids[oldest+1:]...)
	}
	s.byProject[root] = ids
}

// stopAll cancels every in-flight run, for daemon shutdown.
func (s *taskStore) stopAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entry := range s.tasks {
		if entry.cancel != nil {
			entry.cancel()
		}
	}
}
