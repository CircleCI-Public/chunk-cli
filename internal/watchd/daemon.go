package watchd

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
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
}

// RunDaemon is the watch daemon entry point, invoked by the hidden _daemon subcommand.
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

	d := &daemon{projects: make(map[string]*projectState)}

	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, os.Interrupt)
	defer stop()

	// Poll once before accepting connections so the first TUI request has data.
	d.poll()

	log.Printf("watch daemon started pid=%d socket=%s", os.Getpid(), sockPath)

	go d.pollLoop(ctx)

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			log.Printf("watchd accept: %v", err)
			continue
		}
		go d.handleConn(conn)
	}
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

// poll refreshes state for all currently registered projects, discovering any
// new ones that registered since the last poll.
func (d *daemon) poll() {
	roots, _ := sidecar.AllProjectRoots()

	d.mu.Lock()
	defer d.mu.Unlock()

	for _, root := range roots {
		ps, ok := d.projects[root]
		if !ok {
			ps = d.initProject(root)
			if ps == nil {
				continue
			}
			d.projects[root] = ps
		}
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
	return &projectState{root: root, dataDir: dataDir, log: el}
}

// updateProject re-reads sidecar files, new log events, and git state for ps.
func (d *daemon) updateProject(ps *projectState) {
	headRef := HeadRef(ps.root)
	branch := CurrentBranch(ps.root)

	// Read new events incrementally.
	if ps.log != nil {
		fresh, newOff, _ := ps.log.TailFrom(ps.offset)
		ps.events = CapEvents(ps.events, fresh, RecentEvents)
		ps.offset = newOff
	}

	snapName := LoadSnapshotName(ps.dataDir)
	sidecars := LoadSidecars(ps.dataDir, ps.root, snapName, headRef)
	AnnotateActivity(sidecars, ps.events)
	SortByActivity(sidecars)
	sidecars = FilterSidecars(sidecars)

	ps.snap = ProjectSnapshot{
		Root:     ps.root,
		Branch:   branch,
		HeadRef:  headRef,
		Sidecars: sidecars,
		Events:   ps.events,
	}
}

func (d *daemon) handleConn(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	req, err := receiveRequest(conn)
	if err != nil {
		log.Printf("watchd receive: %v", err)
		return
	}

	resp := d.dispatch(req)
	if err := sendResponse(conn, resp); err != nil {
		log.Printf("watchd send response: %v", err)
	}
}

func (d *daemon) dispatch(req wireRequest) wireResponse {
	switch req.Cmd {
	case cmdPing:
		return wireResponse{OK: true}

	case cmdSnapshot:
		snap := d.snapshot(req.Roots)
		return wireResponse{OK: true, Snapshot: &snap}

	default:
		return wireResponse{OK: false, Error: fmt.Sprintf("unknown command %q", req.Cmd)}
	}
}

// snapshot builds a Snapshot filtered to the requested roots. If roots is
// empty, all known projects are returned.
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

	// Return projects in the order the caller requested, when a filter was given.
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
		return Snapshot{Projects: ordered}
	}
	return Snapshot{Projects: projects}
}
