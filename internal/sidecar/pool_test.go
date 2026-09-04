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
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".chunk"), 0o755))

	want := &poolState{
		SidecarIDs:    []string{"sb-1", "sb-2", "sb-3"},
		RepoPath:      "/home/user/myrepo",
		LastSyncedRef: "deadbeef",
	}
	savePoolState(dir, "validate", want)

	got, err := loadPoolState(dir, "validate")
	assert.NilError(t, err)
	assert.DeepEqual(t, got.SidecarIDs, want.SidecarIDs)
	assert.Equal(t, got.RepoPath, want.RepoPath)
	assert.Equal(t, got.LastSyncedRef, want.LastSyncedRef)
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
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".chunk"), 0o755))

	savePoolState(dir, "validate", &poolState{
		SidecarIDs: []string{"sb-1"},
		RepoPath:   "/home/user/repo",
	})

	raw, err := os.ReadFile(poolStatePath(dir, "validate"))
	assert.NilError(t, err)

	var m map[string]json.RawMessage
	assert.NilError(t, json.Unmarshal(raw, &m))
	assert.Assert(t, m["sidecar_ids"] != nil)
	assert.Assert(t, m["repo_path"] != nil)
}

type poolTestEnv struct {
	cl      *circleci.Client
	cci     *fakes.FakeCircleCI
	workDir string
	keyFile string
}

func setupPoolTest(t *testing.T) poolTestEnv {
	t.Helper()
	keyFile, pubKey := fakes.GenerateSSHKeypair(t)
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResult("", 0)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	cl, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	t.Setenv(config.EnvHome, t.TempDir())
	return poolTestEnv{cl: cl, cci: cci, workDir: workDir, keyFile: keyFile}
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

const createSidecarPath = "/api/v3/sidecar/instances"
const createSnapshotPath = "/api/v3/sidecar/snapshots"

func TestAssemblePool_CreatesAndSyncsAll(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)

	pool, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		env.keyFile, "", DefaultWorkspace("my-repo"), env.workDir, nil, "", func(iostream.Level, string) {})
	assert.NilError(t, err)
	assert.Equal(t, len(pool.ids), 2)

	a, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	b, err := pool.Acquire(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, a.ID != b.ID)
	pool.Release(a)
	pool.Release(b)

	assert.Equal(t, countPoolRequests(env.cci, "POST", createSidecarPath), 3)
	assert.Equal(t, countPoolRequests(env.cci, "POST", createSnapshotPath), 1)
	assert.Equal(t, countPoolDeletes(env.cci), 1)
}

func TestAssemblePool_CreateFailure_ReturnsError(t *testing.T) {
	env := setupPoolTest(t)
	env.cci.CreateStatusCode = 500

	_, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		env.keyFile, "", DefaultWorkspace("my-repo"), env.workDir, nil, "", func(iostream.Level, string) {})
	assert.Assert(t, err != nil)
	assert.Equal(t, countPoolDeletes(env.cci), 0)
}

func TestAssemblePool_PartialCreateFailure_CleansUp(t *testing.T) {
	env := setupPoolTest(t)
	env.cci.CreateErrorAfter = 1

	_, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		env.keyFile, "", DefaultWorkspace("my-repo"), env.workDir, nil, "", func(iostream.Level, string) {})
	assert.Assert(t, err != nil)
	assert.Equal(t, countPoolDeletes(env.cci), 1)
	assert.Equal(t, len(env.cci.Sidecars), 0)
}

func TestAssemblePool_SyncFailure_CleansUp(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	env.cci.AddKeyStatusCode = 500

	_, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		env.keyFile, "", DefaultWorkspace("my-repo"), env.workDir, nil, "", func(iostream.Level, string) {})
	assert.Assert(t, err != nil)
	assert.Equal(t, countPoolDeletes(env.cci), 1)
	assert.Equal(t, len(env.cci.Sidecars), 0)
}

func TestAssemblePool_StaleExisting_ReplacedAutomatically(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)
	env.cci.StaleIDs = map[string]bool{"stale-sb-1": true, "stale-sb-2": true}

	pool, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		env.keyFile, "", DefaultWorkspace("my-repo"), env.workDir, []string{"stale-sb-1", "stale-sb-2"}, "", func(iostream.Level, string) {})
	assert.NilError(t, err)
	assert.Equal(t, len(pool.ids), 2)
	assert.Equal(t, countPoolDeletes(env.cci), 3)
}

func TestAssemblePool_ReuseExisting_OnlyCreatesGap(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)

	pool, err := assemblePool(context.Background(), env.cl, 2, "validate", "org-1", "ubuntu:22.04",
		env.keyFile, "", DefaultWorkspace("my-repo"), env.workDir, []string{"existing-sb-1"}, "", func(iostream.Level, string) {})
	assert.NilError(t, err)
	assert.Equal(t, len(pool.ids), 2)
	assert.Equal(t, countPoolRequests(env.cci, "POST", createSidecarPath), 1)
	assert.Equal(t, countPoolDeletes(env.cci), 0)
}

func TestPool_Rebuild(t *testing.T) {
	env := setupPoolTest(t)
	t.Chdir(env.workDir)

	pool := &Pool{
		client:       env.cl,
		orgID:        "org-1",
		image:        "ubuntu:22.04",
		name:         "validate",
		identityFile: env.keyFile,
		workDir:      env.workDir,
		free:         make(chan *PoolEntry, 1),
	}
	dead := &PoolEntry{ID: "dead-sb-1", RepoPath: DefaultWorkspace("my-repo")}

	entry, err := pool.Rebuild(context.Background(), dead, func(iostream.Level, string) {})
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
		env.keyFile, "", DefaultWorkspace("my-repo"), env.workDir, nil, "", noopStatus)
	assert.NilError(t, err)
	assert.Equal(t, len(pool.ids), n)
	releasePoolEntries(pool, drainPoolEntries(ctx, t, pool, n))

	runtime.GC()
	time.Sleep(100 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	pool2, err := assemblePool(ctx, env.cl, n, "validate", "org-1", "ubuntu:22.04",
		env.keyFile, "", DefaultWorkspace("my-repo"), env.workDir, pool.ids, "", noopStatus)
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
