package plan

import (
	"context"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// FlowText is the status strip's step, now and next, as a line, from the
// screen's last read of the flow.
func TestFlowText(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state, task string
		flow        Flow
		want        string
	}{
		{
			name:  "not started",
			state: "init",
			flow:  Flow{WaitingOn: "daemon"},
			want:  "Not started › Next: waiting on factory",
		},
		{
			name:  "in review",
			state: "refine-units",
			flow: Flow{
				WaitingOn: "session",
				Next:      []Step{{Actor: "session", Task: "review-plan", Dispatched: true}},
			},
			want: "Planning › Now: factory is reviewing the plan › Next: waiting on chunk",
		},
		{
			name:  "building a unit",
			state: "build",
			task:  "build-unit",
			flow: Flow{
				Unit:         "Unit 2: parser",
				WaitingOn:    "session",
				WaitingOnWhy: "chunk is building Unit 2: parser",
				Next:         []Step{{Actor: "session", Task: "build-unit", Dispatched: true}},
			},
			want: "Building › Now: Build unit · Unit 2: parser › Next: chunk is building Unit 2: parser",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, store := planModel(t)
			store.FlowV, store.StateV, store.TaskV = tc.flow, tc.state, tc.task
			m = refreshed(t, m)
			assert.Check(t, cmp.Equal(m.FlowText(), tc.want))
		})
	}

	fresh := New(context.Background(), newFakeAgent(), nil, newFakeStore(), "")
	assert.Check(t, cmp.Equal(fresh.FlowText(), ""))
}

func TestStatus(t *testing.T) {
	human := func(event string, required bool) Step {
		return Step{Actor: "human", Event: event, Required: required}
	}
	for _, tc := range []struct {
		name            string
		flow            Flow
		family          Family
		inReview        bool
		step, now, next string
	}{
		{
			name: "not started, waiting on factory",
			flow: Flow{State: "init", WaitingOn: "daemon"},
			step: "Not started",
			next: "waiting on factory",
		},
		{
			name: "start work",
			flow: Flow{State: "init", WaitingOn: "human", Next: []Step{
				human("prioritize", true), human("abandon", false),
			}},
			step: "Not started",
			next: "you start work · ctrl+s",
		},
		{
			name: "in review",
			flow: Flow{
				State:     "refine-units",
				WaitingOn: "session",
				Next:      []Step{{Actor: "session", Task: "review-plan", Dispatched: true}},
			},
			inReview: true,
			step:     "Planning",
			now:      "factory is reviewing the plan",
			next:     "waiting on chunk",
		},
		{
			name: "waits on you to approve the design",
			flow: Flow{State: "refine-proposal", WaitingOn: "human", Next: []Step{
				human("address-comments", false), human("approve-for-units", true), human("abandon", false),
			}},
			step: "Planning",
			next: "you approve the design · ctrl+s",
		},
		{
			name: "the plan is approved when both are required",
			flow: Flow{State: "refine-proposal", WaitingOn: "human", Next: []Step{
				human("approve-for-units", true), human("approve-for-building", true),
			}},
			step: "Planning",
			next: "you approve the plan · ctrl+s",
		},
		{
			name: "waits on you to approve the plan",
			flow: Flow{State: "refine-units", WaitingOn: "human", Next: []Step{
				human("address-comments", false), human("approve-for-building", true), human("abandon", false),
			}},
			step: "Planning",
			next: "you approve the plan · ctrl+s",
		},
		{
			name: "no approval when nothing is required",
			flow: Flow{State: "refine-proposal", WaitingOn: "human", Next: []Step{
				human("address-comments", false), human("approve-for-units", false),
			}},
			step: "Planning",
			next: "you",
		},
		{
			name: "open questions",
			flow: Flow{State: "refine-proposal", WaitingOn: "human", Next: []Step{
				human("address-comments", false), human("approve-for-units", false),
				human("answer-open-questions", true), human("abandon", false),
			}},
			step: "Planning",
			next: "you answer 3. Open Questions in comments",
		},
		{
			name: "building a unit",
			flow: Flow{
				State:     "build",
				Task:      "build-unit",
				Unit:      "Unit 2: parser",
				WaitingOn: "session",
				Next:      []Step{{Actor: "session", Task: "build-unit", Dispatched: true}},
			},
			step: "Building",
			now:  "Build unit · Unit 2: parser",
			next: "waiting on chunk",
		},
		{
			name: "merge the PR",
			flow: Flow{State: "integrate", WaitingOn: "human", Next: []Step{
				human("address-comments", false),
				{Actor: "human", Action: "merge-pr", Required: true},
				human("abandon-integrate", false),
			}},
			step: "Integrating",
			next: "you merge the PR",
		},
		{
			name: "an unknown human action",
			flow: Flow{State: "integrate", WaitingOn: "human", Next: []Step{
				{Actor: "human", Action: "squash", Required: true},
			}},
			step: "Integrating",
			next: "you squash",
		},
		{
			name: "a human entry not required is not next",
			flow: Flow{State: "build", WaitingOn: "human", Next: []Step{
				human("address-comments", false), human("abandon", false),
			}},
			step: "Building",
			next: "you",
		},
		{
			name: "the developer, with why",
			flow: Flow{State: "build", WaitingOn: "human", WaitingOnWhy: "review the PR", Next: []Step{
				human("abandon", false),
			}},
			step: "Building",
			next: "review the PR",
		},
		{
			name: "a session at work, with why",
			flow: Flow{State: "build", WaitingOn: "session", WaitingOnWhy: "chunk is building Unit 2"},
			step: "Building",
			next: "chunk is building Unit 2",
		},
		{
			name: "a session, no why",
			flow: Flow{State: "build", WaitingOn: "session"},
			step: "Building",
			next: "waiting on chunk",
		},
		{
			name: "the daemon, no why",
			flow: Flow{State: "integrate", WaitingOn: "daemon", Next: []Step{{Actor: "daemon", Task: "pr-state"}}},
			step: "Integrating",
			next: "waiting on factory",
		},
		{
			name: "the daemon, with why",
			flow: Flow{State: "integrate", WaitingOn: "daemon", WaitingOnWhy: "CI is running"},
			step: "Integrating",
			next: "CI is running",
		},
		{
			name: "sub-plans, no why",
			flow: Flow{State: "build", WaitingOn: "sub-plans"},
			step: "Building",
			next: "waiting on sub-plans",
		},
		{
			name:   "blocked",
			flow:   Flow{State: "build", WaitingOn: "blockers"},
			family: Family{Blockers: []Relative{{Title: "retry-config", Active: true}}},
			step:   "Building",
			next:   "Waiting on retry-config",
		},
		{
			name: "waits on the first blocker active",
			flow: Flow{State: "build", WaitingOn: "blockers"},
			family: Family{Blockers: []Relative{
				{Title: "a", State: "ended", Outcome: "merged"},
				{Title: "b", State: "build", Active: true},
			}},
			step: "Building",
			next: "Waiting on b",
		},
		{
			name: "no blocker active, with why",
			flow: Flow{State: "build", WaitingOn: "blockers", WaitingOnWhy: "a blocker is integrating"},
			family: Family{Blockers: []Relative{
				{Title: "a", State: "integrate"},
			}},
			step: "Building",
			next: "a blocker is integrating",
		},
		{
			name: "blockers not loaded",
			flow: Flow{State: "build", WaitingOn: "blockers"},
			step: "Building",
			next: "waiting on blockers",
		},
		{
			name:   "a blocker active, waiting on someone else",
			flow:   Flow{State: "build", WaitingOn: "session"},
			family: Family{Blockers: []Relative{{Title: "a", Active: true}}},
			step:   "Building",
			next:   "waiting on chunk",
		},
		{
			name: "an unknown actor",
			flow: Flow{State: "build", WaitingOn: "robot"},
			step: "Building",
		},
		{
			name: "waiting on nobody",
			flow: Flow{State: "ended", Ended: true, Outcome: "merged"},
			step: "Done · merged",
		},
		{
			name: "ended canceled",
			flow: Flow{State: "ended", Ended: true, Outcome: "canceled"},
			step: "Done · canceled",
		},
		{
			name:   "ended with a blocker",
			flow:   Flow{State: "ended", Ended: true, Outcome: "merged"},
			family: Family{Blockers: []Relative{{Title: "a", State: "build", Active: true}}},
			step:   "Done · merged",
		},
		{
			name: "an ended plan with no state",
			flow: Flow{Ended: true},
			step: "Done",
		},
		{
			name: "ready reads Planning",
			flow: Flow{State: "ready", WaitingOn: "session"},
			step: "Planning",
			next: "waiting on chunk",
		},
		{
			name: "stand-by",
			flow: Flow{State: "stand-by", WaitingOn: "human"},
			step: "Standing by",
			next: "you",
		},
		{
			name: "an old name is shown as given",
			flow: Flow{State: "Planning - Human"},
			step: "Planning - Human",
		},
		{
			name: "unknown state",
			flow: Flow{State: "Paused\x1b[31m now\n"},
			step: "Paused now",
		},
		{
			name: "why is cleaned",
			flow: Flow{State: "build", WaitingOn: "daemon", WaitingOnWhy: "Paused\x1b[31m now\n"},
			step: "Building",
			next: "Paused now",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			step, now, next := status(tc.flow, tc.family, tc.inReview)
			assert.Check(t, cmp.Equal(step, tc.step))
			assert.Check(t, cmp.Equal(now, tc.now))
			assert.Check(t, cmp.Equal(next, tc.next))
		})
	}
}
