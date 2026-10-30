package planstate

import (
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestParse(t *testing.T) {
	cases := []struct {
		state string
		phase string
		actor Actor
	}{
		{
			state: "Planning - Human",
			phase: "Planning",
			actor: Human,
		},
		{
			state: "Building - Agent",
			phase: "Building",
			actor: Agent,
		},
		{
			state: " integrating - agent ",
			phase: "integrating",
			actor: Agent,
		},
		{
			state: "Review - HUMAN",
			phase: "Review",
			actor: Human,
		},
		{
			state: "Unprioritized",
			phase: "Unprioritized",
			actor: Unknown,
		},
		{
			state: "Blocked",
			phase: "Blocked",
			actor: Unknown,
		},
		{
			state: "Integrating - Bot",
			phase: "Integrating - Bot",
			actor: Unknown,
		},
		{
			state: "Waiting - On - Human",
			phase: "Waiting - On",
			actor: Human,
		},
		{
			state: "",
			phase: "",
			actor: Unknown,
		},
		{
			state: " - Human",
			phase: "- Human",
			actor: Unknown,
		},
		{
			state: "refine-units",
			phase: "Planning",
			actor: Unknown,
		},
		{
			state: "Refine-Proposal",
			phase: "Planning",
			actor: Unknown,
		},
		{
			state: " build ",
			phase: "Building",
			actor: Unknown,
		},
		{
			state: "integrate",
			phase: "Integrating",
			actor: Unknown,
		},
		{
			state: "ready",
			phase: "Agent Ready",
			actor: Unknown,
		},
		{
			state: "init",
			phase: "Unprioritized",
			actor: Unknown,
		},
		{
			state: "stand-by",
			phase: "standing-by",
			actor: Unknown,
		},
		{
			state: "ended",
			phase: "Done",
			actor: Unknown,
		},
		{
			state: "build - Human",
			phase: "build",
			actor: Human,
		},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			phase, actor := Parse(c.state)
			assert.Check(t, cmp.Equal(phase, c.phase))
			assert.Check(t, cmp.Equal(actor, c.actor))
		})
	}
}

func TestIsUnprioritized(t *testing.T) {
	assert.Check(t, IsUnprioritized("Unprioritized"))
	assert.Check(t, IsUnprioritized(" unprioritized "))
	assert.Check(t, IsUnprioritized("init"))
	assert.Check(t, IsUnprioritized(" Init "))
	assert.Check(t, !IsUnprioritized("init - Human"))
	assert.Check(t, !IsUnprioritized("Unprioritized - Human"))
	assert.Check(t, !IsUnprioritized(""))
}

func TestStageOf(t *testing.T) {
	both := []string{"approve-for-units", "approve-for-building"}
	cases := []struct {
		name   string
		events []string
		units  int
		want   Stage
	}{
		{
			name: "no events, no units",
			want: StageDesign,
		},
		{
			name:  "no events, units",
			units: 2,
			want:  StagePlan,
		},
		{
			name:   "both, no units",
			events: both,
			want:   StageDesign,
		},
		{
			name:   "both, units",
			events: both,
			units:  2,
			want:   StagePlan,
		},
		{
			name:   "only units, no units",
			events: []string{"approve-for-units"},
			want:   StageDesign,
		},
		{
			name:   "only units, units",
			events: []string{"address-comments", "approve-for-units"},
			units:  2,
			want:   StageDesign,
		},
		{
			name:   "only building, no units",
			events: []string{"address-comments", "approve-for-building"},
			want:   StagePlan,
		},
		{
			name:   "only building, units",
			events: []string{"approve-for-building"},
			units:  2,
			want:   StagePlan,
		},
		{
			name:   "unrelated, no units",
			events: []string{"address-comments", "abandon"},
			want:   StageQuestions,
		},
		{
			name:   "unrelated, units",
			events: []string{"address-comments", "abandon"},
			units:  2,
			want:   StageQuestions,
		},
		{
			name:   "empty, no units",
			events: []string{},
			want:   StageQuestions,
		},
		{
			name:   "empty, units",
			events: []string{},
			units:  2,
			want:   StageQuestions,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.Equal(StageOf(tc.events, tc.units), tc.want))
		})
	}
}
