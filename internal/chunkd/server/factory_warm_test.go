package server

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// blockingBuild is a warmMaker that counts its calls and makes nothing until
// release is closed or its context ends, reporting which.
type blockingBuild struct {
	calls     atomic.Int32
	release   chan struct{}
	cancelled chan struct{}
}

func newBlockingBuild() *blockingBuild {
	return &blockingBuild{release: make(chan struct{}), cancelled: make(chan struct{}, 1)}
}

func (b *blockingBuild) build(ctx context.Context) (*factory.WarmPool, error) {
	b.calls.Add(1)
	select {
	case <-b.release:
		return nil, nil
	case <-ctx.Done():
		b.cancelled <- struct{}{}
		return nil, ctx.Err()
	}
}

// Asking twice for the same run's sidecars makes them once, and a run takes
// them, after which they are no longer the store's.
func TestWarmStoreMakesARunsSidecarsOnceForTheRunToTake(t *testing.T) {
	s := newWarmStore(t.Context())
	t.Cleanup(s.stopAll)
	b := newBlockingBuild()

	s.start("run-1", b.build)
	s.start("run-1", b.build)
	close(b.release)

	e := s.take("run-1")
	assert.Assert(t, e != nil)
	assert.Assert(t, e.wait(t.Context()) == nil, "this build makes no pool")
	assert.Equal(t, b.calls.Load(), int32(1))
	assert.Assert(t, s.take("run-1") == nil, "a run's sidecars are taken once")
	assert.Assert(t, s.take("run-2") == nil)
}

// Sidecars no run takes in time are given up, even while still being made.
func TestWarmStoreGivesUpSidecarsNoRunTakes(t *testing.T) {
	s := newWarmStore(t.Context())
	s.ttl = 50 * time.Millisecond
	t.Cleanup(s.stopAll)
	b := newBlockingBuild()

	s.start("run-1", b.build)

	select {
	case <-b.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("the build was not stopped")
	}
	assert.Assert(t, s.take("run-1") == nil)
}

// Cancelling stops sidecars still being made.
func TestWarmStoreDiscardStopsABuild(t *testing.T) {
	s := newWarmStore(t.Context())
	t.Cleanup(s.stopAll)
	b := newBlockingBuild()
	s.start("run-1", b.build)

	s.discard("run-1")

	select {
	case <-b.cancelled:
	default:
		t.Fatal("the build was not stopped")
	}
	assert.Assert(t, s.take("run-1") == nil)
}

// A build that fails leaves nothing for a run to wait on.
func TestWarmStoreForgetsAFailedBuild(t *testing.T) {
	s := newWarmStore(t.Context())
	t.Cleanup(s.stopAll)
	done := make(chan struct{})
	s.start("run-1", func(context.Context) (*factory.WarmPool, error) {
		defer close(done)
		return nil, errors.New("no sidecars today")
	})
	<-done

	deadline := time.Now().Add(5 * time.Second)
	for s.take("run-1") != nil {
		assert.Assert(t, time.Now().Before(deadline), "the failed build was kept")
	}
}

// The daemon shutting down stops every build and waits for it.
func TestWarmStoreStopAllStopsEveryBuild(t *testing.T) {
	s := newWarmStore(t.Context())
	a, b := newBlockingBuild(), newBlockingBuild()
	s.start("run-1", a.build)
	s.start("run-2", b.build)

	s.stopAll()

	for _, bb := range []*blockingBuild{a, b} {
		select {
		case <-bb.cancelled:
		default:
			t.Fatal("a build was not stopped")
		}
	}
}

// A run with nothing made ahead of it, or nothing that could be made, makes
// its own sidecars.
func TestClaimWarmWithNothingMadeLeavesTheRunToMakeItsOwn(t *testing.T) {
	d := &daemon{}
	opts := factory.RunOptions{}
	release := d.claimWarm(t.Context(), nil, &opts, func(iostream.Level, string) {})
	release()
	assert.Assert(t, opts.Warm == nil)

	s := newWarmStore(t.Context())
	t.Cleanup(s.stopAll)
	b := newBlockingBuild()
	s.start("run-1", b.build)
	close(b.release)
	release = d.claimWarm(t.Context(), s.take("run-1"), &opts, func(iostream.Level, string) {})
	release()
	assert.Assert(t, opts.Warm == nil)
}

// A run continuing a started run is handed the sidecars made ahead of it when
// they suit it, and makes its own when they do not.
func TestAContinuedRunIsHandedTheSidecarsMadeAheadOfIt(t *testing.T) {
	got := make(chan *factory.WarmPool, 1)
	d, root := newFactoryDaemon(t, func(_ context.Context, opts factory.RunOptions) (factory.Report, error) {
		got <- opts.Warm
		return factory.Report{}, nil
	})
	d.warm = newWarmStore(t.Context())
	t.Cleanup(d.warm.stopAll)
	rec, err := factory.Start(t.Context(), root, factory.Handoff{Prompt: "add a flag"})
	assert.NilError(t, err)
	reviewers := 2 // one per review prompt in the project

	for _, tc := range []struct {
		name      string
		reviewers int
		used      bool
	}{
		{name: "suits the run", reviewers: reviewers, used: true},
		{name: "made for other reviewers", reviewers: reviewers + 1, used: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := &factory.WarmPool{RunID: rec.RunID, Reviewers: tc.reviewers, OrgID: "org-1", Image: "img"}
			d.warm.start(rec.RunID, func(context.Context) (*factory.WarmPool, error) { return pool, nil })

			sess, err := d.startFactory(chunkd.FactoryRequest{ProjectRoot: root, Continue: rec.RunID, OrgID: "org-1", Image: "img"})
			assert.NilError(t, err)

			assert.Equal(t, <-got == pool, tc.used)
			waitForSessionEnd(t, d, sess.ID)
			assert.Assert(t, d.warm.take(rec.RunID) == nil, "the run took them either way")
		})
	}
}
