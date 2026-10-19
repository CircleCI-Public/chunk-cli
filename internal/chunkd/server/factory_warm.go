package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// warmTTL is how long sidecars made ahead of a run wait for it before they
// are deleted: long enough to read a draft and send it, short enough that
// work never handed over does not keep sidecars for long.
const warmTTL = 15 * time.Minute

// warmMaker makes the sidecars for one run ahead of it. Swapped out in tests.
type warmMaker func(ctx context.Context) (*factory.WarmPool, error)

// warmEntry is the sidecars being made, or made, ahead of one run.
type warmEntry struct {
	done   chan struct{}
	pool   *factory.WarmPool
	err    error
	cancel context.CancelFunc
	timer  *time.Timer
}

// wait returns the sidecars once they are made, or nil when they could not
// be or ctx ended first.
func (e *warmEntry) wait(ctx context.Context) *factory.WarmPool {
	select {
	case <-e.done:
		return e.pool
	case <-ctx.Done():
		return nil
	}
}

// warmStore keeps the sidecars made ahead of runs, by the run each is for.
type warmStore struct {
	ctx   context.Context
	mu    sync.Mutex
	byRun map[string]*warmEntry
	wg    sync.WaitGroup
	ttl   time.Duration
}

func newWarmStore(ctx context.Context) *warmStore {
	return &warmStore{ctx: ctx, byRun: map[string]*warmEntry{}, ttl: warmTTL}
}

// start makes the sidecars for runID with build, unless they are already
// being made. Those no run takes within the store's ttl are deleted.
func (s *warmStore) start(runID string, build warmMaker) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byRun[runID]; ok {
		return
	}
	// The pool's members are cloned in the background on this context after
	// make returns, so it lasts until the sidecars are discarded or the run
	// that took them ends, not just until they are made.
	ctx, cancel := context.WithCancel(s.ctx)
	e := &warmEntry{done: make(chan struct{}), cancel: cancel}
	s.byRun[runID] = e
	e.timer = time.AfterFunc(s.ttl, func() { s.discard(runID) })
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		e.pool, e.err = build(ctx)
		close(e.done)
		if e.err != nil {
			log.Printf("chunkd: sidecars ahead of run %s: %v", runID, e.err)
			s.take(runID)
			cancel()
		}
	}()
}

// take removes runID's sidecars from the store for a run to use, or returns
// nil when none are being made for it.
func (s *warmStore) take(runID string) *warmEntry {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := s.byRun[runID]
	if e == nil {
		return nil
	}
	delete(s.byRun, runID)
	e.timer.Stop()
	return e
}

// discard deletes runID's sidecars, stopping them being made if they still
// are.
func (s *warmStore) discard(runID string) {
	e := s.take(runID)
	if e == nil {
		return
	}
	e.cancel()
	<-e.done
	if e.pool != nil {
		if err := e.pool.Discard(context.Background()); err != nil {
			log.Printf("chunkd: delete the sidecars made ahead of run %q: %v", runID, err) //nolint:gosec // %q escapes control characters, preventing log injection
		}
	}
}

// stopAll deletes every warm pool no run took. Called on daemon shutdown.
func (s *warmStore) stopAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	runs := make([]string, 0, len(s.byRun))
	for id := range s.byRun {
		runs = append(runs, id)
	}
	s.mu.Unlock()
	for _, id := range runs {
		s.discard(id)
	}
	s.wg.Wait()
}

// warmFactory starts making the sidecars a run continuing req.RunID would
// use. A request it cannot act on is refused, but the run still works without
// them, so callers need not wait or report it.
func (d *daemon) warmFactory(req chunkd.WarmRequest) error {
	if msg := d.factoryAuthError(); msg != "" {
		return apiErr(http.StatusServiceUnavailable, "%s", msg)
	}
	if d.client == nil {
		return apiErr(http.StatusServiceUnavailable, "not authenticated to CircleCI, so no sidecars can be made ahead of a run")
	}
	if req.RunID == "" {
		return apiErr(http.StatusBadRequest, "run_id required")
	}
	ps := d.lookupProject(req.ProjectRoot)
	if ps == nil {
		ps = d.adoptProject(req.ProjectRoot)
	}
	if ps == nil {
		return apiErr(http.StatusNotFound, "the chunk daemon is not tracking %q, and it is not the top of a git repository", req.ProjectRoot)
	}
	rec, err := factory.LoadRecord(ps.root, factory.ParseRunID(req.RunID))
	if errors.Is(err, factory.ErrNoRecord) {
		return apiErr(http.StatusNotFound, "%v", err)
	}
	if err != nil {
		return apiErr(http.StatusBadRequest, "%v", err)
	}
	// Made as a run with the request's defaults would make them, so a run
	// started with its defaults can use them.
	cfg, err := loadProjectConfig(ps.root)
	if err != nil {
		return apiErr(http.StatusBadRequest, "%v", err)
	}
	dir, err := sessionPromptsDir(ps.root, "")
	if err != nil {
		return apiErr(http.StatusBadRequest, "%v", err)
	}
	prompts, commands, err := factory.LoadChecks(dir, true, cfg, false)
	if err != nil {
		return apiErr(http.StatusBadRequest, "%v", err)
	}
	orgID, image, err := poolTarget(ps.root, cfg)
	if err != nil {
		return apiErr(http.StatusBadRequest, "%v", err)
	}
	reviewers := factory.ReviewerCount(0, prompts)
	root := ps.root
	d.warm.start(rec.RunID, func(ctx context.Context) (*factory.WarmPool, error) {
		status := func(_ iostream.Level, msg string) {
			log.Printf("chunkd: sidecars ahead of run %s: %s", rec.RunID, msg)
		}
		w, err := factory.NewWarmPool(ctx, d.client, root, rec, reviewers, orgID, image, status)
		if err != nil {
			return nil, err
		}
		// The run's checks start cold on new sidecars: build and lint caches
		// empty, dependencies uninstalled. Running them once now, while the
		// work is drafted, leaves the implementer's sidecar warm. Their output
		// is bookkeeping, not worth a log pane.
		exec := d.execerFor(root, func(string, string) string { return "" })
		if err := w.WarmUp(ctx, exec, commands, status); err != nil {
			status(iostream.LevelWarn, fmt.Sprintf("the implementer's sidecar was not warmed up: %v", err))
		}
		return w, nil
	})
	return nil
}

// claimWarm hands a continued run the sidecars made ahead of it, once they are
// made, when they suit it. It returns a function the run calls as it ends,
// which deletes them if the run did not use them.
func (d *daemon) claimWarm(ctx context.Context, e *warmEntry, opts *factory.RunOptions, status iostream.StatusFunc) func() {
	if e == nil {
		return func() {}
	}
	pool := e.wait(ctx)
	if pool != nil && !pool.Fits(opts.Reviewers, opts.OrgID, opts.Image) {
		status(iostream.LevelInfo, "The sidecars made ahead of this run do not suit it, so it makes its own")
		_ = pool.Discard(context.Background())
		pool = nil
	}
	opts.Warm = pool
	return func() {
		if pool != nil && !pool.Claimed() {
			_ = pool.Discard(context.Background())
		}
		e.cancel()
	}
}

func (d *daemon) handleFactoryWarm(w http.ResponseWriter, r *http.Request) {
	var req chunkd.WarmRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBytes)).Decode(&req); err != nil {
		http.Error(w, "decode request: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.ProjectRoot == "" {
		http.Error(w, "project_root required", http.StatusBadRequest)
		return
	}
	if err := d.warmFactory(req); err != nil {
		writeAPIError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (d *daemon) handleFactoryWarmCancel(w http.ResponseWriter, r *http.Request) {
	go d.warm.discard(factory.ParseRunID(r.PathValue("run")))
	w.WriteHeader(http.StatusAccepted)
}
