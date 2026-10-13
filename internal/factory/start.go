package factory

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// Start makes a run's worktree and branch for the developer to work on a
// change in by hand, with an agent of their own or none, and records it so
// 'chunk factory build --continue' can refine what they did there. Nothing
// runs on a sidecar. The worktree starts from the developer's files as they
// are, as a build's does, so the change is whatever they do in it; prompt is
// what the change is for, which the reviewers check the work against.
func Start(ctx context.Context, root, prompt string) (Record, error) {
	dataDir, err := config.RepoDataDir(root)
	if err != nil {
		return Record{}, fmt.Errorf("find chunk's data directory for the repository: %w", err)
	}
	runID := newRunID()
	wt, err := CreateWorktree(ctx, root, filepath.Join(dataDir, "factory", runID), runID, prompt)
	if err != nil {
		return Record{}, fmt.Errorf("create the run's worktree: %w", err)
	}
	rec := Record{
		RunID: runID, Prompt: prompt, ByHand: true,
		Worktree: wt.Path, Branch: wt.Branch, Baseline: wt.Baseline, Head: wt.Head,
	}
	if err := saveRecord(dataDir, rec); err != nil {
		// A worktree no record points at could not be refined, so it goes.
		if rmErr := wt.Remove(ctx, root); rmErr != nil {
			return Record{}, errors.Join(err, fmt.Errorf("remove the worktree %s: %w", wt.Path, rmErr))
		}
		return Record{}, err
	}
	return rec, nil
}
