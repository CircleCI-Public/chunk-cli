package factoryplan

import (
	"context"
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
)

// planWorktree is the worktree of store's plan, in the repository at root,
// that its planning session works in, added when it has none. The worktrees
// of plans that have ended are removed first; one that could not be is left
// for the next plan to remove.
func planWorktree(ctx context.Context, root string, store *Store) (string, error) {
	id := store.ID()
	if id == "" {
		return "", errNotCreated
	}
	_ = factory.PrunePlanWorktrees(ctx, root, func(ctx context.Context, planID string) (bool, error) {
		if planID == id {
			return false, nil
		}
		return store.ended(ctx, planID)
	})
	dir, err := factory.OpenPlanWorktree(ctx, root, id)
	if err != nil {
		return "", fmt.Errorf("open the plan's worktree: %w", err)
	}
	return dir, nil
}

// ended is whether factory plan planID is over, so its worktree can go.
func (s *Store) ended(ctx context.Context, planID string) (bool, error) {
	p, err := s.api.GetPlan(ctx, planID)
	if err != nil {
		return false, fmt.Errorf("get the state of plan %s: %w", planID, err)
	}
	return p.Phase == circleci.PhaseEnded, nil
}
