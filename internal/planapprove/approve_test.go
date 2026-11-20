package planapprove

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// posts is the paths of every POST the fake has had.
func posts(cci *fakes.FakeCircleCI) []string {
	var paths []string
	for _, r := range cci.Recorder.AllRequests() {
		if r.Method == http.MethodPost {
			paths = append(paths, r.URL.Path)
		}
	}
	return paths
}

// sendOnly is a SendAPI with no ReleaseClaim, so a Send that tried to
// release a claim would not compile against it.
type sendOnly struct{ client *circleci.Client }

func (s sendOnly) GetFlow(ctx context.Context, planID string) (*circleci.Flow, error) {
	return s.client.GetFlow(ctx, planID)
}

func (s sendOnly) SendFlowInputs(ctx context.Context, planID string, inputs circleci.FlowInputs) error {
	return s.client.SendFlowInputs(ctx, planID, inputs)
}

// TestSend checks that Send refuses a plan that is not ready,
// sends exactly one approve-for-building input otherwise, and releases no claim.
func TestSend(t *testing.T) {
	human := func(events ...string) *fakes.FactoryFlowStatus {
		s := &fakes.FactoryFlowStatus{WaitingOn: "human"}
		for _, e := range events {
			s.Next = append(s.Next, fakes.FactoryTransition{Actor: "human", Event: e, IsRequired: true})
		}
		return s
	}
	for _, tc := range []struct {
		name string
		// flow is the plan's flow status; nil drops the flow, so GET /flows answers 404.
		flow *fakes.FactoryFlowStatus
		// postStatus is what POST /events answers instead of the fake, 0 for the fake's answer.
		postStatus int
		wantErr    error
		wantSent   int
	}{
		{
			name:     "waiting on the daemon",
			flow:     &fakes.FactoryFlowStatus{WaitingOn: "daemon"},
			wantErr:  ErrNotReady,
			wantSent: 0,
		},
		{
			name:     "no flow",
			flow:     nil,
			wantErr:  ErrNotReady,
			wantSent: 0,
		},
		{
			name:     "only the design is required",
			flow:     human("approve-for-units"),
			wantErr:  ErrNoUnits,
			wantSent: 0,
		},
		{
			name:     "no approval is offered",
			flow:     human("answer-open-questions"),
			wantErr:  ErrNoApproval,
			wantSent: 0,
		},
		{
			name: "the approval is served by its input alone",
			flow: &fakes.FactoryFlowStatus{
				WaitingOn: "human",
				Next:      []fakes.FactoryTransition{{Actor: "human", Input: "approve-for-building", IsRequired: true}},
			},
			wantSent: 1,
		},
		{
			name: "request-revision is listed beside it",
			flow: &fakes.FactoryFlowStatus{WaitingOn: "human", Next: []fakes.FactoryTransition{
				{Actor: "human", Event: "approve-for-building", IsRequired: true},
				{Actor: "session", Event: "request-revision"},
				{Actor: "human", Event: "end"},
			}},
			wantSent: 1,
		},
		{
			name:       "the post answers 500",
			flow:       human("approve-for-building"),
			postStatus: http.StatusInternalServerError,
			wantSent:   0,
		},
		{
			name:       "the post answers 409",
			flow:       human("approve-for-building"),
			postStatus: http.StatusConflict,
			wantSent:   0,
		},
		{
			name:     "approve-for-building is required",
			flow:     human("approve-for-building"),
			wantSent: 1,
		},
		{
			name:     "both approvals are required",
			flow:     human("approve-for-units", "approve-for-building"),
			wantSent: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cci := fakes.NewFakeCircleCI()
			var attempts int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.postStatus != 0 && r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/factory/events") {
					attempts++
					w.WriteHeader(tc.postStatus)
					return
				}
				cci.ServeHTTP(w, r)
			}))
			t.Cleanup(srv.Close)
			client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
			assert.NilError(t, err)
			plan, err := client.CreatePlan(context.Background(), "a-plan", "# A plan\n\n## 5. Units\n\n### Unit 1: Add the flag\n")
			assert.NilError(t, err)
			id := plan.ID
			cci.ClaimFactoryPlan(id, "s-1")
			cci.SetFactoryPlanState(id, "refine-proposal")
			if tc.flow == nil {
				cci.DropFactoryFlow(id)
			} else {
				cci.SetFactoryFlowStatus(id, *tc.flow)
			}

			err = Send(context.Background(), sendOnly{client}, id)

			switch {
			case tc.postStatus != 0:
				var se *circleci.StatusError
				isStatus := errors.As(err, &se)
				assert.Assert(t, isStatus, "err = %v", err)
				assert.Check(t, cmp.Equal(se.StatusCode, tc.postStatus))
				assert.Check(t, cmp.ErrorContains(err, "send the approve-for-building event"))
				assert.Check(t, attempts >= 1, "the post was tried")
			case tc.wantErr != nil:
				assert.Check(t, cmp.ErrorIs(err, tc.wantErr))
			default:
				assert.NilError(t, err)
			}
			p, _ := cci.FactoryPlan(id)
			assert.Check(t, cmp.Len(p.Events, tc.wantSent))
			if tc.wantSent == 1 {
				assert.Check(t, cmp.DeepEqual(p.Events, []string{"approve-for-building"}))
				assert.Check(t, cmp.DeepEqual(p.Inputs, []any{true}))
			}
			assert.Check(t, cmp.Equal(p.Session, "s-1"), "the claim is kept")
			for _, path := range posts(cci) {
				assert.Check(t, !strings.HasSuffix(path, "/release"), path)
			}
		})
	}
}
