package review

import (
	"context"
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// PoolName is the sidecar pool name for reviews. It keys the persisted pool
// state in .chunk/review-pool.json, so a later run — or a later pass in the
// same run — picks up the same warm sidecars instead of booting new ones.
const PoolName = "review"

// Acquirer is the part of *sidecar.Pool that reviews need.
type Acquirer interface {
	Acquire(ctx context.Context) (*sidecar.PoolEntry, error)
	Release(entry *sidecar.PoolEntry)
}

// WaitReady blocks until all n pool members are synced and free, then returns
// them to the pool.
//
// sidecar.NewPool returns as soon as its members exist, while reused members
// may still be syncing in the background. Waiting here makes a failed sync
// surface before any review starts, rather than as one review failing partway
// through a pass.
func WaitReady(ctx context.Context, pool Acquirer, n int) error {
	entries := make([]*sidecar.PoolEntry, 0, n)
	defer func() {
		for _, e := range entries {
			pool.Release(e)
		}
	}()
	for range n {
		e, err := pool.Acquire(ctx)
		if err != nil {
			return fmt.Errorf("wait for sidecar %d of %d: %w", len(entries)+1, n, err)
		}
		entries = append(entries, e)
	}
	return nil
}
