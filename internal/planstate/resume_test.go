package planstate

import (
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestResumeOf(t *testing.T) {
	done := []Blocker{{Title: "Add retry config", State: Ended, Outcome: "merged"}}
	const build = "No session holds this plan · R starts a build session"
	const planner = "No session holds this plan · R resumes its planner"
	const holder = "1be8ec19-aaaa-bbbb-cccc-dddddddddddd"
	claimed := func(state string, edit func(*ResumeFacts)) ResumeFacts {
		f := ResumeFacts{PlanID: "p-42", State: state, Holder: holder, WaitingOn: Session, BlockersKnown: true}
		if edit != nil {
			edit(&f)
		}
		return f
	}
	const takeOver = "Claimed by session 1be8ec19 · R releases it and resumes the planner"
	const claimedLine = "Claimed by session 1be8ec19 · factory daemon release p-42 frees it"
	for _, tc := range []struct {
		name string
		f    ResumeFacts
		kind ResumeKind
		line string
	}{
		{
			name: "claimed, planning",
			f:    claimed(RefineProposal, nil),
			kind: ResumeTakeOver,
			line: takeOver,
		},
		{
			name: "claimed, init",
			f:    claimed(Init, nil),
			kind: ResumeTakeOver,
			line: takeOver,
		},
		{
			name: "claimed, ready",
			f:    claimed(Ready, nil),
			kind: ResumeTakeOver,
			line: takeOver,
		},
		{
			name: "claimed, refining units",
			f:    claimed(RefineUnits, nil),
			kind: ResumeTakeOver,
			line: takeOver,
		},
		{
			name: "claimed, recording sub-plans",
			f:    claimed(RecordSubPlans, nil),
			kind: ResumeTakeOver,
			line: takeOver,
		},
		{
			name: "claimed, standing-by",
			f:    claimed(StandBy, nil),
			kind: ResumeTakeOver,
			line: takeOver,
		},
		{
			name: "claimed, short ID",
			f:    claimed(Init, func(f *ResumeFacts) { f.Holder = "s-1" }),
			kind: ResumeTakeOver,
			line: "Claimed by session s-1 · R releases it and resumes the planner",
		},
		{
			name: "claimed, ended",
			f:    claimed(Ended, func(f *ResumeFacts) { f.Ended = true }),
		},
		{
			name: "claimed, no flow",
			f:    claimed("", nil),
		},
		{
			name: "claimed, blocker active",
			f: claimed(RefineProposal, func(f *ResumeFacts) {
				f.Blockers = []Blocker{{Title: "Fix uploads", Active: true}}
			}),
			line: "Waiting on Fix uploads",
		},
		{
			name: "claimed, blockers not loaded",
			f:    claimed(RefineProposal, func(f *ResumeFacts) { f.BlockersKnown = false }),
		},
		{
			name: "claimed, waiting on human",
			f:    claimed(RefineProposal, func(f *ResumeFacts) { f.WaitingOn = Human }),
			line: "Waiting on you",
		},
		{
			name: "claimed, unknown state",
			f:    claimed("weird", nil),
			line: "chunk can't resume a plan in weird",
		},
		{
			name: "claimed, building",
			f:    claimed(Build, nil),
			line: claimedLine,
		},
		{
			name: "claimed, integrating",
			f:    claimed(Integrate, nil),
			line: claimedLine,
		},
		{
			name: "claimed, waiting_on empty",
			f:    claimed(RefineProposal, func(f *ResumeFacts) { f.WaitingOn = "" }),
			line: claimedLine,
		},
		{
			name: "unclaimed, waiting_on empty",
			f:    ResumeFacts{PlanID: "p-42", State: RefineProposal, BlockersKnown: true},
		},
		{
			name: "building, unclaimed",
			f: ResumeFacts{
				PlanID:        "p-42",
				State:         Build,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumeBuild,
			line: build,
		},
		{
			name: "claimed",
			f: ResumeFacts{
				PlanID:        "p-42",
				State:         Build,
				Holder:        "bb5a623b-8dd3-4ae8-9c91-48b4eaaf6d62",
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			line: "Claimed by session bb5a623b · factory daemon release p-42 frees it",
		},
		{
			name: "blocked",
			f: ResumeFacts{
				State:         Build,
				WaitingOn:     Blockers,
				Blockers:      append(done, Blocker{Title: "Fix uploads", State: Build, Active: true}),
				BlockersKnown: true,
			},
			line: "Waiting on Fix uploads",
		},
		{
			name: "integrating",
			f: ResumeFacts{
				State:         Integrate,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumeBuild,
			line: build,
		},
		{
			name: "new name build",
			f: ResumeFacts{
				State:         Build,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumeBuild,
			line: build,
		},
		{
			name: "planning",
			f: ResumeFacts{
				State:         RefineProposal,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumePlanner,
			line: planner,
		},
		{
			name: "refining units",
			f: ResumeFacts{
				State:         RefineUnits,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumePlanner,
			line: planner,
		},
		{
			name: "ready",
			f: ResumeFacts{
				State:         Ready,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumePlanner,
			line: planner,
		},
		{
			name: "unprioritized",
			f: ResumeFacts{
				State:         Init,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumePlanner,
			line: planner,
		},
		{
			name: "recording sub-plans",
			f: ResumeFacts{
				State:         RecordSubPlans,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumePlanner,
			line: planner,
		},
		{
			name: "recording sub-plans, waiting on you",
			f: ResumeFacts{
				State:         RecordSubPlans,
				WaitingOn:     Human,
				BlockersKnown: true,
			},
			line: "Waiting on you",
		},
		{
			name: "standing-by",
			f: ResumeFacts{
				State:         StandBy,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumePlanner,
			line: planner,
		},
		{
			name: "every blocker done",
			f: ResumeFacts{
				State:         Build,
				WaitingOn:     Session,
				Blockers:      done,
				BlockersKnown: true,
			},
			kind: ResumeBuild,
			line: build,
		},
		{
			name: "a blocker merged",
			f: ResumeFacts{
				State:         Build,
				WaitingOn:     Session,
				Blockers:      []Blocker{{Title: "a", State: Ended, Outcome: "merged"}},
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
		},
		{
			name: "ended",
			f: ResumeFacts{
				State:         Ended,
				Ended:         true,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			line: "",
		},
		{
			name: "claimed by a short ID",
			f: ResumeFacts{
				PlanID:        "p-42",
				State:         Build,
				Holder:        "s-1",
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			line: "Claimed by session s-1 · factory daemon release p-42 frees it",
		},
		{
			name: "a canceled blocker still blocks",
			f: ResumeFacts{
				State:         Build,
				WaitingOn:     Blockers,
				Blockers:      []Blocker{{Title: "a", State: Ended, Outcome: "canceled", Active: true}},
				BlockersKnown: true,
			},
			line: "Waiting on a",
		},
		{
			name: "a blocker past its until",
			f: ResumeFacts{
				State:         Build,
				WaitingOn:     Session,
				Blockers:      []Blocker{{Title: "a", State: Build}},
				BlockersKnown: true,
			},
			kind: ResumeBuild,
			line: build,
		},
		{
			name: "blockers not loaded",
			f: ResumeFacts{
				State:     Build,
				WaitingOn: Session,
			},
			line: "",
		},
		{
			name: "waiting on you",
			f: ResumeFacts{
				State:         Integrate,
				WaitingOn:     Human,
				BlockersKnown: true,
			},
			line: "Waiting on you",
		},
		{
			name: "waiting on factory",
			f: ResumeFacts{
				State:         Integrate,
				WaitingOn:     Daemon,
				BlockersKnown: true,
			},
			line: "Waiting on factory",
		},
		{
			name: "nothing next",
			f: ResumeFacts{
				State:         Integrate,
				BlockersKnown: true,
			},
			line: "",
		},
		{
			name: "waiting on blockers",
			f: ResumeFacts{
				State:         Build,
				WaitingOn:     Blockers,
				BlockersKnown: true,
			},
			line: "Waiting on blockers",
		},
		{
			name: "waiting on sub-plans",
			f: ResumeFacts{
				State:         Build,
				WaitingOn:     SubPlans,
				BlockersKnown: true,
			},
			line: "Waiting on sub-plans",
		},
		{
			name: "waiting on nobody",
			f: ResumeFacts{
				State:         Build,
				BlockersKnown: true,
			},
		},
		{
			name: "unknown state",
			f: ResumeFacts{
				State:         "paused",
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			line: "chunk can't resume a plan in paused",
		},
		{
			name: "an old name is unknown",
			f: ResumeFacts{
				State:         "Planning - Human",
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			line: "chunk can't resume a plan in Planning - Human",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := ResumeOf(tc.f)
			assert.Check(t, cmp.Equal(r.Kind, tc.kind))
			assert.Check(t, cmp.Equal(r.Line(), tc.line))
		})
	}
}

func TestTakeOverQuestion(t *testing.T) {
	assert.Check(t, cmp.Equal(TakeOverQuestion("1be8ec19-aaaa-bbbb-cccc-dddddddddddd"),
		"Plan is claimed by session 1be8ec19, which chunk watch didn't start. Release it and resume the planner?"))
	assert.Check(t, cmp.Equal(TakeOverQuestion("s-1"),
		"Plan is claimed by session s-1, which chunk watch didn't start. Release it and resume the planner?"))
	assert.Check(t, cmp.Equal(ClaimedWhy("s-1", "p-42"), "Claimed by session s-1 · factory daemon release p-42 frees it"))
}
