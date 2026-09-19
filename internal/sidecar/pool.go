package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// PoolEntry is a single ready-to-use sidecar checked out from a Pool.
type PoolEntry struct {
	ID       string
	RepoPath string
	Client   *circleci.Client
}

// Pool manages sidecars up to a fixed capacity as a work queue for concurrent tasks.
type Pool struct {
	free          chan *PoolEntry
	updates       chan struct{}
	ids           []string
	entries       []*PoolEntry
	client        *circleci.Client
	workDir       string
	orgID         string
	image         string
	name          string
	repoPath      string
	lastSyncedRef string

	mu             sync.Mutex
	stateMu        sync.Mutex
	pendingSyncs   int
	pendingCreates int
	checkedOut     int
	closed         bool
	syncErr        error
	createCancel   context.CancelFunc
	createDone     chan struct{}
}

type poolState struct {
	SidecarIDs    []string `json:"sidecar_ids"`
	RepoPath      string   `json:"repo_path"`
	Image         string   `json:"image,omitempty"`
	LastSyncedRef string   `json:"last_synced_ref,omitempty"`
}

// PoolOptions describes the resources and persisted identity of a pool.
type PoolOptions struct {
	Size        int
	Name        string
	OrgID       string
	Image       string
	WorkDir     string
	RepoPath    string
	ExistingIDs []string
	FreshIDs    []string
}

func poolStatePath(workDir, name string) string {
	return filepath.Join(workDir, ".chunk", name+"-pool.json")
}

func loadPoolState(workDir, name string) (*poolState, error) {
	data, err := os.ReadFile(poolStatePath(workDir, name))
	if err != nil {
		return nil, err
	}
	var state poolState
	return &state, json.Unmarshal(data, &state)
}

func savePoolState(workDir, name string, state *poolState) error {
	data, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("marshal pool state: %w", err)
	}
	path := poolStatePath(workDir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create pool state directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write pool state: %w", err)
	}
	return nil
}

func clearPoolState(workDir, name string) {
	_ = os.Remove(poolStatePath(workDir, name))
}

func NewPool(
	ctx context.Context,
	client *circleci.Client,
	opts PoolOptions,
	status iostream.StatusFunc,
) (*Pool, error) {
	if opts.Size < 1 {
		return nil, errors.New("pool size must be positive")
	}
	state, _ := loadPoolState(opts.WorkDir, opts.Name)
	repoPath := opts.RepoPath
	if repoPath == "" && state != nil {
		repoPath = state.RepoPath
	}
	if repoPath == "" {
		_, repo, err := gitremote.DetectOrgAndRepo(opts.WorkDir)
		if err != nil {
			return nil, fmt.Errorf("pool: detect repo: %w", err)
		}
		repoPath = DefaultWorkspace(repo)
	}
	image := opts.Image
	if image == "" && state != nil {
		image = state.Image
	}

	existingIDs := append([]string(nil), opts.ExistingIDs...)
	var lastSyncedRef string
	if state != nil && len(state.SidecarIDs) > 0 {
		if len(existingIDs) == 0 {
			existingIDs = append(existingIDs, state.SidecarIDs...)
		}
		if state.RepoPath == repoPath && slices.Equal(existingIDs, state.SidecarIDs[:min(len(existingIDs), len(state.SidecarIDs))]) {
			lastSyncedRef = state.LastSyncedRef
		}
	}
	if len(existingIDs) > opts.Size {
		existingIDs = existingIDs[:opts.Size]
	}

	freshIDs := make(map[string]bool, len(opts.FreshIDs))
	for _, id := range opts.FreshIDs {
		freshIDs[id] = true
	}
	return assemblePool(ctx, client, opts.Size, opts.Name, opts.OrgID, image, repoPath, opts.WorkDir, existingIDs, lastSyncedRef, freshIDs, status)
}

func assemblePool(
	ctx context.Context,
	client *circleci.Client,
	n int,
	name, orgID, image, repoPath, workDir string,
	existingIDs []string,
	lastSyncedRef string,
	freshIDs map[string]bool,
	status iostream.StatusFunc,
) (*Pool, error) {
	aliveExisting := existingIDs[:0:0]
	var staleIDs []string
	staleFlags := make([]bool, len(existingIDs))
	var swg sync.WaitGroup
	for i, id := range existingIDs {
		if freshIDs[id] {
			continue
		}
		swg.Add(1)
		go func(i int, id string) {
			defer swg.Done()
			staleFlags[i] = IsDefinitelyStale(ctx, client, id)
		}(i, id)
	}
	swg.Wait()

	for i, id := range existingIDs {
		if staleFlags[i] {
			staleIDs = append(staleIDs, id)
			continue
		}
		aliveExisting = append(aliveExisting, id)
	}

	cleanCtx := context.Background()
	if len(staleIDs) > 0 {
		var delWg sync.WaitGroup
		for _, id := range staleIDs {
			delWg.Add(1)
			go func(id string) {
				defer delWg.Done()
				status(iostream.LevelInfo, fmt.Sprintf("sidecar %s is stale, creating replacement...", id))
				_ = client.DeleteSidecar(cleanCtx, id)
			}(id)
		}
		delWg.Wait()
	}

	goodNew := make([]string, 0, n-len(aliveExisting))
	newCount := n - len(aliveExisting)
	existingSyncedRef := lastSyncedRef
	var cloneSnapshotID, cloneSeedID string
	if newCount > 0 {
		seedIdx := len(aliveExisting)
		seed, err := Create(ctx, client, orgID, fmt.Sprintf("%s-%d", name, seedIdx), image)
		if err != nil {
			return nil, fmt.Errorf("create sidecar %d: %w", seedIdx, err)
		}
		status(iostream.LevelInfo, fmt.Sprintf("created sidecar %d (%s)", seedIdx, seed.ID))

		headRef, err := bundleSyncFanOutSince(ctx, client, []string{seed.ID}, repoPath, workDir, "", true, status)
		if err != nil {
			_ = client.DeleteSidecar(cleanCtx, seed.ID)
			return nil, fmt.Errorf("pool seed sync: %w", err)
		}
		lastSyncedRef = headRef

		if newCount == 1 {
			goodNew = append(goodNew, seed.ID)
		} else {
			snap, err := client.CreateSnapshot(ctx, seed.ID, fmt.Sprintf("%s-seed-%d", name, time.Now().UTC().UnixNano()))
			if err != nil {
				_ = client.DeleteSidecar(cleanCtx, seed.ID)
				return nil, fmt.Errorf("pool snapshot: %w", err)
			}
			status(iostream.LevelInfo, fmt.Sprintf("created seed snapshot %s", snap.ID))
			cloneSnapshotID = snap.ID
			cloneSeedID = seed.ID
		}
	}

	var prepared *preparedBundleSync
	if len(aliveExisting) > 0 {
		status(iostream.LevelInfo, fmt.Sprintf("syncing to %d sidecars...", len(aliveExisting)))
		var err error
		prepared, err = prepareBundleSync(repoPath, workDir, existingSyncedRef, len(aliveExisting), status)
		if err != nil {
			deleteSidecars(client, goodNew)
			if cloneSeedID != "" {
				_ = client.DeleteSidecar(cleanCtx, cloneSeedID)
			}
			return nil, fmt.Errorf("pool sync: %w", err)
		}
	}
	if cloneSnapshotID == "" && len(goodNew) > 0 && len(staleIDs) > 0 {
		if err := replaceActiveSidecars(context.WithoutCancel(ctx), map[string]string{staleIDs[0]: goodNew[0]}); err != nil {
			deleteSidecars(client, goodNew)
			return nil, fmt.Errorf("update active pool after replacement: %w", err)
		}
		staleIDs = staleIDs[1:]
	}

	allIDs := make([]string, 0, len(aliveExisting)+len(goodNew))
	allIDs = append(allIDs, aliveExisting...)
	allIDs = append(allIDs, goodNew...)
	pendingCreates := 0
	if cloneSnapshotID != "" {
		pendingCreates = newCount
	}

	entries := make([]*PoolEntry, len(allIDs))
	for i, id := range allIDs {
		entries[i] = &PoolEntry{ID: id, RepoPath: repoPath, Client: client}
	}

	free := make(chan *PoolEntry, n)
	for _, entry := range entries[len(aliveExisting):] {
		free <- entry
	}
	pool := &Pool{
		free:           free,
		updates:        make(chan struct{}, 1),
		ids:            allIDs,
		entries:        entries,
		client:         client,
		workDir:        workDir,
		orgID:          orgID,
		image:          image,
		name:           name,
		repoPath:       repoPath,
		lastSyncedRef:  lastSyncedRef,
		pendingSyncs:   len(aliveExisting),
		pendingCreates: pendingCreates,
	}

	if err := pool.persistState(); err != nil {
		deleteSidecars(client, goodNew)
		if cloneSeedID != "" {
			_ = client.DeleteSidecar(cleanCtx, cloneSeedID)
		}
		return nil, fmt.Errorf("pool state: %w", err)
	}
	if cloneSnapshotID != "" {
		pool.startCloneCreation(ctx, cloneSnapshotID, cloneSeedID, newCount, staleIDs, status)
	}
	if prepared != nil {
		pool.startBackgroundSync(ctx, aliveExisting, freshIDs, prepared, status)
	}

	return pool, nil
}

func deleteSidecars(client *circleci.Client, ids []string) {
	ctx := context.Background()
	for _, id := range ids {
		_ = client.DeleteSidecar(ctx, id)
	}
}

func (p *Pool) Rebuild(ctx context.Context, dead *PoolEntry, status iostream.StatusFunc) (*PoolEntry, error) {
	_ = p.client.DeleteSidecar(ctx, dead.ID)

	sc, err := Create(ctx, p.client, p.orgID, fmt.Sprintf("%s-rebuilt-%d", p.name, time.Now().UTC().UnixNano()), p.image)
	if err != nil {
		return nil, fmt.Errorf("rebuild: create sidecar: %w", err)
	}

	if err := BundleSyncFanOut(ctx, p.client, []string{sc.ID}, dead.RepoPath, p.workDir, true, status); err != nil {
		_ = p.client.DeleteSidecar(ctx, sc.ID)
		return nil, fmt.Errorf("rebuild: sync: %w", err)
	}

	return &PoolEntry{ID: sc.ID, RepoPath: dead.RepoPath, Client: p.client}, nil
}

type cloneResult struct {
	id  string
	err error
}

func (p *Pool) startCloneCreation(
	ctx context.Context,
	snapshotID, seedID string,
	count int,
	staleIDs []string,
	status iostream.StatusFunc,
) {
	createCtx, cancel := context.WithCancel(ctx)
	cleanupCtx := context.WithoutCancel(ctx)
	results := make(chan cloneResult, count)
	done := make(chan struct{})
	p.mu.Lock()
	p.createCancel = cancel
	p.createDone = done
	p.mu.Unlock()

	baseIndex := len(p.entries)
	for i := range count {
		go func(i int) {
			index := baseIndex + i
			sc, err := Create(createCtx, p.client, p.orgID, fmt.Sprintf("%s-%d", p.name, index), snapshotID)
			if err != nil {
				results <- cloneResult{err: fmt.Errorf("create sidecar %d from snapshot: %w", index, err)}
				return
			}
			status(iostream.LevelInfo, fmt.Sprintf("created sidecar %d (%s)", index, sc.ID))
			results <- cloneResult{id: sc.ID}
		}(i)
	}

	go func() {
		defer close(done)
		defer cancel()
		defer func() { _ = p.client.DeleteSidecar(cleanupCtx, seedID) }()
		replacementIndex := 0
		for range count {
			result := <-results
			if result.err != nil {
				status(iostream.LevelWarn, result.err.Error())
				p.finishCreate(nil, result.err, status)
				continue
			}

			if replacementIndex < len(staleIDs) {
				staleID := staleIDs[replacementIndex]
				if err := replaceActiveSidecars(context.WithoutCancel(ctx), map[string]string{staleID: result.id}); err != nil {
					_ = p.client.DeleteSidecar(cleanupCtx, result.id)
					err = fmt.Errorf("replace active sidecar %s: %w", staleID, err)
					status(iostream.LevelWarn, err.Error())
					p.finishCreate(nil, err, status)
					continue
				}
				replacementIndex++
				status(iostream.LevelInfo, fmt.Sprintf("replacement sidecar: %s", result.id))
			}

			p.finishCreate(&PoolEntry{ID: result.id, RepoPath: p.repoPath, Client: p.client}, nil, status)
		}

		for _, staleID := range staleIDs[replacementIndex:] {
			if _, err := RemoveActiveSidecar(context.WithoutCancel(ctx), staleID); err != nil {
				status(iostream.LevelWarn, fmt.Sprintf("could not remove failed sidecar %s from active pool: %v", staleID, err))
			}
		}
	}()
}

func (p *Pool) finishCreate(entry *PoolEntry, err error, status iostream.StatusFunc) {
	p.mu.Lock()
	if p.pendingCreates > 0 {
		p.pendingCreates--
	}
	if err != nil {
		p.syncErr = errors.Join(p.syncErr, err)
	}
	closed := p.closed
	if entry != nil && !closed {
		p.ids = append(p.ids, entry.ID)
		p.entries = append(p.entries, entry)
		p.free <- entry
	}
	p.mu.Unlock()

	if entry != nil && closed {
		_ = p.client.DeleteSidecar(context.Background(), entry.ID)
	} else if entry != nil {
		if err := p.persistState(); err != nil {
			status(iostream.LevelWarn, fmt.Sprintf("could not save pool state: %v", err))
		}
	}
	p.notifyUpdate()
}

func (p *Pool) persistState() error {
	p.stateMu.Lock()
	defer p.stateMu.Unlock()

	p.mu.Lock()
	state := &poolState{
		SidecarIDs:    slices.Clone(p.ids),
		RepoPath:      p.repoPath,
		Image:         p.image,
		LastSyncedRef: p.lastSyncedRef,
	}
	p.mu.Unlock()

	stored, err := loadPoolState(p.workDir, p.name)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if stored != nil {
		if state.RepoPath == "" {
			state.RepoPath = stored.RepoPath
		}
		if state.Image == "" {
			state.Image = stored.Image
		}
		if state.LastSyncedRef == "" {
			state.LastSyncedRef = stored.LastSyncedRef
		}
	}
	return savePoolState(p.workDir, p.name, state)
}

func (p *Pool) Acquire(ctx context.Context) (*PoolEntry, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, errors.New("pool is destroyed")
		}
		if p.pendingSyncs == 0 && p.pendingCreates == 0 && len(p.free) == 0 && p.checkedOut == 0 && p.syncErr != nil {
			err := p.syncErr
			p.mu.Unlock()
			return nil, err
		}
		select {
		case entry := <-p.free:
			p.checkedOut++
			p.mu.Unlock()
			return entry, nil
		default:
			p.mu.Unlock()
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-p.updates:
		}
	}
}

func (p *Pool) Release(entry *PoolEntry) {
	p.mu.Lock()
	if p.checkedOut > 0 {
		p.checkedOut--
	}
	closed := p.closed
	if !closed {
		p.free <- entry
	}
	p.mu.Unlock()
	if closed {
		_ = p.client.DeleteSidecar(context.Background(), entry.ID)
	}
	p.notifyUpdate()
}

func (p *Pool) Close(ctx context.Context) {
	p.mu.Lock()
	cancel := p.createCancel
	done := p.createDone
	p.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func (p *Pool) Destroy(ctx context.Context) {
	p.mu.Lock()
	p.closed = true
	ids := slices.Clone(p.ids)
	cancel := p.createCancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	clearPoolState(p.workDir, p.name)
	for _, id := range ids {
		_ = p.client.DeleteSidecar(ctx, id)
	}
	p.notifyUpdate()
}

func (p *Pool) startBackgroundSync(ctx context.Context, sidecarIDs []string, freshIDs map[string]bool, prepared *preparedBundleSync, status iostream.StatusFunc) {
	parallelism := len(sidecarIDs)
	if parallelism > bundleSyncFanOutConcurrency {
		parallelism = bundleSyncFanOutConcurrency
	}

	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for i, id := range sidecarIDs {
		entry := p.entries[i]
		wg.Add(1)
		go func(i int, id string, entry *PoolEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			err := syncPreparedSidecar(ctx, p.client, id, freshIDs[id], prepared)
			if isStaleSyncError(err) {
				entry, err = p.replaceStaleEntry(ctx, entry, prepared, status)
				if err == nil {
					p.mu.Lock()
					p.entries[i] = entry
					p.ids[i] = entry.ID
					p.mu.Unlock()
				}
			}
			p.finishSync(entry, err)
		}(i, id, entry)
	}

	go func() {
		wg.Wait()
		p.mu.Lock()
		doneWithoutError := p.pendingSyncs == 0 && p.syncErr == nil && !p.closed
		if doneWithoutError {
			p.lastSyncedRef = prepared.headRef
		}
		p.mu.Unlock()
		if doneWithoutError {
			if err := p.persistState(); err != nil {
				status(iostream.LevelWarn, fmt.Sprintf("could not save pool state: %v", err))
			}
			status(iostream.LevelDone, fmt.Sprintf("Synced %d sidecars", len(sidecarIDs)))
		}
		p.notifyUpdate()
	}()
}

func isStaleSyncError(err error) bool {
	return err != nil && (circleci.SidecarGone(err) || circleci.SidecarOutOfDate(err))
}

func (p *Pool) replaceStaleEntry(ctx context.Context, stale *PoolEntry, prepared *preparedBundleSync, status iostream.StatusFunc) (*PoolEntry, error) {
	status(iostream.LevelInfo, fmt.Sprintf("sidecar %s became stale during sync, creating replacement...", stale.ID))
	_ = p.client.DeleteSidecar(context.Background(), stale.ID)

	sc, err := Create(ctx, p.client, p.orgID, fmt.Sprintf("%s-rebuilt-%d", p.name, time.Now().UTC().UnixNano()), p.image)
	if err != nil {
		return stale, fmt.Errorf("replace stale sidecar: create: %w", err)
	}
	replacement := &PoolEntry{ID: sc.ID, RepoPath: stale.RepoPath, Client: p.client}
	if err := syncPreparedSidecar(ctx, p.client, replacement.ID, true, prepared); err != nil {
		_ = p.client.DeleteSidecar(context.Background(), replacement.ID)
		return stale, fmt.Errorf("replace stale sidecar: sync: %w", err)
	}
	if err := replaceActiveSidecars(context.WithoutCancel(ctx), map[string]string{stale.ID: replacement.ID}); err != nil {
		_ = p.client.DeleteSidecar(context.Background(), replacement.ID)
		return stale, fmt.Errorf("replace stale sidecar: update active state: %w", err)
	}
	status(iostream.LevelInfo, fmt.Sprintf("replacement sidecar: %s", replacement.ID))
	return replacement, nil
}

func (p *Pool) finishSync(entry *PoolEntry, err error) {
	p.mu.Lock()
	if err != nil {
		p.syncErr = errors.Join(p.syncErr, fmt.Errorf("sidecar %s: %w", entry.ID, err))
	} else if !p.closed {
		p.free <- entry
	}
	closed := p.closed
	if p.pendingSyncs > 0 {
		p.pendingSyncs--
	}
	p.mu.Unlock()
	if err == nil && closed {
		_ = p.client.DeleteSidecar(context.Background(), entry.ID)
	}
	p.notifyUpdate()
}

func (p *Pool) notifyUpdate() {
	select {
	case p.updates <- struct{}{}:
	default:
	}
}
