package sidecar

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/circleci"
	"github.com/smeltdev/smelt/internal/iostream"
)

const poolCloseTimeout = 10 * time.Second

// assembleTestPool is assemblePool with the pool's lifetime tied to t: its
// background work finishes before the test's temp directories are removed.
func assembleTestPool(
	ctx context.Context,
	t *testing.T,
	be Backend,
	n int,
	name, orgID, image, repoPath, workDir, stateDir string,
	existingIDs []string,
	freshIDs map[string]bool,
	status iostream.StatusFunc,
) (*Pool, error) {
	t.Helper()
	pool, err := assemblePool(ctx, be, n, name, orgID, image, repoPath, workDir, stateDir, existingIDs, freshIDs, status)
	return closeWithTest(t, pool), err
}

// newTestPool is NewPool with the pool's lifetime tied to t.
func newTestPool(ctx context.Context, t *testing.T, client *circleci.Client, opts PoolOptions, status iostream.StatusFunc) (*Pool, error) {
	t.Helper()
	pool, err := NewPool(ctx, client, opts, status)
	return closeWithTest(t, pool), err
}

func closeWithTest(t *testing.T, pool *Pool) *Pool {
	if pool != nil {
		// Cleanups run in reverse order, so this runs before the TempDir
		// cleanups registered earlier by setupPoolTest.
		t.Cleanup(func() { closeAndWait(t, pool) })
	}
	return pool
}

// failReporter is the part of testing.TB closeAndWait needs.
type failReporter interface {
	Helper()
	Errorf(format string, args ...any)
}

func closeAndWait(t failReporter, pool *Pool) {
	t.Helper()
	closeAndWaitWithin(t, pool, poolCloseTimeout)
}

// closeAndWaitWithin closes pool and fails t if its background work has not
// finished within d, instead of leaking it into the next test.
func closeAndWaitWithin(t failReporter, pool *Pool, d time.Duration) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	finished := pool.waitForBackground(ctx)
	pool.Close(ctx)
	if !finished {
		t.Errorf("pool background work still running after %s", d)
	}
}

// blockedSyncBackend is a Backend whose Sync never returns until release is
// closed.
type blockedSyncBackend struct {
	Backend
	release chan struct{}
	once    sync.Once
}

func (b *blockedSyncBackend) Create(context.Context, string, string, string) (Instance, error) {
	return Instance{ID: "blocked-1"}, nil
}

func (b *blockedSyncBackend) IsStale(context.Context, string) bool { return false }

func (b *blockedSyncBackend) Delete(context.Context, string) error { return nil }

func (b *blockedSyncBackend) Sync(ctx context.Context, _, _, _ string, _ bool, _ iostream.StatusFunc) error {
	<-b.release
	return nil
}

func (b *blockedSyncBackend) unblock() { b.once.Do(func() { close(b.release) }) }

type recordingReporter struct {
	failed bool
	msg    string
}

func (r *recordingReporter) Helper() {}
func (r *recordingReporter) Errorf(format string, args ...any) {
	r.failed = true
	r.msg = format
}

func TestCloseAndWait_FailsWhenBackgroundWorkIsBlocked(t *testing.T) {
	be := &blockedSyncBackend{release: make(chan struct{})}
	t.Cleanup(be.unblock)
	dir := t.TempDir()

	pool, err := assemblePool(context.Background(), be, 1, "validate", "org-1", "img",
		"/workspace", dir, dir, []string{"blocked-1"}, nil, func(iostream.Level, string) {})
	assert.NilError(t, err)

	rec := &recordingReporter{}
	closeAndWaitWithin(rec, pool, 100*time.Millisecond)
	assert.Assert(t, rec.failed, "expected closeAndWait to report blocked background work")

	be.unblock()
	closeAndWait(t, pool)
}

// TestPoolTestsCloseTheirPools keeps pool_test.go on the helpers so a pool's
// background work cannot outlive the test's temp directories.
func TestPoolTestsCloseTheirPools(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "pool_test.go", nil, 0)
	assert.NilError(t, err)
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if id, ok := call.Fun.(*ast.Ident); ok && (id.Name == "assemblePool" || id.Name == "NewPool") {
			t.Errorf("%s: call %s through assembleTestPool/newTestPool", fset.Position(call.Pos()), id.Name)
		}
		return true
	})
}
