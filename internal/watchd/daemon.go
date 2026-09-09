package watchd

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

type projectState struct {
	root string
	// canonRoot is root with symlinks resolved, so a caller that discovered the
	// same project by another route — git's --show-toplevel, a shell's $PWD
	// through a symlinked home — still matches it. Computed once at init,
	// outside the lock, because resolving it touches the filesystem.
	canonRoot string
	dataDir   string
	log       *eventlog.Log
	offset    int64
	events    []eventlog.Event
	snap      ProjectSnapshot

	// conflict and lastFetch are written by the conflict loop and read by the
	// poll loop when it builds snap, so unlike the fields above — which only
	// the poll loop touches — both are guarded by daemon.mu. nil conflict means
	// no check has completed yet.
	conflict  *ConflictState
	lastFetch time.Time
}

type daemon struct {
	mu       sync.RWMutex
	projects map[string]*projectState // keyed by project root

	// client streams command output. Nil when the daemon started without
	// credentials, in which case commands are still recorded but no output is
	// streamed for them.
	client *circleci.Client
	// authError explains an absent client to the user, or "" when there is
	// nothing to explain. Written once at construction and never again, so
	// snapshot reads it without a lock.
	authError string
	// out buffers command output. Streamers run as their own goroutines and
	// never execute on the poll path, so a hung stream cannot stall the
	// dashboard for every other project.
	out *outputStore
}

// RunDaemon is the watch daemon entry point, called by the hidden _daemon subcommand.
//
// The client is resolved by the caller and may be nil: the daemon records
// commands either way, and authMessage is what tells the user why output is
// missing. Resolution belongs to the caller because it can read the OS keychain,
// and the daemon must not hold a lock over that on the command-registration path
// — a hook is waiting on it.
//
// The message arrives already rendered, empty when there is nothing to explain.
// Only the caller that resolved the credentials knows which failures mean "log
// in" and which are something else, and asking the daemon to classify them
// would make this package depend on the auth flow it deliberately sits below.
func RunDaemon(ctx context.Context, client *circleci.Client, authMessage string) error {
	if _, err := EnsureDir(); err != nil {
		return fmt.Errorf("ensure watchd dir: %w", err)
	}

	pidPath, err := PIDPath()
	if err != nil {
		return err
	}
	if err := writePID(pidPath, os.Getpid()); err != nil {
		return fmt.Errorf("write pid: %w", err)
	}
	defer func() { _ = os.Remove(pidPath) }()

	sockPath, err := SocketPath()
	if err != nil {
		return err
	}
	_ = os.Remove(sockPath)

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sockPath, err)
	}
	defer func() { _ = ln.Close() }()
	defer func() { _ = os.Remove(sockPath) }()

	// The signal-aware context is built first because the output store derives
	// every streamer from it: a streamer must not be able to outlive the daemon
	// even if a later return path skips the deferred stopAll below.
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()

	d := &daemon{
		projects:  make(map[string]*projectState),
		client:    client,
		authError: authMessage,
		out:       newOutputStore(ctx),
	}
	// Still cancelled explicitly: this returns before the process exits in tests
	// and any embedded caller, and it is what stops streamers promptly rather
	// than whenever the parent context happens to be torn down.
	defer d.out.stopAll()

	// Poll once before accepting connections so the first request has data.
	d.poll()

	log.Printf("watch daemon started pid=%d socket=%s", os.Getpid(), sockPath)

	go d.pollLoop(ctx)
	go d.checkConflictsLoop(ctx)

	srv := newServer(d)
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	if err := srv.Serve(ln); err != nil && ctx.Err() == nil {
		return fmt.Errorf("watchd serve: %w", err)
	}
	return nil
}

func (d *daemon) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.poll()
		case <-ctx.Done():
			return
		}
	}
}

// poll refreshes state for all registered projects, discovering any new ones
// since the last poll. All I/O (including initProject) runs outside the lock;
// only the final state swap acquires a write lock.
func (d *daemon) poll() {
	roots, _ := sidecar.AllProjectRoots()

	d.mu.RLock()
	work := make([]*projectState, 0, len(roots))
	var newRoots []string
	for _, root := range roots {
		if ps, ok := d.projects[root]; ok {
			work = append(work, ps)
		} else {
			newRoots = append(newRoots, root)
		}
	}
	d.mu.RUnlock()

	// initProject does file I/O; run it outside the lock.
	newProjects := make(map[string]*projectState, len(newRoots))
	for _, root := range newRoots {
		if ps := d.initProject(root); ps != nil {
			newProjects[root] = ps
			work = append(work, ps)
		}
	}

	if len(newProjects) > 0 {
		d.mu.Lock()
		for root, ps := range newProjects {
			if _, exists := d.projects[root]; !exists {
				d.projects[root] = ps
			}
		}
		d.mu.Unlock()
	}

	for _, ps := range work {
		d.updateProject(ps)
	}
}

// initProject opens the event log for root and returns a new projectState.
// Returns nil if the data directory cannot be determined.
func (d *daemon) initProject(root string) *projectState {
	dataDir, err := config.ProjectDataDir(root)
	if err != nil {
		log.Printf("watchd: data dir for %s: %v", root, err)
		return nil
	}
	el, err := eventlog.Open(dataDir)
	if err != nil {
		log.Printf("watchd: event log for %s: %v", root, err)
		return nil
	}
	return &projectState{root: root, canonRoot: canonicalRoot(root), dataDir: dataDir, log: el}
}

// conflictReport answers a conflict query for one project root.
//
// It reads ps.conflict directly rather than the published snapshot: the
// snapshot is only as fresh as the last poll, and this sits in front of a hook
// that is about to tell an agent something. A root the daemon does not know
// yields Known false — distinct from a known project with nothing to report,
// which is the difference between "no answer" and "no conflicts".
func (d *daemon) conflictReport(root string) ConflictReport {
	want := canonicalRoot(root)
	d.mu.RLock()
	defer d.mu.RUnlock()
	for _, ps := range d.projects {
		if ps.root != root && ps.canonRoot != want {
			continue
		}
		return ConflictReport{Root: ps.root, Conflict: ps.conflict, Known: true}
	}
	return ConflictReport{Root: root}
}

// updateProject refreshes sidecar files, new log events, and git state for ps.
// The final snap assignment is protected by the write lock so concurrent
// snapshot reads via d.snapshot() never observe a partially-built value.
func (d *daemon) updateProject(ps *projectState) {
	head := headRef(ps.root)
	branch := currentBranch(ps.root)
	repoName := projectRepoName(ps.root)
	snapName := loadSnapshotName(ps.dataDir)

	if ps.log != nil {
		fresh, newOff, _ := ps.log.TailFrom(ps.offset)
		ps.events = capEvents(ps.events, fresh, RecentEvents)
		ps.offset = newOff
	}

	sidecars := loadSidecars(ps.dataDir, ps.root, snapName)
	annotateActivity(sidecars, ps.events)

	snap := ProjectSnapshot{
		Root:     ps.root,
		Branch:   branch,
		HeadRef:  head,
		RepoName: repoName,
		Sidecars: sidecars,
		Events:   ps.events,
		Commands: d.out.commandsFor(ps.root),
	}
	d.mu.Lock()
	// Read under the same lock that publishes snap, because the conflict loop
	// writes it from another goroutine on its own schedule.
	snap.Conflict = ps.conflict
	ps.snap = snap
	d.mu.Unlock()
}

// snapshot returns a Snapshot filtered to the requested roots.
// If roots is empty all known projects are returned.
func (d *daemon) snapshot(roots []string) Snapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()

	filter := make(map[string]bool, len(roots))
	for _, r := range roots {
		filter[r] = true
	}

	var projects []ProjectSnapshot
	for _, ps := range d.projects {
		if len(filter) > 0 && !filter[ps.root] {
			continue
		}
		projects = append(projects, ps.snap)
	}

	if len(roots) > 0 {
		ordered := make([]ProjectSnapshot, 0, len(roots))
		byRoot := make(map[string]ProjectSnapshot, len(projects))
		for _, p := range projects {
			byRoot[p.Root] = p
		}
		for _, r := range roots {
			if p, ok := byRoot[r]; ok {
				ordered = append(ordered, p)
			}
		}
		return Snapshot{Projects: ordered, AuthError: d.authError}
	}
	// Map iteration is random; sort by root so project rows stay stable
	// between polls when watchAll mode requests all projects.
	sort.Slice(projects, func(i, j int) bool { return projects[i].Root < projects[j].Root })
	return Snapshot{Projects: projects, AuthError: d.authError}
}
