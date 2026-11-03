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
	}{
		{state: "Planning - Human", phase: "Planning"},
		{state: "Building - Agent", phase: "Building"},
		{state: " integrating - agent ", phase: "integrating"},
		{state: "Review - HUMAN", phase: "Review"},
		{state: "Unprioritized", phase: "Unprioritized"},
		{state: "Blocked", phase: "Blocked"},
		{state: "Integrating - Bot", phase: "Integrating - Bot"},
		{state: "Waiting - On - Human", phase: "Waiting - On"},
		{state: "", phase: ""},
		{state: " - Human", phase: "- Human"},
		{state: "refine-units", phase: "Planning"},
		{state: "Refine-Proposal", phase: "Planning"},
		{state: " build ", phase: "Building"},
		{state: "integrate", phase: "Integrating"},
		{state: "ready", phase: "Agent Ready"},
		{state: "init", phase: "Unprioritized"},
		{state: "stand-by", phase: "standing-by"},
		{state: "ended", phase: "Done"},
		{state: "build - Human", phase: "build"},
	}
	for _, c := range cases {
		t.Run(c.state, func(t *testing.T) {
			assert.Check(t, cmp.Equal(Parse(c.state), c.phase))
		})
	}
}

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
				{Actor: Human, Event: "abandon"},
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

func TestAgentActs(t *testing.T) {
	rr := Move{Actor: "session", Event: RequestRevision}
	example := []Move{
		{Actor: "human", Event: "address-comments"},
		{Actor: "human", Event: "approve-for-building"},
		rr,
		{Actor: "human", Event: "abandon"},
	}
	for _, tc := range []struct {
		name                                  string
		state                                 string
		next                                  []Move
		agent, session, human, readyToApprove bool
	}{
		{
			name:           "request-revision while the plan waits on you",
			state:          "Planning - Human",
			next:           example,
			human:          true,
			readyToApprove: true,
		},
		{
			name:    "a session task beside request-revision",
			state:   "Building - Agent",
			next:    []Move{{Actor: "session"}, rr, {Actor: "human", Event: "abandon"}},
			agent:   true,
			session: true,
		},
		{
			name:  "a daemon task beside request-revision",
			state: "Planning - Agent",
			next:  []Move{{Actor: "daemon"}, rr},
			agent: true,
		},
		{
			name:  "only request-revision and abandon",
			state: "Planning - Human",
			next:  []Move{rr, {Actor: "human", Event: "abandon"}},
		},
		{
			name:           "request-revision with spaces",
			state:          "Planning - Human",
			next:           []Move{{Actor: " session ", Event: " request-revision "}, {Actor: "human", Event: "approve-for-building"}},
			human:          true,
			readyToApprove: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.Equal(AgentActs(tc.next), tc.agent))
			assert.Check(t, cmp.Equal(SessionActs(tc.next), tc.session))
			assert.Check(t, cmp.Equal(WaitsOnHuman(tc.next), tc.human))
			assert.Check(t, cmp.Equal(ReadyToApprove(tc.state, tc.next), tc.readyToApprove))
		})
	}
}
