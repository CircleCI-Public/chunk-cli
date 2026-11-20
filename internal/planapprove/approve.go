// Package planapprove approves a factory plan for building: it checks
// factory waits on the developer to approve the plan, then sends the
// approval.
package planapprove

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/planstate"
)

// ErrNotReady is returned by Send for a plan whose flow does not wait on
// the developer, such as one whose review has not passed, or that has ended.
var ErrNotReady = errors.New("the plan is not ready yet: wait for its review to pass")

// ErrNoUnits is returned by Send for a plan whose flow requires
// approve-for-units but not approve-for-building, as planstate.Approval
// decides: its design, not the plan, is what waits for approval.
var ErrNoUnits = errors.New("the plan has no units yet: approve its design with ctrl+s, so chunk writes them")

// ErrNoApproval is returned by Send for a plan whose flow offers no
// approval, as planstate.Approval decides: another step, such as answering
// 3. Open Questions, comes first.
var ErrNoApproval = errors.New("factory offers no approval yet: " +
	"do what the plan screen's Next says first")

// SendAPI is what Send needs of factory: *circleci.Client.
type SendAPI interface {
	GetFlow(ctx context.Context, planID string) (*circleci.Flow, error)
	SendFlowInputs(ctx context.Context, planID string, inputs circleci.FlowInputs) error
}

// Send approves factory plan planID for building when its flow waits on the
// developer and factory requires approve-for-building, refusing with
// ErrNotReady, ErrNoUnits or ErrNoApproval otherwise. It releases no claim:
// the worker holding the plan builds it.
func Send(ctx context.Context, api SendAPI, planID string) error {
	if _, err := ready(ctx, api, planID); err != nil {
		return err
	}
	if err := api.SendFlowInputs(ctx, planID, circleci.BoolInput(circleci.ApproveForBuilding)); err != nil {
		return fmt.Errorf("send the approve-for-building event: %w", err)
	}
	return nil
}

// ready reads plan planID's flow fresh from factory and returns it when it
// waits on the developer and requires approve-for-building, or ErrNotReady,
// ErrNoUnits or ErrNoApproval as Send refuses.
func ready(ctx context.Context, api SendAPI, planID string) (*circleci.Flow, error) {
	f, err := api.GetFlow(ctx, planID)
	if isNotFound(err) {
		return nil, ErrNotReady
	}
	if err != nil {
		return nil, fmt.Errorf("get the plan's flow: %w", err)
	}
	if f.WaitingOn != planstate.Human {
		return nil, ErrNotReady
	}
	next := make([]planstate.Move, len(f.Next))
	for i, t := range f.Next {
		next[i] = planstate.Move{Actor: t.Actor, Event: string(t.Event), Required: t.IsRequired}
	}
	switch planstate.Approval(next) {
	case planstate.ApproveForUnits:
		return nil, ErrNoUnits
	case "":
		return nil, ErrNoApproval
	case planstate.ApproveForBuilding:
		// The plan is what waits for approval.
	}
	return f, nil
}

// isNotFound is whether err is factory answering 404.
func isNotFound(err error) bool {
	var se *circleci.StatusError
	return errors.As(err, &se) && se.StatusCode == http.StatusNotFound
}
