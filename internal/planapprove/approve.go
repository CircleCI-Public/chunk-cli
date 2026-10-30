// Package planapprove approves a factory plan for building: it checks the
// plan's review has passed and its units are written, and frees its claim
// from a session that is only planning it.
package planapprove

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
)

// API is what approving a plan needs of factory: *circleci.Client.
type API interface {
	GetFlow(ctx context.Context, planID string) (*circleci.Flow, error)
	PlanText(ctx context.Context, id string) (string, error)
	ReleaseClaim(ctx context.Context, sessionID, planID string) error
	CreateFlowEvent(ctx context.Context, planID string, event circleci.FlowEvent) error
}

// Locate is the worktree root factory's daemon has session working in:
// factory.SessionWorktree.
type Locate func(ctx context.Context, session string) (string, error)

// ErrNotReady is returned by Approve for a plan whose review has not passed:
// factory has not made it planstate.ReadyToApprove.
var ErrNotReady = errors.New("the plan is not ready yet: wait for its review to pass")

// ErrBeingBuilt is returned by Approve for a plan a session that may be building it holds.
var ErrBeingBuilt = errors.New("the plan is already being built")

// ErrNoUnits is returned by Approve for a planstate.ReadyToApprove plan
// whose design, not the plan, is what waits for approval, as
// planstate.StageOf decides.
var ErrNoUnits = errors.New("the plan has no units yet: approve its design with ctrl+s, so chunk writes them")

// ErrQuestionsOpen is returned by Approve for a plan whose flow offers
// neither approval, as planstate.StageOf decides: its 3. Open Questions
// wait for answers in comments, sent before either is approved.
var ErrQuestionsOpen = errors.New("the plan has open questions: answer 3. Open Questions in comments, then send them")

// Approve approves factory plan planID for building once its review has
// passed, first releasing a claim held by session, the plan's planning
// session, or by one locate places outside worktree. It refuses a claim
// held from worktree itself, or by a session factory's daemon does not know,
// as either may be building the plan. The flow, text and claim are read
// from factory, so a plan that moved after a screen last polled is still
// refused.
func Approve(ctx context.Context, api API, locate Locate, planID, session, worktree string) error {
	f, err := api.GetFlow(ctx, planID)
	if isNotFound(err) {
		return ErrNotReady
	}
	if err != nil {
		return fmt.Errorf("get the plan's flow: %w", err)
	}
	next := make([]planstate.Move, len(f.Next))
	for i, t := range f.Next {
		next[i] = planstate.Move{Actor: t.Actor, Event: string(t.Event)}
	}
	if !planstate.ReadyToApprove(f.State, next) {
		return ErrNotReady
	}
	text, err := api.PlanText(ctx, planID)
	if err != nil {
		return fmt.Errorf("get the plan's text: %w", err)
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("the plan in factory is empty")
	}
	events := make([]string, len(f.ApplicableEvents))
	for i, e := range f.ApplicableEvents {
		events[i] = string(e)
	}
	switch planstate.StageOf(events, len(planstate.Units(text))) {
	case planstate.StageDesign:
		return ErrNoUnits
	case planstate.StageQuestions:
		return ErrQuestionsOpen
	case planstate.StagePlan:
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
		if err := api.ReleaseClaim(ctx, session, planID); err != nil {
			return fmt.Errorf("release the planning session's claim: %w", err)
		}
		return nil
	}
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
	if err := api.ReleaseClaim(ctx, holder, planID); err != nil {
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
