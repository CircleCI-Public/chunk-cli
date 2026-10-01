// Package factory runs an implementer agent on a sidecar and loops it through
// review and validation until the checks pass or attempts run out.
package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// relayConcurrency caps concurrent pushes from the staging copy. Each push
// runs its own rsync and SSH tunnel.
const relayConcurrency = 8

// Relay carries a workspace from one sidecar to others through a staging copy
// on this machine. Sidecars cannot reach each other, so the implementer's tree
// is pulled here with rsync and pushed from here to each reviewer the same way
// the developer's own tree is synced.
//
// The staging copy is a temporary directory the relay creates and owns: a
// pull mirrors the sidecar into it with --delete, so it must never be a
// directory anyone else keeps files in.
type Relay struct {
	client  *circleci.Client
	staging string
	status  iostream.StatusFunc
}

// NewRelay creates a relay with a fresh staging directory. Close removes it.
func NewRelay(client *circleci.Client, status iostream.StatusFunc) (*Relay, error) {
	staging, err := os.MkdirTemp("", "chunk-factory-")
	if err != nil {
		return nil, fmt.Errorf("create staging directory: %w", err)
	}
	if status == nil {
		status = func(iostream.Level, string) {}
	}
	return &Relay{client: client, staging: staging, status: status}, nil
}

// Dir is the staging copy: the workspace as of the last Pull.
func (r *Relay) Dir() string {
	return r.staging
}

// Close removes the staging copy.
func (r *Relay) Close() error {
	return os.RemoveAll(r.staging)
}

// Pull mirrors the workspace at repoPath on sidecarID into the staging copy.
func (r *Relay) Pull(ctx context.Context, sidecarID, repoPath string) error {
	if err := sidecar.RsyncPull(ctx, r.client, sidecarID, repoPath, r.staging, r.status); err != nil {
		return fmt.Errorf("pull from %s: %w", sidecarID, err)
	}
	return nil
}

// Push mirrors the staging copy to each entry's workspace, in parallel. It
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
			if err := sidecar.RsyncSyncEphemeral(ctx, r.client, id, repoPath, r.staging, status); err != nil {
				errs[i] = fmt.Errorf("push to %s: %w", id, err)
			}
		}(i, e.ID, e.RepoPath)
	}
	wg.Wait()
	return errors.Join(errs...)
}
