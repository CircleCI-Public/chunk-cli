package factoryplan

import (
	"context"
	"errors"
	"fmt"
	"net/http"

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
// A plan with no flow is over only once it is gone: one that is still there
// is a live plan made before plans had flows.
func (s *Store) ended(ctx context.Context, planID string) (bool, error) {
	f, err := s.api.GetFlow(ctx, planID)
	if isNotFound(err) {
		_, err = s.api.GetPlan(ctx, planID)
		if isNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, fmt.Errorf("get plan %s: %w", planID, err)
		}
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("get the state of plan %s: %w", planID, err)
	}
	return f.Phase == circleci.PhaseEnded, nil
}

// isNotFound is whether err is factory answering 404.
func isNotFound(err error) bool {
	var se *circleci.StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}
