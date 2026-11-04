// Package planapprove approves a factory plan for building: it checks
// factory waits on the developer to approve the plan, and frees its claim
// from a session that is only planning it.
package planapprove

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
)

// API is what approving a plan needs of factory: *circleci.Client.
type API interface {
	GetFlow(ctx context.Context, planID string) (*circleci.Flow, error)
	ReleaseClaim(ctx context.Context, planID string) error
	CreateFlowEvent(ctx context.Context, planID string, event circleci.FlowEvent) error
}

// Locate is the worktree root factory's daemon has session working in:
// factory.SessionWorktree.
type Locate func(ctx context.Context, session string) (string, error)

// ErrNotReady is returned by Approve for a plan whose flow does not wait on
// the developer, such as one whose review has not passed, or that has ended.
var ErrNotReady = errors.New("the plan is not ready yet: wait for its review to pass")

// ErrBeingBuilt is returned by Approve for a plan a session that may be building it holds.
var ErrBeingBuilt = errors.New("the plan is already being built")

// ErrNoUnits is returned by Approve for a plan whose flow requires
// approve-for-units but not approve-for-building, as planstate.Approval
// decides: its design, not the plan, is what waits for approval.
var ErrNoUnits = errors.New("the plan has no units yet: approve its design with ctrl+s, so chunk writes them")

// ErrNoApproval is returned by Approve for a plan whose flow offers no
// approval, as planstate.Approval decides: another step, such as answering
// 3. Open Questions, comes first.
var ErrNoApproval = errors.New("factory offers no approval yet: " +
	"do what the plan screen's Next says first")

// Approve approves factory plan planID for building when its flow waits on
// the developer and factory requires approve-for-building, beside
// approve-for-units or not, first releasing a claim held by session, the
// plan's planning session, or by one locate places outside worktree. It
// refuses a claim held from worktree itself, or by a session factory's
// daemon does not know, as either may be building the plan. The flow and
// its claim are read from factory, so a plan that moved after a screen last
// polled is still refused.
func Approve(ctx context.Context, api API, locate Locate, planID, session, worktree string) error {
	f, err := api.GetFlow(ctx, planID)
	if isNotFound(err) {
		return ErrNotReady
	}
	if err != nil {
		return fmt.Errorf("get the plan's flow: %w", err)
	}
	if f.WaitingOn != planstate.Human {
		return ErrNotReady
	}
	next := make([]planstate.Move, len(f.Next))
	for i, t := range f.Next {
		next[i] = planstate.Move{Actor: t.Actor, Event: string(t.Event), Required: t.IsRequired}
	}
	switch planstate.Approval(next) {
	case planstate.ApproveForUnits:
		return ErrNoUnits
	case "":
		return ErrNoApproval
	case planstate.ApproveForBuilding:
		// The plan is what waits for approval: send it below.
	}
	if f.SessionID != "" {
		if err := release(ctx, api, locate, planID, f.SessionID, session, worktree); err != nil {
			return err
		}
	}
	if err := api.CreateFlowEvent(ctx, planID, circleci.ApproveForBuilding); err != nil {
		return fmt.Errorf("send the approve-for-building event: %w", err)
	}
	return nil
}

// release releases holder's claim on plan planID, or refuses, as Approve
// describes.
func release(ctx context.Context, api API, locate Locate, planID, holder, session, worktree string) error {
	if holder == session {
		if err := api.ReleaseClaim(ctx, planID); err != nil {
			return fmt.Errorf("release the planning session's claim: %w", err)
		}
		return nil
	}
	return ReleaseForeign(ctx, api, locate, planID, holder, worktree)
}

// ReleaseForeign releases holder's claim on planID unless factory's daemon
// does not know holder or holder works in worktree (ErrBeingBuilt).
func ReleaseForeign(ctx context.Context, api API, locate Locate, planID, holder, worktree string) error {
	dir, err := locate(ctx, holder)
	if errors.Is(err, factory.ErrUnknownSession) {
		return fmt.Errorf("%w: plan %s is held by session %s, which factory's daemon does not know; release it with factory daemon release %[2]s", ErrBeingBuilt, planID, holder)
	}
	if err != nil {
		return fmt.Errorf("find where session %s works: %w", holder, err)
	}
	if samePath(dir, worktree) {
		return fmt.Errorf("%w: plan %s is already being built by session %s; stop it, or release it with factory daemon release %[2]s", ErrBeingBuilt, planID, holder)
	}
	if err := api.ReleaseClaim(ctx, planID); err != nil {
		return fmt.Errorf("release session %s's claim: %w", holder, err)
	}
	return nil
}

// samePath reports whether a and b name the same directory, compared with
// symlinks resolved, as macOS temp dirs differ by /private. A path that
// cannot be resolved, such as one not made yet, is compared as spelled.
func samePath(a, b string) bool {
	return resolved(a) == resolved(b)
}

func resolved(p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return r
}

// isNotFound is whether err is factory answering 404.
func isNotFound(err error) bool {
	var se *circleci.StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}
