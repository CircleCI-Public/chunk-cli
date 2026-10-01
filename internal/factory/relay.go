// Package factory runs an implementer agent on a sidecar and loops it through
// review and validation until the checks pass or attempts run out.
package factory

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// relayConcurrency caps concurrent pushes from the worktree. Each push
// runs its own rsync and SSH tunnel.
const relayConcurrency = 8

// Relay carries the implementer's workspace to the reviewers through the run's
// worktree on this machine. Sidecars cannot reach each other, so the
// implementer's files are pulled into the worktree with rsync and pushed from
// there to each reviewer the same way the developer's own tree is synced.
//
// A pull mirrors the implementer's files into the worktree with --delete, so
// the directory must be one chunk owns. It leaves .git alone: the worktree
// keeps its own, and the implementer's git config and hooks never reach this
// machine, where git will run on the files.
type Relay struct {
	client *circleci.Client
	dir    string
	status iostream.StatusFunc
}

// NewRelay relays through dir, the run's worktree.
func NewRelay(client *circleci.Client, dir string, status iostream.StatusFunc) *Relay {
	if status == nil {
		status = func(iostream.Level, string) {}
	}
	return &Relay{client: client, dir: dir, status: status}
}

// Pull mirrors the workspace at repoPath on sidecarID into the worktree.
func (r *Relay) Pull(ctx context.Context, sidecarID, repoPath string) error {
	if err := sidecar.RsyncPull(ctx, r.client, sidecarID, repoPath, r.dir, r.status); err != nil {
		return fmt.Errorf("pull from %s: %w", sidecarID, err)
	}
	return nil
}

// Push mirrors the worktree to each entry's workspace, in parallel. It
// attempts every entry and reports all that failed.
func (r *Relay) Push(ctx context.Context, entries []*sidecar.PoolEntry) error {
	errs := make([]error, len(entries))
	sem := make(chan struct{}, relayConcurrency)
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(i int, id, repoPath string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Each push reports its own progress; tag it so concurrent pushes
			// can be told apart.
			status := func(level iostream.Level, msg string) {
				r.status(level, fmt.Sprintf("%s: %s", id, msg))
			}
			if err := sidecar.RsyncSyncEphemeral(ctx, r.client, id, repoPath, r.dir, status); err != nil {
				errs[i] = fmt.Errorf("push to %s: %w", id, err)
			}
		}(i, e.ID, e.RepoPath)
	}
	wg.Wait()
	return errors.Join(errs...)
}
