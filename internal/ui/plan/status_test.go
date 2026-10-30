package plan

import (
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestStatus(t *testing.T) {
	for _, tc := range []struct {
		name            string
		flow            Flow
		family          Family
		stage           Stage
		inReview        bool
		step, now, next string
	}{
		{
			name: "not started",
			flow: Flow{State: "Unprioritized"},
			step: "Not started",
		},
		{
			name:     "in review",
			flow:     Flow{State: "Planning - Agent"},
			inReview: true,
			step:     "Planning",
			now:      "factory is reviewing the plan",
		},
		{
			name:  "design waits",
			flow:  Flow{State: "Planning - Human"},
			stage: StageDesign,
			step:  "Planning",
			next:  "you approve the design · ctrl+s",
		},
		{
			name:  "plan waits",
			flow:  Flow{State: "Planning - Human"},
			stage: StagePlan,
			step:  "Planning",
			next:  "you approve the plan · ctrl+s",
		},
		{
			name:  "questions",
			flow:  Flow{State: "Planning - Human"},
			stage: StageQuestions,
			step:  "Planning",
			next:  "you answer 3. Open Questions in comments",
		},
		{
			name: "building a unit",
			flow: Flow{
				State: "Building - Agent",
				Task:  "build-unit",
				Unit:  "Unit 2: parser",
				Next:  []Step{{Actor: "session", Task: "build-unit", Dispatched: true}},
			},
			step: "Building",
			now:  "Build unit · Unit 2: parser",
			next: "chunk: build-unit",
		},
		{
			name: "conflicts",
			flow: Flow{
				State: "Integrating - Human",
				Next:  []Step{{Actor: "human", Event: "resolve-conflicts"}},
			},
			step: "Integrating",
			next: "you resolve conflicts · ctrl+s",
		},
		{
			name: "merge",
			flow: Flow{
				State: "Integrating - Human",
				Next:  []Step{{Actor: "human", Action: "merge-pr"}},
			},
			step: "Integrating",
			next: "you merge the PR",
		},
		{
			name: "daemon task",
			flow: Flow{
				State: "Integrating - Agent",
				Next:  []Step{{Actor: "daemon", Task: "pr-state"}},
			},
			step: "Integrating",
			next: "factory: pr-state",
		},
		{
			name: "unknown actor and event",
			flow: Flow{
				State: "Building - Agent",
				Next:  []Step{{Actor: "robot", Event: "poke"}},
			},
			step: "Building",
			next: "robot: poke",
		},
		{
			name: "transition with nothing set",
			flow: Flow{
				State: "Building - Agent",
				Next:  []Step{{Actor: "session"}},
			},
			step: "Building",
			next: "chunk",
		},
		{
			name: "start work",
			flow: Flow{
				State: "Unprioritized",
				Next: []Step{
					{Actor: "human", Event: "prioritize"},
					{Actor: "human", Event: "abandon"},
				},
			},
			step: "Not started",
			next: "you start work · ctrl+s",
		},
		{
			name: "stuck with no blockers",
			flow: Flow{State: "Building - Agent"},
			step: "Building",
		},
		{
			name: "merged",
			flow: Flow{
				State:   "Done",
				Ended:   true,
				Outcome: "merged",
			},
			step: "Done · merged",
		},
		{
			name: "canceled",
			flow: Flow{
				State:   "Canceled",
				Ended:   true,
				Outcome: "canceled",
			},
			step: "Done · canceled",
		},
		{
			name: "ended canceled",
			flow: Flow{
				State:   "ended",
				Ended:   true,
				Outcome: "canceled",
			},
			step: "Done · canceled",
		},
		{
			name: "new name for a phase",
			flow: Flow{
				State: "build",
				Next:  []Step{{Actor: "session", Task: "build-unit", Dispatched: true}},
			},
			step: "Building",
			next: "chunk: build-unit",
		},
		{
			name: "unknown state",
			flow: Flow{State: "Paused\x1b[31m now\n"},
			step: "Paused now",
		},
		{
			name: "address comments",
			flow: Flow{
				State: "Building - Human",
				Next:  []Step{{Actor: "human", Event: "address-comments"}},
			},
			step: "Building",
			next: "you send your comments",
		},
		{
			name: "building waits on open questions",
			flow: Flow{
				State: "Building - Human",
				Next: []Step{
					{Actor: "human", Event: "answer-open-questions"},
					{Actor: "human", Event: "abandon"},
				},
			},
			step: "Building",
			next: "you answer 3. Open Questions in comments",
		},
		{
			name: "integrating waits on open questions",
			flow: Flow{
				State: "Integrating - Human",
				Next: []Step{
					{Actor: "human", Event: "answer-open-questions"},
					{Actor: "human", Event: "abandon-integrate"},
					{Actor: "human", Event: "abandon"},
				},
			},
			step: "Integrating",
			next: "you answer 3. Open Questions in comments",
		},
		{
			name: "blockers wait only while next is empty",
			flow: Flow{
				State: "Planning - Human",
				Next:  []Step{{Actor: "human", Event: "approve-for-building"}},
			},
			family: Family{Blockers: []Relative{{Title: "retry-config"}}},
			stage:  StagePlan,
			step:   "Planning",
			next:   "you approve the plan · ctrl+s",
		},
		{
			name:   "blocked",
			flow:   Flow{State: "Building - Agent"},
			family: Family{Blockers: []Relative{{Title: "retry-config"}}},
			step:   "Building",
			next:   "Waiting on retry-config",
		},
		{
			name: "waits on the first blocker not done",
			flow: Flow{State: "Building - Agent"},
			family: Family{Blockers: []Relative{
				{Title: "a", State: "Done"},
				{Title: "b", State: "Building - Agent"},
			}},
			step: "Building",
			next: "Waiting on b",
		},
		{
			name: "every blocker done",
			flow: Flow{State: "Building - Agent"},
			family: Family{Blockers: []Relative{
				{Title: "a", State: "Done"},
				{Title: "b", State: " Done "},
			}},
			step: "Building",
		},
		{
			name: "a blocker ended merged is done",
			flow: Flow{State: "build"},
			family: Family{Blockers: []Relative{
				{Title: "a", State: "ended", Outcome: "merged"},
			}},
			step: "Building",
		},
		{
			name: "a blocker ended abandoned still blocks",
			flow: Flow{State: "build"},
			family: Family{Blockers: []Relative{
				{Title: "a", State: "Done"},
				{Title: "b", State: "ended", Outcome: "canceled"},
			}},
			step: "Building",
			next: "Waiting on b",
		},
		{
			name: "an old abandoned blocker still blocks",
			flow: Flow{State: "Building - Agent"},
			family: Family{Blockers: []Relative{
				{Title: "a", State: "Canceled", Outcome: "canceled"},
			}},
			step: "Building",
			next: "Waiting on a",
		},
		{
			name: "ended with a blocker",
			flow: Flow{
				State:   "Done",
				Ended:   true,
				Outcome: "merged",
			},
			family: Family{Blockers: []Relative{{Title: "a", State: "Building - Agent"}}},
			step:   "Done · merged",
		},
		{
			name: "conflicts outrank merge",
			flow: Flow{
				State: "Integrating - Human",
				Next: []Step{
					{Actor: "human", Action: "merge-pr"},
					{Actor: "human", Event: "address-comments"},
					{Actor: "human", Event: "resolve-conflicts"},
					{Actor: "human", Event: "abandon-integrate"},
					{Actor: "human", Event: "abandon"},
				},
			},
			step: "Integrating",
			next: "you resolve conflicts · ctrl+s",
		},
		{
			name: "merge before comments",
			flow: Flow{
				State: "Integrating - Human",
				Next: []Step{
					{Actor: "human", Action: "merge-pr"},
					{Actor: "human", Event: "address-comments"},
					{Actor: "human", Event: "abandon-integrate"},
					{Actor: "human", Event: "abandon"},
				},
			},
			step: "Integrating",
			next: "you merge the PR",
		},
		{
			name: "escape hatches are not next",
			flow: Flow{
				State: "Integrating - Agent",
				Next: []Step{
					{Actor: "human", Event: "abandon-integrate"},
					{Actor: "human", Event: "abandon"},
				},
			},
			step: "Integrating",
		},
		{
			name: "comments before abandon",
			flow: Flow{
				State: "Building - Human",
				Next: []Step{
					{Actor: "human", Event: "address-comments"},
					{Actor: "human", Event: "abandon"},
				},
			},
			step: "Building",
			next: "you send your comments",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step, now, next := status(tc.flow, tc.family, tc.stage, tc.inReview)
			assert.Check(t, cmp.Equal(step, tc.step))
			assert.Check(t, cmp.Equal(now, tc.now))
			assert.Check(t, cmp.Equal(next, tc.next))
		})
	}
}
