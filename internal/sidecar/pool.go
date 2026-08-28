package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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

// Pool manages a fixed set of sidecars as a work queue for parallel tasks.
type Pool struct {
	free         chan *PoolEntry
	updates      chan struct{}
	ids          []string
	entries      []*PoolEntry
	client       *circleci.Client
	workDir      string
	orgID        string
	image        string
	name         string
	identityFile string
	authSock     string

	mu           sync.Mutex
	pendingSyncs int
	checkedOut   int
	syncErr      error
}

type poolState struct {
	SidecarIDs    []string `json:"sidecar_ids"`
	RepoPath      string   `json:"repo_path"`
	LastSyncedRef string   `json:"last_synced_ref,omitempty"`
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

func savePoolState(workDir, name string, state *poolState) {
	data, err := json.Marshal(state)
	if err != nil {
		return
	}
	_ = os.WriteFile(poolStatePath(workDir, name), data, 0o600)
}

func clearPoolState(workDir, name string) {
	_ = os.Remove(poolStatePath(workDir, name))
}

func NewPool(
	ctx context.Context,
	client *circleci.Client,
	n int,
	name, orgID, image, identityFile, authSock, workDir string,
	status iostream.StatusFunc,
) (*Pool, error) {
	_, repo, err := gitremote.DetectOrgAndRepo(workDir)
	if err != nil {
		return nil, fmt.Errorf("pool: detect repo: %w", err)
	}
	repoPath := DefaultWorkspace(repo)

	var existingIDs []string
	var lastSyncedRef string
	if state, err := loadPoolState(workDir, name); err == nil && len(state.SidecarIDs) > 0 {
		reuse := state.SidecarIDs
		if len(reuse) > n {
			reuse = reuse[:n]
		}
		existingIDs = reuse
		lastSyncedRef = state.LastSyncedRef
	}

	return assemblePool(ctx, client, n, name, orgID, image, identityFile, authSock, repoPath, workDir, existingIDs, lastSyncedRef, status)
}

func assemblePool(
	ctx context.Context,
	client *circleci.Client,
	n int,
	name, orgID, image, identityFile, authSock, repoPath, workDir string,
	existingIDs []string,
	lastSyncedRef string,
	status iostream.StatusFunc,
) (*Pool, error) {
	aliveExisting := existingIDs[:0:0]
	if len(existingIDs) > 0 {
		staleFlags := make([]bool, len(existingIDs))
		var swg sync.WaitGroup
		for i, id := range existingIDs {
			swg.Add(1)
			go func(i int, id string) {
				defer swg.Done()
				staleFlags[i] = IsDefinitelyStale(ctx, client, id, identityFile, authSock)
			}(i, id)
		}
		swg.Wait()

		var staleIDs []string
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
	}

	goodNew := make([]string, 0, n-len(aliveExisting))
	newCount := n - len(aliveExisting)
	if newCount > 0 {
		seedIdx := len(aliveExisting)
		seed, err := Create(ctx, client, orgID, fmt.Sprintf("%s-%d", name, seedIdx), image)
		if err != nil {
			return nil, fmt.Errorf("create sidecar %d: %w", seedIdx, err)
		}
		status(iostream.LevelInfo, fmt.Sprintf("created sidecar %d (%s)", seedIdx, seed.ID))

		headRef, err := bundleSyncFanOutSince(ctx, client, []string{seed.ID}, identityFile, authSock, repoPath, workDir, "", true, status)
		if err != nil {
			cleanCtx := context.Background()
			_ = client.DeleteSidecar(cleanCtx, seed.ID)
			return nil, fmt.Errorf("pool seed sync: %w", err)
		}
		lastSyncedRef = headRef

		if newCount == 1 {
			goodNew = append(goodNew, seed.ID)
		} else {
			snap, err := client.CreateSnapshot(ctx, seed.ID, fmt.Sprintf("%s-seed-%d", name, time.Now().UTC().UnixNano()))
			if err != nil {
				cleanCtx := context.Background()
				_ = client.DeleteSidecar(cleanCtx, seed.ID)
				return nil, fmt.Errorf("pool snapshot: %w", err)
			}
			status(iostream.LevelInfo, fmt.Sprintf("created seed snapshot %s", snap.ID))

			cloneIDs := make([]string, newCount)
			cloneErrs := make([]error, newCount)
			var wg sync.WaitGroup
			for i := range cloneIDs {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					idx := len(aliveExisting) + i
					sc, err := Create(ctx, client, orgID, fmt.Sprintf("%s-%d", name, idx), snap.ID)
					if err != nil {
						cloneErrs[i] = fmt.Errorf("create sidecar %d from snapshot: %w", idx, err)
						return
					}
					cloneIDs[i] = sc.ID
					status(iostream.LevelInfo, fmt.Sprintf("created sidecar %d (%s)", idx, sc.ID))
				}(i)
			}
			wg.Wait()

			var errl []error
			for i, err := range cloneErrs {
				if err != nil {
					errl = append(errl, err)
					continue
				}
				if cloneIDs[i] != "" {
					goodNew = append(goodNew, cloneIDs[i])
					status(iostream.LevelInfo, fmt.Sprintf("replacement sidecar: %s", cloneIDs[i]))
				}
			}

			cleanCtx := context.Background()
			_ = client.DeleteSidecar(cleanCtx, seed.ID)
			if len(errl) > 0 {
				for _, id := range goodNew {
					_ = client.DeleteSidecar(cleanCtx, id)
				}
				return nil, errors.Join(errl...)
			}
		}
	}

	allIDs := make([]string, 0, len(aliveExisting)+len(goodNew))
	allIDs = append(allIDs, aliveExisting...)
	allIDs = append(allIDs, goodNew...)

	entries := make([]*PoolEntry, len(allIDs))
	for i, id := range allIDs {
		entries[i] = &PoolEntry{ID: id, RepoPath: repoPath, Client: client}
	}

	free := make(chan *PoolEntry, len(entries))
	for _, entry := range entries[len(aliveExisting):] {
		free <- entry
	}
	pool := &Pool{
		free:         free,
		updates:      make(chan struct{}, 1),
		ids:          allIDs,
		entries:      entries,
		client:       client,
		workDir:      workDir,
		orgID:        orgID,
		image:        image,
		name:         name,
		identityFile: identityFile,
		authSock:     authSock,
		pendingSyncs: len(aliveExisting),
	}

	if len(aliveExisting) == 0 {
		savePoolState(workDir, name, &poolState{SidecarIDs: allIDs, RepoPath: repoPath, LastSyncedRef: lastSyncedRef})
		return pool, nil
	}

	status(iostream.LevelInfo, fmt.Sprintf("syncing to %d sidecars...", len(aliveExisting)))
	prepared, err := prepareBundleSync(repoPath, workDir, lastSyncedRef, len(aliveExisting), status)
	if err != nil {
		cleanCtx := context.Background()
		for _, id := range goodNew {
			_ = client.DeleteSidecar(cleanCtx, id)
		}
		return nil, fmt.Errorf("pool sync: %w", err)
	}
	pool.startBackgroundSync(ctx, aliveExisting, prepared, status)
	savePoolState(workDir, name, &poolState{SidecarIDs: allIDs, RepoPath: repoPath, LastSyncedRef: lastSyncedRef})

	return pool, nil
}

func (p *Pool) Rebuild(ctx context.Context, dead *PoolEntry, status iostream.StatusFunc) (*PoolEntry, error) {
	_ = p.client.DeleteSidecar(ctx, dead.ID)

	sc, err := Create(ctx, p.client, p.orgID, fmt.Sprintf("%s-rebuilt", p.name), p.image)
	if err != nil {
		return nil, fmt.Errorf("rebuild: create sidecar: %w", err)
	}

	if err := BundleSyncFanOut(ctx, p.client, []string{sc.ID}, p.identityFile, p.authSock, dead.RepoPath, p.workDir, true, status); err != nil {
		_ = p.client.DeleteSidecar(ctx, sc.ID)
		return nil, fmt.Errorf("rebuild: sync: %w", err)
	}

	return &PoolEntry{ID: sc.ID, RepoPath: dead.RepoPath, Client: p.client}, nil
}

func (p *Pool) Acquire(ctx context.Context) (*PoolEntry, error) {
	for {
		p.mu.Lock()
		if p.pendingSyncs == 0 && len(p.free) == 0 && p.checkedOut == 0 && p.syncErr != nil {
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
	p.free <- entry
	p.mu.Unlock()
	p.notifyUpdate()
}

func (p *Pool) Close(_ context.Context) {}

func (p *Pool) Destroy(ctx context.Context) {
	clearPoolState(p.workDir, p.name)
	for _, id := range p.ids {
		_ = p.client.DeleteSidecar(ctx, id)
	}
}

func (p *Pool) startBackgroundSync(ctx context.Context, sidecarIDs []string, prepared *preparedBundleSync, status iostream.StatusFunc) {
	parallelism := len(sidecarIDs)
	if parallelism > bundleSyncFanOutConcurrency {
		parallelism = bundleSyncFanOutConcurrency
	}

	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for i, id := range sidecarIDs {
		entry := p.entries[i]
		wg.Add(1)
		go func(id string, entry *PoolEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			err := syncPreparedSidecar(ctx, p.client, id, p.identityFile, p.authSock, false, prepared)
			p.finishSync(entry, err)
		}(id, entry)
	}

	go func() {
		wg.Wait()
		p.mu.Lock()
		doneWithoutError := p.pendingSyncs == 0 && p.syncErr == nil
		p.mu.Unlock()
		if doneWithoutError {
			savePoolState(p.workDir, p.name, &poolState{
				SidecarIDs:    p.ids,
				RepoPath:      prepared.repoPath,
				LastSyncedRef: prepared.headRef,
			})
			status(iostream.LevelDone, fmt.Sprintf("Synced %d sidecars", len(sidecarIDs)))
		}
		p.notifyUpdate()
	}()
}

func (p *Pool) finishSync(entry *PoolEntry, err error) {
	p.mu.Lock()
	if err != nil {
		p.syncErr = errors.Join(p.syncErr, fmt.Errorf("sidecar %s: %w", entry.ID, err))
	} else {
		p.free <- entry
	}
	if p.pendingSyncs > 0 {
		p.pendingSyncs--
	}
	p.mu.Unlock()
	p.notifyUpdate()
}

func (p *Pool) notifyUpdate() {
	select {
	case p.updates <- struct{}{}:
	default:
	}
}
