package watchd

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"syscall"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
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
	// org and repo are resolved once from the git remote and cached for PR monitoring.
	org  string
	repo string
}

type daemon struct {
	mu         sync.RWMutex
	projects   map[string]*projectState // keyed by project root
	validateMu sync.Mutex               // serializes concurrent /validate requests
	runner     ValidateRunner

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
	// res samples resource usage, only while a dashboard is attached.
	res *resourceSampler
	// tasks tracks validation runs started asynchronously, whose callers have
	// already been released and so are not waiting for the answer.
	tasks *taskStore
	// risk remembers which projects owe a blocking run, and is consulted before
	// releasing a caller.
	risk *riskMemory
	// hist is what past runs say about each project, for the questions an
	// absolute threshold cannot answer.
	hist *riskHistory
	// prm monitors open PRs for each project's current branch. Nil when no
	// GitHub credentials are available.
	prm *prMonitor
}

// RunDaemon is the watch daemon entry point, called by the hidden _daemon subcommand.
//
// client and authMessage support the output-buffering feature; runner is called
// in-process to handle /validate requests. Both client and runner may be nil
// (the daemon still records commands without a client, and /validate returns an
// error without a runner). ghClient may be nil; PR monitoring is skipped when
// no GitHub credentials are available.
func RunDaemon(ctx context.Context, client *circleci.Client, authMessage string, runner ValidateRunner, ghClient *github.Client) error {
	var tcpLn net.Listener
	if addr := TCPListenAddr(); addr != "" {
		if TCPToken() == "" {
			return fmt.Errorf("CHUNK_WATCHD_TCP_ADDR requires CHUNK_WATCHD_TCP_TOKEN to be set")
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen tcp on %s: %w", addr, err)
		}
		tcpLn = ln
	}
	return runDaemon(ctx, tcpLn, client, authMessage, runner, ghClient)
}

// runDaemon is the inner daemon loop. It accepts a pre-opened tcpLn (nil when
// TCP is disabled) so tests can avoid the TOCTOU race of closing and re-opening
// a listener to discover a free port.
func runDaemon(ctx context.Context, tcpLn net.Listener, client *circleci.Client, authMessage string, runner ValidateRunner, ghClient *github.Client) error {
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
		runner:    runner,
		client:    client,
		authError: authMessage,
		out:       newOutputStore(ctx),
		res:       newResourceSampler(client),
		tasks:     newTaskStore(ctx),
		risk:      newRiskMemory(),
		hist:      newRiskHistory(),
		prm:       newPRMonitor(ghClient),
	}
	// A background run is the one run with nobody to report a failure to, so what
	// it concluded is remembered here and blocks the run after it.
	d.tasks.onFinish = d.risk.record
	// Still cancelled explicitly: this returns before the process exits in tests
	// and any embedded caller, and it is what stops streamers promptly rather
	// than whenever the parent context happens to be torn down.
	defer d.out.stopAll()
	defer d.res.stopAll()
	defer d.tasks.stopAll()

	// Poll once before accepting connections so the first request has data.
	d.poll()

	go d.pollLoop(ctx)
	go d.checkConflictsLoop(ctx)

	srv := newServer(d)
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()

	if tcpLn != nil {
		log.Printf("watch daemon started pid=%d socket=%s tcp=%s", os.Getpid(), sockPath, tcpLn.Addr())
		// The TCP server wraps the same handler with bearer-token auth so the
		// Unix socket (bound to the local user's filesystem) stays unauthenticated
		// while the TCP listener enforces a shared secret.
		tcpSrv := &http.Server{
			Handler:           withBearerAuth(srv.Handler, TCPToken()),
			ReadHeaderTimeout: 5 * time.Second,
		}
		go func() {
			<-ctx.Done()
			_ = tcpSrv.Close()
		}()
		go func() {
			if serveErr := tcpSrv.Serve(tcpLn); serveErr != nil && ctx.Err() == nil {
				log.Printf("watchd tcp: %v", serveErr)
			}
		}()
	} else {
		log.Printf("watch daemon started pid=%d socket=%s", os.Getpid(), sockPath)
	}

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
	ps := &projectState{root: root, canonRoot: canonicalRoot(root), dataDir: dataDir, log: el}
	// Resolve org/repo once for PR monitoring. Failure is silently ignored:
	// the project may not be on GitHub, or the remote may not be reachable.
	if org, repo, err := gitremote.DetectOrgAndRepo(root); err == nil {
		ps.org = org
		ps.repo = repo
	}
	return ps
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
	d.res.annotate(sidecars)

	// Kick off a background PR fetch if one is due. Uses a background context
	// derived from the daemon's own: the fetch must survive this poll returning.
	d.prm.maybeRefresh(context.Background(), ps.root, branch, ps.org, ps.repo)

	snap := ProjectSnapshot{
		Root:     ps.root,
		Branch:   branch,
		HeadRef:  head,
		RepoName: repoName,
		Sidecars: sidecars,
		Events:   ps.events,
		Commands: d.out.commandsFor(ps.root),
	}
	d.prm.annotate(&snap)

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
	// A snapshot request is the daemon's signal that a dashboard is attached, and
	// resource sampling is gated on that: a persistent SSH connection per sidecar
	// is only worth holding while someone is looking at it.
	d.res.touch()

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
