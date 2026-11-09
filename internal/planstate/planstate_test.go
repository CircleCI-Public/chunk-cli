package planstate

import (
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestApproval(t *testing.T) {
	for _, tc := range []struct {
		name string
		next []Move
		want string
	}{
		{
			name: "units required",
			next: []Move{
				{Actor: Human, Event: ApproveForUnits, Required: true},
				{Actor: Human, Event: ApproveForBuilding},
			},
			want: ApproveForUnits,
		},
		{
			name: "both required, building listed first",
			next: []Move{
				{Actor: Human, Event: ApproveForBuilding, Required: true},
				{Actor: Human, Event: ApproveForUnits, Required: true},
			},
			want: ApproveForBuilding,
		},
		{
			name: "both required, units listed first",
			next: []Move{
				{Actor: Human, Event: ApproveForUnits, Required: true},
				{Actor: Human, Event: ApproveForBuilding, Required: true},
			},
			want: ApproveForBuilding,
		},
		{
			name: "building required",
			next: []Move{
				{Actor: Human, Event: ApproveForBuilding, Required: true},
				{Actor: Human, Event: "request-revision"},
				{Actor: Human, Event: "end"},
			},
			want: ApproveForBuilding,
		},
		{
			name: "none required, nothing else required",
			next: []Move{
				{Actor: Human, Event: ApproveForUnits},
				{Actor: Human, Event: ApproveForBuilding},
			},
		},
		{
			name: "open questions",
			next: []Move{
				{Actor: Human, Event: "answer-open-questions", Required: true},
				{Actor: Human, Event: ApproveForUnits},
				{Actor: Human, Event: ApproveForBuilding},
			},
		},
		{
			name: "prioritize required",
			next: []Move{
				{Actor: Human, Event: "prioritize", Required: true},
			},
		},
		{
			name: "only building, unrequired",
			next: []Move{
				{Actor: Human, Event: ApproveForBuilding},
			},
		},
		{
			name: "a session's entry is not the developer's",
			next: []Move{
				{Actor: Session, Event: ApproveForUnits, Required: true},
			},
		},
		{
			name: "nothing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.Equal(Approval(tc.next), tc.want))
		})
	}
}

func TestStep(t *testing.T) {
	for _, tc := range []struct {
		state string
		want  string
	}{
		{
			state: Init,
			want:  "Not started",
		},
		{
			state: Ready,
			want:  "Planning",
		},
		{
			state: RefineProposal,
			want:  "Planning",
		},
		{
			state: RefineUnits,
			want:  "Planning",
		},
		{
			state: RecordSubPlans,
			want:  "Standing by",
		},
		{
			state: StandBy,
			want:  "Standing by",
		},
		{
			state: Build,
			want:  "Building",
		},
		{
			state: Integrate,
			want:  "Integrating",
		},
		{
			state: Ended,
			want:  "Done",
		},
		{
			state: "Planning - Human",
			want:  "Planning - Human",
		},
		{
			state: "",
			want:  "",
		},
	} {
		t.Run(tc.state, func(t *testing.T) {
			assert.Check(t, cmp.Equal(Step(tc.state), tc.want))
		})
	}
}
