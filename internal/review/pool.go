package review

import (
	"context"
	"fmt"
)

// PoolName is the sidecar pool name for reviews. It keys the persisted pool
// state in .chunk/review-pool.json, so a later run — or a later pass in the
// same run — picks up the same warm sidecars instead of booting new ones.
const PoolName = "review"

// WaitReady blocks until the pool's background clone creation and sync have
// finished, via waitSynced (the pool's WaitSynced), and reports any member
// that failed.
//
// sidecar.NewPool returns as soon as its members exist, while reused members
// may still be syncing in the background. Waiting here makes a failed sync
// surface before any review starts, rather than as one review failing partway
// through a pass. It holds no members while waiting: the pool reports a failed
// member through Acquire only once nothing is checked out.
func WaitReady(ctx context.Context, waitSynced func(context.Context) error) error {
	if err := waitSynced(ctx); err != nil {
		return fmt.Errorf("wait for sidecar pool: %w", err)
	}
	return nil
}
