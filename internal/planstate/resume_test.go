package planstate

import (
	"slices"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestResumeOf(t *testing.T) {
	session := []Move{{Actor: "session"}}
	rr := Move{Actor: "session", Event: RequestRevision}
	done := []Blocker{{Title: "Add retry config", State: "Done"}}
	const build = "No session holds this plan · R starts a build session"
	const planner = "No session holds this plan · R resumes its planner"
	for _, tc := range []struct {
		name string
		f    ResumeFacts
		kind ResumeKind
		line string
	}{
		{
			name: "building, unclaimed",
			f: ResumeFacts{
				PlanID:        "p-42",
				State:         "Building - Agent",
				Next:          session,
				BlockersKnown: true,
			},
			kind: ResumeBuild,
			line: build,
		},
		{
			name: "claimed",
			f: ResumeFacts{
				PlanID:        "p-42",
				State:         "Building - Agent",
				Holder:        "bb5a623b-8dd3-4ae8-9c91-48b4eaaf6d62",
				Next:          session,
				BlockersKnown: true,
			},
			line: "Claimed by session bb5a623b · factory daemon release p-42 frees it",
		},
		{
			name: "blocked",
			f: ResumeFacts{
				State:         "Building - Agent",
				Next:          session,
				Blockers:      append(done, Blocker{Title: "Fix uploads", State: "Building - Agent"}),
				BlockersKnown: true,
			},
			line: "Waiting on Fix uploads",
		},
		{
			name: "integrating",
			f: ResumeFacts{
				State:         "Integrating - Agent",
				Next:          session,
				BlockersKnown: true,
			},
			kind: ResumeBuild,
			line: build,
		},
		{
			name: "new name build",
			f: ResumeFacts{
				State:         "build",
				Next:          session,
				BlockersKnown: true,
			},
			kind: ResumeBuild,
			line: build,
		},
		{
			name: "planning",
			f: ResumeFacts{
				State:         "Planning - Agent",
				Next:          session,
				BlockersKnown: true,
			},
			kind: ResumePlanner,
			line: planner,
		},
		{
			name: "unprioritized",
			f: ResumeFacts{
				State:         "Unprioritized",
				Next:          session,
				BlockersKnown: true,
			},
			kind: ResumePlanner,
			line: planner,
		},
		{
			name: "standing-by",
			f: ResumeFacts{
				State:         "standing-by",
				Next:          session,
				BlockersKnown: true,
			},
			kind: ResumePlanner,
			line: planner,
		},
		{
			name: "every blocker done",
			f: ResumeFacts{
				State:         "Building - Agent",
				Next:          session,
				Blockers:      done,
				BlockersKnown: true,
			},
			kind: ResumeBuild,
			line: build,
		},
		{
			name: "a blocker merged",
			f: ResumeFacts{
				State:         "Building - Agent",
				Next:          session,
				Blockers:      []Blocker{{Title: "a", State: "ended", Outcome: "merged"}},
				BlockersKnown: true,
			},
			kind: ResumeBuild,
			line: build,
		},
		{
			name: "no flow",
			f: ResumeFacts{
				PlanID: "p-1",
			},
			line: "No flow: factory hands this plan no tasks",
		},
		{
			name: "ended",
			f: ResumeFacts{
				State:         "Done",
				Ended:         true,
				Next:          session,
				BlockersKnown: true,
			},
			line: "",
		},
		{
			name: "claimed by a short ID",
			f: ResumeFacts{
				PlanID: "p-42",
				State:  "Building - Agent",
				Holder: "s-1",
				Next:   session,
			},
			line: "Claimed by session s-1 · factory daemon release p-42 frees it",
		},
		{
			name: "a canceled blocker still blocks",
			f: ResumeFacts{
				State:         "Building - Agent",
				Next:          session,
				Blockers:      []Blocker{{Title: "a", State: "ended", Outcome: "canceled"}},
				BlockersKnown: true,
			},
			line: "Waiting on a",
		},
		{
			name: "blockers not loaded",
			f: ResumeFacts{
				State: "Building - Agent",
				Next:  session,
			},
			line: "",
		},
		{
			name: "waiting on you",
			f: ResumeFacts{
				State:         "Integrating - Human",
				Next:          []Move{{Actor: "human", Event: "merge-pr"}, {Actor: "human", Event: "abandon"}},
				BlockersKnown: true,
			},
			line: "Waiting on you",
		},
		{
			name: "waiting on factory",
			f: ResumeFacts{
				State:         "Integrating - Agent",
				Next:          []Move{{Actor: "daemon"}},
				BlockersKnown: true,
			},
			line: "Waiting on factory",
		},
		{
			name: "nothing next",
			f: ResumeFacts{
				State:         "Building - Agent",
				BlockersKnown: true,
			},
			line: "",
		},
		{
			name: "only escape hatches",
			f: ResumeFacts{
				State:         "Planning - Human",
				Next:          []Move{{Actor: "human", Event: "abandon"}},
				BlockersKnown: true,
			},
			line: "",
		},
		{
			name: "request-revision while waiting on you",
			f: ResumeFacts{
				State:         "Planning - Human",
				Next:          []Move{{Actor: "human", Event: "address-comments"}, {Actor: "human", Event: "approve-for-building"}, rr, {Actor: "human", Event: "abandon"}},
				BlockersKnown: true,
			},
			line: "Waiting on you",
		},
		{
			name: "building beside request-revision",
			f: ResumeFacts{
				State:         "Building - Agent",
				Next:          append(slices.Clone(session), rr),
				BlockersKnown: true,
			},
			kind: ResumeBuild,
			line: build,
		},
		{
			name: "planning beside request-revision",
			f: ResumeFacts{
				State:         "Planning - Agent",
				Next:          append(slices.Clone(session), rr),
				BlockersKnown: true,
			},
			kind: ResumePlanner,
			line: planner,
		},
		{
			name: "only request-revision and abandon",
			f: ResumeFacts{
				State:         "Planning - Human",
				Next:          []Move{rr, {Actor: "human", Event: "abandon"}},
				BlockersKnown: true,
			},
			line: "",
		},
		{
			name: "unknown phase",
			f: ResumeFacts{
				State:         "Agent Ready",
				Next:          session,
				BlockersKnown: true,
			},
			line: "chunk can't resume a plan in Agent Ready",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ResumeOf(tc.f)
			assert.Check(t, cmp.Equal(r.Kind, tc.kind))
			assert.Check(t, cmp.Equal(r.Line(), tc.line))
		})
	}
}

func TestBlockerDone(t *testing.T) {
	for _, tc := range []struct {
		name    string
		state   string
		outcome string
		want    bool
	}{
		{
			name:  "done",
			state: "Done",
			want:  true,
		},
		{
			name:  "done with spaces",
			state: " Done ",
			want:  true,
		},
		{
			name:    "ended merged",
			state:   "ended",
			outcome: "merged",
			want:    true,
		},
		{
			name:    "ended canceled",
			state:   "ended",
			outcome: "canceled",
			want:    false,
		},
		{
			name:    "canceled",
			state:   "Canceled",
			outcome: "canceled",
			want:    false,
		},
		{
			name:  "building",
			state: "Building - Agent",
			want:  false,
		},
		{
			name:  "no state",
			state: "",
			want:  false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Check(t, cmp.Equal(BlockerDone(tc.state, tc.outcome), tc.want))
		})
	}
}
