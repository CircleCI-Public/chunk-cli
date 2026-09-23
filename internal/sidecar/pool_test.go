package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func TestPoolStatePath(t *testing.T) {
	assert.Equal(t, poolStatePath("/my/project", "validate"), "/my/project/.chunk/validate-pool.json")
	assert.Equal(t, poolStatePath("/my/project", "mutate"), "/my/project/.chunk/mutate-pool.json")
}

func TestPoolStateRoundTrip(t *testing.T) {
	dir := t.TempDir()

	want := &poolState{
		SidecarIDs:    []string{"sb-1", "sb-2", "sb-3"},
		RepoPath:      "/home/user/myrepo",
		Image:         "snapshot-1",
		LastSyncedRef: "deadbeef",
	}
	assert.NilError(t, savePoolState(dir, "validate", want))

	got, err := loadPoolState(dir, "validate")
	assert.NilError(t, err)
	assert.DeepEqual(t, got.SidecarIDs, want.SidecarIDs)
	assert.Equal(t, got.RepoPath, want.RepoPath)
	assert.Equal(t, got.Image, want.Image)
	assert.Equal(t, got.LastSyncedRef, want.LastSyncedRef)
}

func TestSavePoolStateReturnsWriteError(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(dir, ".chunk"), nil, 0o600))

	err := savePoolState(dir, "validate", &poolState{SidecarIDs: []string{"sb-1"}})
	assert.Assert(t, err != nil)
}

func TestPoolAcquireRelease(t *testing.T) {
	entries := []*PoolEntry{{ID: "sb-1"}, {ID: "sb-2"}}
	free := make(chan *PoolEntry, len(entries))
	for _, entry := range entries {
		free <- entry
	}
	pool := &Pool{free: free, ids: []string{"sb-1", "sb-2"}, entries: entries}

	a, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	b, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, a.ID != b.ID)

	pool.Release(a)
	pool.Release(b)
	assert.Equal(t, len(pool.free), 2)
}

func TestPoolAcquireCancelledContext(t *testing.T) {
	pool := &Pool{free: make(chan *PoolEntry, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := pool.Acquire(ctx)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestPoolAcquireReadyEntryWinsOverSyncError(t *testing.T) {
	entry := &PoolEntry{ID: "sb-1"}
	free := make(chan *PoolEntry, 1)
	free <- entry
	pool := &Pool{
		free:    free,
		updates: make(chan struct{}, 1),
		syncErr: errors.New("background sync failed"),
	}

	got, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, got, entry)
}

func TestPoolAcquireTerminalSyncErrorWhenNothingAvailable(t *testing.T) {
	pool := &Pool{
		free:    make(chan *PoolEntry, 1),
		updates: make(chan struct{}, 1),
		syncErr: errors.New("background sync failed"),
	}

	_, err := pool.Acquire(context.Background())
	assert.Error(t, err, "background sync failed")
}

func TestPoolStateMarshalFormat(t *testing.T) {
	dir := t.TempDir()

	assert.NilError(t, savePoolState(dir, "validate", &poolState{
		SidecarIDs: []string{"sb-1"},
		RepoPath:   "/home/user/repo",
	}))

	raw, err := os.ReadFile(poolStatePath(dir, "validate"))
	assert.NilError(t, err)

	var m map[string]json.RawMessage
	assert.NilError(t, json.Unmarshal(raw, &m))
	assert.Assert(t, m["sidecar_ids"] != nil)
	assert.Assert(t, m["repo_path"] != nil)
}

func TestPoolPersistStatePreservesStoredMetadata(t *testing.T) {
	dir := t.TempDir()
	assert.NilError(t, savePoolState(dir, "validate", &poolState{
		SidecarIDs:    []string{"old"},
		RepoPath:      "/workspace/repo",
		Image:         "snapshot-1",
		LastSyncedRef: "deadbeef",
	}))
	pool := &Pool{
		ids:     []string{"new"},
		workDir: dir,
		name:    "validate",
	}

	assert.NilError(t, pool.persistState())
	state, err := loadPoolState(dir, "validate")
	assert.NilError(t, err)
	assert.DeepEqual(t, state.SidecarIDs, []string{"new"})
	assert.Equal(t, state.RepoPath, "/workspace/repo")
	assert.Equal(t, state.Image, "snapshot-1")
	assert.Equal(t, state.LastSyncedRef, "deadbeef")
}

type poolTestEnv struct {
	cl      *circleci.Client
	cci     *fakes.FakeCircleCI
	workDir string
}

func setupPoolTest(t *testing.T) poolTestEnv {
	t.Helper()
	homeDir := t.TempDir()
	t.Setenv(config.EnvHome, homeDir)
	pubKey := fakes.GenerateSSHKeypairAt(t, filepath.Join(homeDir, ".ssh", "chunk_ai"))
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResult("", 0)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	cl, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	return poolTestEnv{cl: cl, cci: cci, workDir: workDir}
}

func countPoolRequests(cci *fakes.FakeCircleCI, method, path string) int {
	n := 0
	for _, r := range cci.Recorder.AllRequests() {
		if r.Method == method && r.URL.Path == path {
			n++
		}
	}
	return n
}

func countPoolDeletes(cci *fakes.FakeCircleCI) int {
	n := 0
	for _, r := range cci.Recorder.AllRequests() {
		if r.Method == "DELETE" {
			n++
		}
	}
	return n
}

func waitForPoolTest(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for pool state")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

const createSidecarPath = "/api/v3/sidecar/instances"
const createSnapshotPath = "/api/v3/sidecar/snapshots"

func TestAssemblePool_CreatesAndSyncsAll(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)

	pool, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, nil, "", nil, func(iostream.Level, string) {})
	assert.NilError(t, err)

	a, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	b, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, a.ID != b.ID)
	pool.Release(a)
	pool.Release(b)

	assert.Equal(t, countPoolRequests(env.cci, "POST", createSidecarPath), 3)
	assert.Equal(t, countPoolRequests(env.cci, "POST", createSnapshotPath), 1)
	waitForPoolTest(t, func() bool { return countPoolDeletes(env.cci) == 1 })
}

func TestAssemblePool_CreateFailure_ReturnsError(t *testing.T) {
	env := setupPoolTest(t)
	env.cci.CreateStatusCode = 500

	_, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, nil, "", nil, func(iostream.Level, string) {})
	assert.Assert(t, err != nil)
	assert.Equal(t, countPoolDeletes(env.cci), 0)
}

func TestAssemblePool_AllCloneCreatesFail(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	env.cci.CreateErrorAfter = 1

	pool, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, nil, "", nil, func(iostream.Level, string) {})
	assert.NilError(t, err)
	_, err = pool.Acquire(context.Background())
	assert.ErrorContains(t, err, "create sidecar")
	waitForPoolTest(t, func() bool { return countPoolDeletes(env.cci) == 1 })
}

func TestAssemblePool_PartialCloneFailureKeepsSuccessfulClone(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	env.cci.CreateErrorAfter = 2

	pool, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, nil, "", nil, func(iostream.Level, string) {})
	assert.NilError(t, err)
	entry, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	pool.Release(entry)

	waitForPoolTest(t, func() bool {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		return pool.pendingCreates == 0
	})
	waitForPoolTest(t, func() bool { return countPoolDeletes(env.cci) == 1 })
	pool.mu.Lock()
	assert.DeepEqual(t, pool.ids, []string{entry.ID})
	pool.mu.Unlock()
}

func TestAssemblePool_CloneIsAvailableBeforeAllCreatesFinish(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	blocked := make(chan struct{})
	env.cci.CreateWait = map[int]<-chan struct{}{2: blocked}
	t.Cleanup(func() {
		select {
		case <-blocked:
		default:
			close(blocked)
		}
	})

	pool, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, nil, "", nil, func(iostream.Level, string) {})
	assert.NilError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	first, err := pool.Acquire(ctx)
	assert.NilError(t, err)
	assert.Equal(t, first.ID, "sidecar-new-3")

	close(blocked)
	second, err := pool.Acquire(ctx)
	assert.NilError(t, err)
	assert.Assert(t, first.ID != second.ID)
	pool.Release(first)
	pool.Release(second)
}

func TestPoolCloseCancelsOutstandingCloneCreates(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	blocked := make(chan struct{})
	env.cci.CreateWait = map[int]<-chan struct{}{2: blocked, 3: blocked}
	t.Cleanup(func() { close(blocked) })

	pool, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, nil, "", nil, func(iostream.Level, string) {})
	assert.NilError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool.Close(ctx)
	assert.NilError(t, ctx.Err())
	waitForPoolTest(t, func() bool { return countPoolDeletes(env.cci) == 1 })

	pool.mu.Lock()
	assert.Equal(t, pool.pendingCreates, 0)
	pool.mu.Unlock()
}

func TestAssemblePool_SyncFailure_CleansUp(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	env.cci.AddKeyStatusCode = 500

	_, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, nil, "", nil, func(iostream.Level, string) {})
	assert.Assert(t, err != nil)
	assert.Equal(t, countPoolDeletes(env.cci), 1)
	assert.Equal(t, len(env.cci.Sidecars), 0)
}

func TestAssemblePool_StaleExisting_ReplacedAutomatically(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	env.cci.StaleIDs = map[string]bool{"stale-sb-1": true, "stale-sb-2": true}

	pool, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, []string{"stale-sb-1", "stale-sb-2"}, "", nil, func(iostream.Level, string) {})
	assert.NilError(t, err)
	entries := drainPoolEntries(context.Background(), t, pool, 2)
	releasePoolEntries(pool, entries)
	assert.Equal(t, len(pool.ids), 2)
	waitForPoolTest(t, func() bool { return countPoolDeletes(env.cci) == 3 })
}

func TestAssemblePool_OutdatedExisting_ReplacedAutomatically(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	env.cci.OutdatedIDs = map[string]bool{"outdated-sb-1": true}
	ctx := context.Background()
	assert.NilError(t, SaveActive(ctx, ActiveSidecar{
		SidecarIDs: []string{"outdated-sb-1"},
		Name:       "development",
		OrgID:      "org-1",
		Workspace:  "/workspace/my-repo",
	}))

	pool, err := assemblePool(ctx, env.cl, 1, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, []string{"outdated-sb-1"}, "", nil, func(iostream.Level, string) {})
	assert.NilError(t, err)
	entry, err := pool.Acquire(ctx)
	assert.NilError(t, err)
	pool.Release(entry)

	assert.Assert(t, entry.ID != "outdated-sb-1")
	active, err := LoadActive(ctx)
	assert.NilError(t, err)
	assert.DeepEqual(t, active.SidecarIDs, []string{entry.ID})
	assert.Equal(t, active.Name, "development")
	assert.Equal(t, active.OrgID, "org-1")
	assert.Equal(t, active.Workspace, "/workspace/my-repo")
	assert.Equal(t, countPoolRequests(env.cci, "POST", createSidecarPath), 1)
	assert.Equal(t, countPoolDeletes(env.cci), 1)

	second, err := NewPool(ctx, env.cl, PoolOptions{
		Size:        1,
		Name:        "validate",
		OrgID:       active.OrgID,
		Image:       "ubuntu:22.04",
		WorkDir:     env.workDir,
		RepoPath:    DefaultWorkspace("my-repo"),
		ExistingIDs: active.SidecarIDs,
	}, func(iostream.Level, string) {})
	assert.NilError(t, err)
	secondEntry, err := second.Acquire(ctx)
	assert.NilError(t, err)
	second.Release(secondEntry)

	assert.Equal(t, secondEntry.ID, entry.ID)
	assert.Equal(t, countPoolRequests(env.cci, "POST", createSidecarPath), 1)
}

func TestAssemblePool_SidecarGoneDuringSync_ReplacedAutomatically(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	env.cci.StaleAfterAddKey = map[string]int{"existing-sb-1": 1}
	assert.NilError(t, SaveActive(context.Background(), ActiveSidecar{
		SidecarIDs: []string{"other-sb", "existing-sb-1"},
		Name:       "development",
	}))

	pool, err := assemblePool(context.Background(), env.cl, 1, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, []string{"existing-sb-1"}, "", nil, func(iostream.Level, string) {})
	assert.NilError(t, err)
	entry, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	pool.Release(entry)

	assert.Assert(t, entry.ID != "existing-sb-1")
	assert.Equal(t, pool.ids[0], entry.ID)
	assert.Equal(t, countPoolRequests(env.cci, "POST", createSidecarPath), 1)
	assert.Equal(t, countPoolDeletes(env.cci), 1)
	active, err := LoadActive(context.Background())
	assert.NilError(t, err)
	assert.DeepEqual(t, active.SidecarIDs, []string{"other-sb", entry.ID})
	assert.Equal(t, active.Name, "development")
}

func TestAssemblePool_OrdinaryExistingSyncFailureDoesNotReplace(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	env.cci.AddKeyStatusCode = 500

	pool, err := assemblePool(context.Background(), env.cl, 1, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, []string{"existing-sb-1"}, "", nil, func(iostream.Level, string) {})
	assert.NilError(t, err)
	_, err = pool.Acquire(context.Background())
	assert.Assert(t, err != nil)

	assert.Equal(t, countPoolRequests(env.cci, "POST", createSidecarPath), 0)
	assert.Equal(t, countPoolDeletes(env.cci), 0)
}

func TestAssemblePool_ReuseExisting_OnlyCreatesGap(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)

	pool, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, []string{"existing-sb-1"}, "", nil, func(iostream.Level, string) {})
	assert.NilError(t, err)
	assert.Equal(t, len(pool.ids), 2)
	assert.Equal(t, countPoolRequests(env.cci, "POST", createSidecarPath), 1)
	assert.Equal(t, countPoolDeletes(env.cci), 0)
}

func TestAssemblePool_FreshExistingSkipsStaleProbe(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)

	pool, err := assemblePool(context.Background(), env.cl, 1, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, []string{"fresh-sb-1"}, "", map[string]bool{"fresh-sb-1": true}, func(iostream.Level, string) {})
	assert.NilError(t, err)
	entry, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	pool.Release(entry)

	assert.Equal(t, countPoolRequests(env.cci, "POST", "/api/v3/sidecar/instances/fresh-sb-1/ssh/add-key"), 1)
}

func TestAssemblePool_FreshExistingRetriesProvisioningLag(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	env.cci.NotFoundBeforeAddKey = map[string]int{"fresh-sb-1": 1}

	pool, err := assemblePool(context.Background(), env.cl, 1, "validate", "org-1", "snapshot-1",
		DefaultWorkspace("my-repo"), env.workDir, []string{"fresh-sb-1"}, "", map[string]bool{"fresh-sb-1": true}, func(iostream.Level, string) {})
	assert.NilError(t, err)
	t.Cleanup(func() { pool.Close(context.Background()) })
	entry, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	pool.Release(entry)

	assert.Equal(t, entry.ID, "fresh-sb-1")
	assert.Equal(t, countPoolRequests(env.cci, "POST", "/api/v3/sidecar/instances/fresh-sb-1/ssh/add-key"), 2)
	assert.Equal(t, countPoolRequests(env.cci, "POST", createSidecarPath), 0)
	assert.Equal(t, countPoolDeletes(env.cci), 0)
}

func TestNewPoolRestoresCreationContext(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	assert.NilError(t, savePoolState(env.workDir, "validate", &poolState{
		SidecarIDs: []string{"existing-sb-1"},
		RepoPath:   "/saved/workspace",
		Image:      "snapshot-1",
	}))

	pool, err := NewPool(context.Background(), env.cl, PoolOptions{
		Size:    1,
		Name:    "validate",
		OrgID:   "org-1",
		WorkDir: env.workDir,
	}, func(iostream.Level, string) {})
	assert.NilError(t, err)
	entry, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	pool.Release(entry)

	assert.Equal(t, pool.image, "snapshot-1")
	assert.Equal(t, entry.RepoPath, "/saved/workspace")
}

func TestNewPoolUsesConfiguredRepoPath(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)

	pool, err := NewPool(context.Background(), env.cl, PoolOptions{
		Size:        1,
		Name:        "validate",
		OrgID:       "org-1",
		Image:       "ubuntu:22.04",
		WorkDir:     env.workDir,
		RepoPath:    "/custom/workspace",
		ExistingIDs: []string{"fresh-sb-1"},
		FreshIDs:    []string{"fresh-sb-1"},
	}, func(iostream.Level, string) {})
	assert.NilError(t, err)
	entry, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	pool.Release(entry)

	assert.Equal(t, entry.RepoPath, "/custom/workspace")
}

func TestPool_Replace(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)

	pool := &Pool{
		client:     env.cl,
		orgID:      "org-1",
		image:      "ubuntu:22.04",
		name:       "validate",
		workDir:    env.workDir,
		repoPath:   DefaultWorkspace("my-repo"),
		free:       make(chan *PoolEntry, 1),
		updates:    make(chan struct{}, 1),
		checkedOut: 1,
	}
	dead := &PoolEntry{ID: "dead-sb-1", RepoPath: DefaultWorkspace("my-repo"), Client: env.cl}
	pool.entries = []*PoolEntry{dead}
	pool.ids = []string{dead.ID}

	err := pool.Replace(context.Background(), dead, func(iostream.Level, string) {})
	assert.NilError(t, err)
	entry, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, entry.ID != dead.ID)
	assert.Assert(t, entry.Client != nil)
	assert.Equal(t, countPoolDeletes(env.cci), 1)
	assert.Equal(t, countPoolRequests(env.cci, "POST", createSidecarPath), 1)
}

func drainPoolEntries(ctx context.Context, t *testing.T, pool *Pool, n int) []*PoolEntry {
	t.Helper()
	entries := make([]*PoolEntry, n)
	for i := range entries {
		entry, err := pool.Acquire(ctx)
		assert.NilError(t, err)
		entries[i] = entry
	}
	return entries
}

func releasePoolEntries(pool *Pool, entries []*PoolEntry) {
	for _, entry := range entries {
		pool.Release(entry)
	}
}

func TestPool_100Sidecars_NoGoroutineLeak(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)

	const n = 100
	ctx := context.Background()
	noopStatus := iostream.StatusFunc(func(_ iostream.Level, _ string) {})

	pool, err := assemblePool(ctx, env.cl, n, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, nil, "", nil, noopStatus)
	assert.NilError(t, err)
	entries := drainPoolEntries(ctx, t, pool, n)
	assert.Equal(t, len(pool.ids), n)
	releasePoolEntries(pool, entries)

	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	pool2, err := assemblePool(ctx, env.cl, n, "validate", "org-1", "ubuntu:22.04",
		DefaultWorkspace("my-repo"), env.workDir, pool.ids, "", nil, noopStatus)
	assert.NilError(t, err)
	assert.Equal(t, len(pool2.ids), n)
	releasePoolEntries(pool2, drainPoolEntries(ctx, t, pool2, n))

	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	afterSecond := runtime.NumGoroutine()
	assert.Assert(t, afterSecond <= baseline+5,
		"goroutine accumulation on second pool run: baseline=%d after=%d delta=%d",
		baseline, afterSecond, afterSecond-baseline)
}
