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

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

type projectState struct {
	root    string
	dataDir string
	log     *eventlog.Log
	offset  int64
	events  []eventlog.Event
	snap    ProjectSnapshot
}

type daemon struct {
	mu       sync.RWMutex
	projects map[string]*projectState // keyed by project root

	// creds resolves the CircleCI client lazily; nil-safe to use when
	// unauthenticated, in which case output streaming is simply unavailable.
	creds *credentials
	// out buffers command output. Streamers run as their own goroutines and
	// never execute on the poll path, so a hung stream cannot stall the
	// dashboard for every other project.
	out *outputStore
	// res samples resource usage, only while a dashboard is attached.
	res *resourceSampler
}

// RunDaemon is the watch daemon entry point, called by the hidden _daemon subcommand.
func RunDaemon(ctx context.Context) error {
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

	creds := &credentials{}
	d := &daemon{
		projects: make(map[string]*projectState),
		creds:    creds,
		out:      newOutputStore(ctx),
		res:      newResourceSampler(creds),
	}
	// Still cancelled explicitly: this returns before the process exits in tests
	// and any embedded caller, and it is what stops streamers promptly rather
	// than whenever the parent context happens to be torn down.
	defer d.out.stopAll()
	defer d.res.stopAll()

	// Poll once before accepting connections so the first request has data.
	d.poll()

	log.Printf("watch daemon started pid=%d socket=%s", os.Getpid(), sockPath)

	go d.pollLoop(ctx)

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

	// Reconcile samplers once per poll, across every project. Doing it inside
	// updateProject would hand reconcile one project's sidecars at a time, and it
	// stops any sampler absent from what it is given — so each project's turn
	// would tear down every other project's samplers.
	d.res.reconcile(d.allSidecars())
}

// allSidecars returns every known sidecar across all projects.
func (d *daemon) allSidecars() []SidecarState {
	d.mu.RLock()
	defer d.mu.RUnlock()
	var all []SidecarState
	for _, ps := range d.projects {
		all = append(all, ps.snap.Sidecars...)
	}
	return all
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
	return &projectState{root: root, dataDir: dataDir, log: el}
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
	d.res.annotate(sidecars)

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
	ps.snap = snap
	d.mu.Unlock()
}

// snapshot returns a Snapshot filtered to the requested roots.
// If roots is empty all known projects are returned.
func (d *daemon) snapshot(roots []string) Snapshot {
	// A snapshot request is the daemon's signal that a dashboard is attached, and
	// resource sampling is gated on that: a persistent SSH connection per sidecar
	// is only worth holding while someone is looking at it.
	d.res.touch()

	// Read outside the project lock: message only reports already-resolved state,
	// but it takes the credentials mutex, and nesting two locks here to report a
	// string is not worth the ordering it would impose.
	authErr := d.creds.message()

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
		return Snapshot{Projects: ordered, AuthError: authErr}
	}
	// Map iteration is random; sort by root so project rows stay stable
	// between polls when watchAll mode requests all projects.
	sort.Slice(projects, func(i, j int) bool { return projects[i].Root < projects[j].Root })
	return Snapshot{Projects: projects, AuthError: authErr}
}
