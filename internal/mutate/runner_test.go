package mutate

import (
	"context"
	"errors"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestRunWithDispatchesInParallelAndPreservesOrder(t *testing.T) {
	entries := make(chan *sidecar.PoolEntry, 2)
	entries <- &sidecar.PoolEntry{ID: "sidecar-1"}
	entries <- &sidecar.PoolEntry{ID: "sidecar-2"}
	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, 2)
	proceed := make(chan struct{})
	go func() {
		<-started
		<-started
		close(proceed)
	}()
	fn := runnerFuncs{
		acquire: func(ctx context.Context) (*sidecar.PoolEntry, error) {
			select {
			case entry := <-entries:
				return entry, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		release: func(entry *sidecar.PoolEntry) { entries <- entry },
		replace: func(context.Context, *sidecar.PoolEntry, iostream.StatusFunc) error {
			return errors.New("unexpected replacement")
		},
		baseline: func(context.Context, *sidecar.PoolEntry, string, time.Duration) error { return nil },
		patch:    func(_ string, m Mutation) ([]byte, error) { return []byte(m.ID), nil },
		run: func(_ context.Context, _ *sidecar.PoolEntry, patch []byte, _ string, _ time.Duration) (bool, string, string, bool) {
			current := active.Add(1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			if string(patch) != "MUT-003" {
				started <- struct{}{}
				<-proceed
			}
			active.Add(-1)
			return string(patch) == "MUT-002", string(patch), "", true
		},
	}
	mutations := []Mutation{{ID: "MUT-001"}, {ID: "MUT-002"}, {ID: "MUT-003"}}
	var statusMessages []string

	results, err := runWith(context.Background(), mutations, "/project", "task test", time.Second, func(_ iostream.Level, message string) {
		statusMessages = append(statusMessages, message)
	}, fn)

	assert.NilError(t, err)
	assert.Equal(t, maximum.Load(), int32(2))
	assert.Equal(t, len(results), 3)
	for i := range mutations {
		assert.Equal(t, results[i].Mutation.ID, mutations[i].ID)
	}
	assert.Assert(t, results[1].Killed)
	assert.Assert(t, slices.Contains(statusMessages, "Used 2 sidecars for mutation tests"))
}

func TestRunWithWaitsForStartedJobsWhenAcquireFails(t *testing.T) {
	entry := &sidecar.PoolEntry{ID: "sidecar-1"}
	acquires := 0
	var released atomic.Int32
	jobFinished := make(chan struct{})
	fn := runnerFuncs{
		acquire: func(context.Context) (*sidecar.PoolEntry, error) {
			acquires++
			if acquires <= 2 {
				return entry, nil
			}
			return nil, errors.New("pool unavailable")
		},
		release:  func(*sidecar.PoolEntry) { released.Add(1) },
		replace:  func(context.Context, *sidecar.PoolEntry, iostream.StatusFunc) error { return nil },
		baseline: func(context.Context, *sidecar.PoolEntry, string, time.Duration) error { return nil },
		patch:    func(_ string, m Mutation) ([]byte, error) { return []byte(m.ID), nil },
		run: func(context.Context, *sidecar.PoolEntry, []byte, string, time.Duration) (bool, string, string, bool) {
			defer close(jobFinished)
			return false, "", "", true
		},
	}

	results, err := runWith(context.Background(), []Mutation{{ID: "MUT-001"}, {ID: "MUT-002"}}, "/project", "task test", time.Second, func(iostream.Level, string) {}, fn)

	assert.ErrorContains(t, err, "acquire sidecar: pool unavailable")
	<-jobFinished
	assert.Equal(t, len(results), 1)
	assert.Equal(t, released.Load(), int32(2))
}

func TestRunWithReplacesUnusableWorker(t *testing.T) {
	entry := &sidecar.PoolEntry{ID: "sidecar-1"}
	entries := make(chan *sidecar.PoolEntry, 1)
	entries <- entry
	var replaced atomic.Int32
	fn := runnerFuncs{
		acquire: func(context.Context) (*sidecar.PoolEntry, error) { return <-entries, nil },
		release: func(entry *sidecar.PoolEntry) { entries <- entry },
		replace: func(context.Context, *sidecar.PoolEntry, iostream.StatusFunc) error {
			replaced.Add(1)
			return nil
		},
		baseline: func(context.Context, *sidecar.PoolEntry, string, time.Duration) error { return nil },
		patch:    func(string, Mutation) ([]byte, error) { return []byte("patch"), nil },
		run: func(context.Context, *sidecar.PoolEntry, []byte, string, time.Duration) (bool, string, string, bool) {
			return false, "", "cleanup failed", false
		},
	}

	results, err := runWith(context.Background(), []Mutation{{ID: "MUT-001"}}, "/project", "task test", time.Second, func(iostream.Level, string) {}, fn)

	assert.NilError(t, err)
	assert.Equal(t, replaced.Load(), int32(1))
	assert.Equal(t, results[0].Error, "cleanup failed")
}

func TestRunWithSkipsReplacementWhenCancelled(t *testing.T) {
	entry := &sidecar.PoolEntry{ID: "sidecar-1"}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var replaced, released atomic.Int32
	fn := runnerFuncs{
		acquire: func(context.Context) (*sidecar.PoolEntry, error) { return entry, nil },
		release: func(*sidecar.PoolEntry) { released.Add(1) },
		replace: func(context.Context, *sidecar.PoolEntry, iostream.StatusFunc) error {
			replaced.Add(1)
			return nil
		},
		baseline: func(context.Context, *sidecar.PoolEntry, string, time.Duration) error { return nil },
		patch:    func(string, Mutation) ([]byte, error) { return []byte("patch"), nil },
		run: func(context.Context, *sidecar.PoolEntry, []byte, string, time.Duration) (bool, string, string, bool) {
			cancel()
			return false, "", "apply patch: context canceled", false
		},
	}

	results, err := runWith(ctx, []Mutation{{ID: "MUT-001"}}, "/project", "task test", time.Second, func(iostream.Level, string) {}, fn)

	assert.NilError(t, err)
	assert.Equal(t, len(results), 1)
	assert.Equal(t, replaced.Load(), int32(0))
	assert.Equal(t, released.Load(), int32(2), "baseline and mutation worker are both released")
}

func TestValidateBaseline(t *testing.T) {
	entry := mutationTestEntry(t, &fakes.ExecResponse{ExitCode: 1, Stderr: "tests failed\n"})

	err := validateBaseline(context.Background(), entry, "task test", time.Second)

	var baselineErr *BaselineError
	assert.Assert(t, errors.As(err, &baselineErr))
	assert.Equal(t, baselineErr.ExitCode, 1)
	assert.Assert(t, strings.Contains(baselineErr.Output, "tests failed"))
	assert.ErrorContains(t, err, "baseline test failed (exit 1)")
}

func mutationTestEntry(t *testing.T, response *fakes.ExecResponse) *sidecar.PoolEntry {
	t.Helper()
	fake := fakes.NewFakeCircleCI()
	fake.ExecResponse = response
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: server.URL})
	assert.NilError(t, err)
	return &sidecar.PoolEntry{ID: "sidecar-1", RepoPath: "/workspace/project", Client: client}
}

func TestRunMutationMarksWorkerUnusableWhenCleanupFails(t *testing.T) {
	call := 0
	exec := func(context.Context, string) (int, string, error) {
		call++
		if call == 3 {
			return 1, "patch did not apply", nil
		}
		return 0, "", nil
	}

	_, _, errMsg, reusable := runMutation(context.Background(), "/workspace/project", []byte("patch"), "task test", time.Second, exec)

	assert.Assert(t, !reusable)
	assert.Assert(t, strings.Contains(errMsg, "reverse patch (exit 1)"))
}

func TestTailBufferKeepsNewestOutput(t *testing.T) {
	buf := newTailBuffer(4)
	_, err := buf.Write([]byte("abc"))
	assert.NilError(t, err)
	_, err = buf.Write([]byte("def"))
	assert.NilError(t, err)

	assert.Equal(t, buf.String(), "[2 bytes truncated]\ncdef")
}

func TestTailBufferBoundsSingleLargeWrite(t *testing.T) {
	buf := newTailBuffer(4)
	_, err := buf.Write([]byte("abcdef"))
	assert.NilError(t, err)

	assert.Equal(t, buf.String(), "[2 bytes truncated]\ncdef")
	assert.Assert(t, !strings.Contains(buf.String(), "ab"))
}
