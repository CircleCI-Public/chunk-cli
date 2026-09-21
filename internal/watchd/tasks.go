package watchd

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// MaxTasksPerProject caps retained validation tasks per project. Only finished
// tasks are evicted, so a project cannot lose a run that is still going. It is
// also what bounds delivered results, which collect keeps rather than deletes.
const MaxTasksPerProject = 20

// maxTaskOutput caps what a single finished task retains. Without it the only
// bound on the daemon's heap is MaxTasksPerProject times however verbose the
// project's validate commands are, times however many projects are active —
// and a verbose test run is megabytes.
//
// Nothing collects a result on its own: reading one is something a developer
// asks for, so the retained size has to be survivable for runs nobody ever
// reads. Twenty tasks at this cap is under 2 MB per project.
const maxTaskOutput = 64 * 1024

// tailOutput keeps the last maxTaskOutput bytes of a run's output.
//
// The tail rather than the head because that is where a validate run says what
// failed and prints its tally; the head is commands announcing themselves,
// which is the part a reader can most afford to lose. What went is replaced by
// a marker, so a truncated result cannot be misread as the whole of a short
// one.
//
// The cut is moved forward to the next line break so the tail does not open
// mid-line, but only within lineScanLimit: output with no line breaks at all —
// a progress bar rewriting one line, a single JSON blob — would otherwise give
// back whatever trails its last newline, throwing away almost the whole window
// to tidy the first line. Past that limit the raw cut is kept and only stray
// continuation bytes are trimmed, so the result is still valid UTF-8.
func tailOutput(out string) string {
	if len(out) <= maxTaskOutput {
		return out
	}
	dropped := len(out) - maxTaskOutput
	tail := out[dropped:]
	if i := strings.IndexByte(tail[:min(len(tail), lineScanLimit)], '\n'); i >= 0 {
		dropped += i + 1
		tail = tail[i+1:]
	}
	for len(tail) > 0 && !utf8.RuneStart(tail[0]) {
		dropped++
		tail = tail[1:]
	}
	return fmt.Sprintf("[%d bytes of earlier output dropped]\n%s", dropped, tail)
}

// lineScanLimit bounds how far tailOutput will skip forward to start on a line
// boundary. Enough to clear one partial line of ordinary output, small enough
// that losing it to tidiness costs nothing.
const lineScanLimit = 1024

// TaskState is a validation task as reported to a caller.
type TaskState struct {
	ID string `json:"id"`
	// ProjectRoot is the key the task is filed under — the root of the repo,
	// which may sit above the directory the run was actually asked for. See
	// taskStore.projectKey.
	ProjectRoot string    `json:"project_root"`
	StartedAt   time.Time `json:"started_at"`
	FinishedAt  time.Time `json:"finished_at,omitempty"`
	Running     bool      `json:"running"`
	// ExitCode is the validate run's exit status. Meaningful only once the task
	// has finished.
	ExitCode int `json:"exit_code"`
	// Output is what the run printed, held for whoever collects the result.
	// Capped at maxTaskOutput, keeping the tail.
	Output string `json:"output,omitempty"`
	// Stale reports that the working tree changed between the run starting and
	// its result being read, so the result describes code that is no longer on
	// disk. For a live-tree run ExitCode and Output are cleared when it is set: a
	// stale task is reported so that the discard is visible, and carrying the
	// verdict would invite it to be read as one. For a snapshot run they are
	// kept — see Snapshot, where the verdict outlives the tree moving.
	//
	// The tree most often moved because of the run itself — output written,
	// goldens regenerated, a lockfile touched. Reporting the discard is what
	// lets that be diagnosed instead of looking like no run happened.
	//
	// It is not a fix for the cause. A mid-run edit by the developer and a file
	// written by the run are the same event as far as a content digest is
	// concerned, so nothing here can tell them apart, and relaxing the
	// comparison to let artifacts through would let a real edit through with
	// them — trading a silence for a false pass, which is the one direction this
	// store must not fail in. Actually keeping the result means stopping the
	// tree from moving under the run, which is a matter of where the run
	// happens rather than how its result is judged.
	Stale bool `json:"stale"`
	// Snapshot reports that the run validated a checked-out copy of the tree
	// rather than the tree itself. Such a result is exact about the state it
	// ran against whatever happened afterwards, so it is reported even when
	// stale — qualified rather than thrown away.
	Snapshot bool `json:"snapshot,omitempty"`
	// DeliveredAt records when this result was handed to a caller. A stamped
	// task is never reported again; an unstamped one is still owed to somebody.
	//
	// It exists so that handing a result over and forgetting it are two steps
	// rather than one. See taskStore.collect.
	DeliveredAt time.Time `json:"delivered_at,omitempty"`
}

// Passed reports whether a finished task validated the tree successfully.
//
// A stale live-tree task never passes, whatever its exit code says. Its verdict
// is stripped when it is reported, which leaves ExitCode at zero — so without
// the Stale check a discarded run would read here as a clean pass, which is
// exactly the claim discarding it exists to avoid making.
//
// A stale snapshot task can still pass. It ran against a copy that could not
// move, so its exit code remains exactly true about the state it was handed;
// what staleness says there is that the state has been overtaken, not that the
// verdict is unreliable. Callers are expected to report that qualification —
// see printResults.
func (t TaskState) Passed() bool {
	return !t.Running && t.ExitCode == 0 && (!t.Stale || t.Snapshot)
}

// taskEntry is a validation task in flight or finished, with the tree it began
// against.
type taskEntry struct {
	state TaskState
	// start fingerprints the tree the run was launched on. Comparing it against
	// a fresh fingerprint is the whole of the staleness check: two Worktrees with
	// equal Head and Digest describe identical content, so an inequality means
	// the tree moved. It is compared when the run ends and again when the result
	// is read, because an edit can land in either window.
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

	// fingerprint, repoRoot and now are fields so tests can drive staleness,
	// project identity and timing without a git tree or a clock.
	fingerprint func(dir string) (gitutil.Worktree, error)
	repoRoot    func(from string) (string, error)
	now         func() time.Time

	// onFinish is told how each run ended, project root and pass/fail. It is a
	// hook rather than a direct call so the store keeps knowing only about
	// tracking runs, and the daemon decides what a result means for the next one.
	// Called without the store's lock held, and may be nil.
	onFinish func(root string, passed bool)
}

func newTaskStore(parent context.Context) *taskStore {
	if parent == nil {
		parent = context.Background()
	}
	return &taskStore{
		parent:      parent,
		tasks:       make(map[string]*taskEntry),
		byProject:   make(map[string][]string),
		fingerprint: fingerprintTree,
		repoRoot:    gitutil.RepoRoot,
		now:         time.Now,
	}
}

// projectKey returns the key a task is filed under: the root of the repo the
// given directory sits in, canonicalised.
//
// The repo root rather than the directory itself, because the two ends of an
// async run do not agree on where they are. A developer running
// `chunk validate --async` in /repo/sub files the task under /repo/sub, while
// the hook that reads results runs at the project root and asks for /repo — so
// the result is never handed to anybody, and the run reports nothing at all. The
// repo root is the one spelling both ends land on without having to coordinate.
//
// Staleness is unaffected. gitutil.Fingerprint resolves the repo root itself
// (rev-parse --show-toplevel) and hashes the whole worktree, so a fingerprint
// taken at /repo/sub and one taken at /repo were always the same value; only the
// key was ever per-directory.
//
// Falls back to the directory as given when the repo root cannot be found, which
// keeps a non-repo behaving as it did: start refuses it for want of a
// fingerprint, and a lookup for it finds nothing.
func (s *taskStore) projectKey(dir string) string {
	if dir == "" {
		return ""
	}
	if root, err := s.repoRoot(dir); err == nil && root != "" {
		dir = root
	}
	return config.CanonicalProjectRoot(dir)
}

// start launches run for root in the background and returns the new task's ID.
//
// It refuses a tree it cannot fingerprint, because staleness is only detectable
// against a recorded fingerprint: without one, a result that finishes after an
// edit is indistinguishable from one that finishes before it, and would be
// reported as if it described the current tree. Callers are expected to fall
// back to running synchronously, where the answer reaches whoever asked for it
// while it is still true.
func (s *taskStore) start(root string, snapshot bool, run runFn) (string, error) {
	root = s.projectKey(root)
	start, err := s.fingerprint(root)
	if err != nil {
		return "", err
	}

	s.mu.Lock()
	// Refused rather than queued. evictLocked will not reclaim a running task —
	// cancelling work that has not reported yet is worse than going over the
	// cap — so without this nothing bounds the store at all: every start past
	// the cap adds an entry and a goroutine that no eviction can take back.
	//
	// Runs are serialised against each other, so tasks past the first are not
	// even progressing; they are parked on the mutex holding whatever their
	// environment captured. Refusing sends the caller inline instead, which is
	// where an answer it actually waits for comes from.
	if n := s.runningLocked(root); n >= MaxTasksPerProject {
		s.mu.Unlock()
		return "", fmt.Errorf("%d runs already in flight for this project", n)
	}

	id := uuid.NewString()
	ctx, cancel := context.WithCancel(s.parent)
	entry := &taskEntry{
		state: TaskState{
			ID:          id,
			ProjectRoot: root,
			StartedAt:   s.now(),
			Running:     true,
			Snapshot:    snapshot,
		},
		start:  start,
		cancel: cancel,
	}

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
	// The project and the baseline it started against, copied out so the lock can
	// be dropped before the fingerprint below.
	s.mu.Lock()
	entry, ok := s.tasks[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	root := entry.state.ProjectRoot
	before := entry.start
	s.mu.Unlock()

	// Read with the lock released: fingerprinting shells out to git, and holding
	// the mutex across it would stall every other caller.
	after, err := s.fingerprint(root)
	// A tree that cannot be fingerprinted now is treated as stale. The run began
	// against a known tree and ends against an unknown one, so there is no
	// evidence the result still holds — and claiming it does is the one outcome
	// worse than staying quiet.
	stale := err != nil || after.Head != before.Head || after.Digest != before.Digest

	s.mu.Lock()
	entry, ok = s.tasks[id]
	if ok {
		entry.state.Running = false
		entry.state.FinishedAt = s.now()
		entry.state.ExitCode = exitCode
		entry.state.Output = tailOutput(output)
		entry.state.Stale = stale
	}
	s.mu.Unlock()
	if !ok {
		return
	}

	// Reported even when the result is stale, and even though a stale result is
	// never handed to a caller. A failure that went out of date is still the last
	// thing known about this project, and the run that replaces it should be one
	// somebody is waiting for.
	if s.onFinish != nil {
		s.onFinish(root, exitCode == 0)
	}
}

// peek returns the finished tasks for root that nobody has been told about yet,
// without recording them as delivered. Calling it twice returns the same tasks.
//
// Reading and acknowledging are separate because the caller that matters is an
// HTTP handler: it has to encode the response and write it to a socket, and
// either can fail after the store has been told the result went out. Marking
// delivery before the bytes leave means a failure there loses the result with no
// trace — and the client turns a transport failure into ErrDaemonUnavailable,
// which the results hook treats as "no daemon, nothing to say" and reports as
// silence. A validation failure would vanish into a successful-looking no-op.
//
// Staleness is decided here rather than only when a run finished, because this
// is where the question is actually asked: a caller wants to know whether the
// answer describes the code in front of it now. Checking only at completion
// misses the common shape — a run that finished seconds ago, an edit since, and
// a result that was true when it was recorded and is not true when it is read.
//
// A stale task is returned with its verdict stripped rather than dropped. The
// answer is still discarded — an outcome about code that has since changed is
// never reported as a verdict — but the fact that a run was discarded is not,
// because the commonest reason the tree moved is the run itself. A validate
// command that writes coverage output, regenerates a golden or touches a
// lockfile changes a path git is watching and invalidates the result it just
// produced. Dropping that in silence is indistinguishable from no run having
// happened, so a project whose commands are not perfectly gitignored would get
// nothing, every turn, with no way to tell why.
func (s *taskStore) peek(root string) []TaskState {
	root = s.projectKey(root)

	// Read before the lock: fingerprinting shells out to git, and holding the
	// mutex across it would stall every other caller. Read once for the whole
	// project rather than per task, since they all compare against one tree.
	now, err := s.fingerprint(root)
	// A tree that cannot be fingerprinted now cannot be shown to match anything,
	// so nothing is reported for it. The alternative is claiming a result is
	// current without evidence, which is the outcome this store exists to avoid.
	unverifiable := err != nil

	s.mu.Lock()
	defer s.mu.Unlock()

	var out []TaskState
	for _, id := range s.byProject[root] {
		// Present by construction: evictLocked is the only thing that removes a
		// task, and it drops the id from this slice in the same step.
		entry, ok := s.tasks[id]
		if !ok {
			continue
		}
		if entry.state.Running || !entry.state.DeliveredAt.IsZero() {
			continue
		}
		// Stale either because the tree moved while the run was in flight, or
		// because it has moved since the run ended. Both mean the same thing to
		// whoever is about to read the result.
		movedSince := unverifiable || now.Head != entry.start.Head || now.Digest != entry.start.Digest
		reported := entry.state
		if entry.state.Stale || movedSince {
			reported.Stale = true
			if !entry.state.Snapshot {
				// A live-tree run is handed over as stale rather than dropped, with
				// its verdict stripped before it goes (see TaskState.Stale): what is
				// being reported is that a run was discarded, not what it concluded.
				//
				// It has to be reported at all because the commonest reason the tree
				// moved is the run itself — a validate command that writes coverage
				// output, regenerates a golden or touches a lockfile changes a path
				// git is watching, and the result it just produced is thrown out on
				// that basis. Dropped in silence that is indistinguishable from no run
				// having happened, so a project whose commands are not perfectly
				// gitignored gets nothing, forever, with nothing to say why.
				reported.ExitCode = 0
				reported.Output = ""
			}
			// A snapshot-backed run keeps its verdict. It validated a copy that
			// cannot move, so the answer is still exactly true about the state it ran
			// against — reported with that said rather than discarded, because the
			// work was done, and "your code passed as of the end of that turn" is
			// worth more than silence.
		}
		out = append(out, reported)
	}
	return out
}

// acknowledge records that tasks reached a caller, so they are not reported
// again. Tasks a peek has already handed over but nobody acknowledged stay
// undelivered and come back on the next peek.
//
// Stamping rather than deleting keeps the record for the cap to reclaim (see
// evictLocked, which takes delivered results before unread ones) and leaves
// something behind to show the run happened at all.
func (s *taskStore) acknowledge(tasks []TaskState) {
	if len(tasks) == 0 {
		return
	}
	at := s.now()

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range tasks {
		entry, ok := s.tasks[t.ID]
		if !ok || !entry.state.DeliveredAt.IsZero() {
			continue
		}
		entry.state.DeliveredAt = at
	}
}

// runningLocked counts the project's tasks that have not finished yet. Callers
// hold s.mu.
func (s *taskStore) runningLocked(root string) int {
	n := 0
	for _, id := range s.byProject[root] {
		if entry, ok := s.tasks[id]; ok && entry.state.Running {
			n++
		}
	}
	return n
}

// inFlight reports the tasks still running for root, for the dashboard and for
// callers deciding whether to queue another run.
func (s *taskStore) inFlight(root string) []TaskState {
	root = s.projectKey(root)

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

// supersede stops the runs in flight for root that a new run makes pointless,
// and reports how many it stopped.
//
// A run is released because the tree has moved — that is what made a new hook
// fire — so a live-tree run already in flight is validating code that is no
// longer on disk, and its result is going to be discarded the moment it
// finishes. Stopping it frees the validate lock the new run is about to want,
// instead of leaving two runs of the same commands queued behind each other for
// an answer only one of them can give.
//
// Snapshot-backed runs are left alone. Their verdict stays true about the state
// they were handed whatever the tree does afterwards, so that one will be
// reported rather than thrown away — cancelling it would discard the only work
// here that was going to survive.
func (s *taskStore) supersede(root string) int {
	root = s.projectKey(root)

	s.mu.Lock()
	defer s.mu.Unlock()

	ids := s.byProject[root]
	kept := make([]string, 0, len(ids))
	stopped := 0
	for _, id := range ids {
		entry, ok := s.tasks[id]
		if !ok {
			continue
		}
		if !entry.state.Running || entry.state.Snapshot {
			kept = append(kept, id)
			continue
		}
		if entry.cancel != nil {
			entry.cancel()
		}
		// Dropped here rather than left to report a cancellation. finish finds no
		// task and records nothing, which is right: a run that was stopped has
		// concluded nothing, and reporting it as a failure would owe the project a
		// blocking run it never earned.
		delete(s.tasks, id)
		stopped++
	}
	if len(kept) == 0 {
		delete(s.byProject, root)
	} else {
		s.byProject[root] = kept
	}
	return stopped
}

// evictLocked drops tasks until root is under MaxTasksPerProject, taking the
// least useful one each time: an already-delivered result first, then a finished
// one nobody has read. A running task is never evicted — cancelling a run to
// make room would lose work that is about to produce an answer.
//
// The order matters now that delivery no longer deletes. A delivered result is
// being kept only in case its response was lost in flight, while an undelivered
// one is still owed to somebody, so reclaiming the delivered copy first is what
// keeps the retry window from costing a report.
func (s *taskStore) evictLocked(root string) {
	ids := s.byProject[root]
	for len(ids) > MaxTasksPerProject {
		victim := s.oldestLocked(ids, func(e *taskEntry) bool {
			return !e.state.Running && !e.state.DeliveredAt.IsZero()
		})
		if victim == -1 {
			victim = s.oldestLocked(ids, func(e *taskEntry) bool { return !e.state.Running })
		}
		if victim == -1 {
			// Every retained task is still running. Going over the cap is better
			// than dropping a run that has not reported yet.
			break
		}
		delete(s.tasks, ids[victim])
		ids = append(ids[:victim], ids[victim+1:]...)
	}
	s.byProject[root] = ids
}

// oldestLocked returns the index in ids of the first task matching want, or -1.
// ids is oldest-first, so the first match is the oldest one.
func (s *taskStore) oldestLocked(ids []string, want func(*taskEntry) bool) int {
	for i, id := range ids {
		if entry, ok := s.tasks[id]; ok && want(entry) {
			return i
		}
	}
	return -1
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
