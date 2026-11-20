package planstate

import (
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestResumeOf(t *testing.T) {
	done := []Blocker{{Title: "Add retry config", State: Ended, Outcome: "merged"}}
	const claim = "No worker holds this plan · R claims it for a worker"
	const holder = "1be8ec19-aaaa-bbbb-cccc-dddddddddddd"
	claimed := func(state string, edit func(*ResumeFacts)) ResumeFacts {
		f := ResumeFacts{PlanID: "p-42", State: state, Holder: holder, WaitingOn: Session, BlockersKnown: true}
		if edit != nil {
			edit(&f)
		}
		return f
	}
	const claimedLine = "Claimed by session 1be8ec19"
	const workerLine = "Worker scotts-mbp · architect running"
	for _, tc := range []struct {
		name string
		f    ResumeFacts
		kind ResumeKind
		line string
	}{
		{
			name: "holder and worker, planning",
			f:    claimed(RefineProposal, func(f *ResumeFacts) { f.Worker = "w-1" }),
			line: claimedLine,
		},
		{
			name: "claimed, short ID",
			f:    claimed(Init, func(f *ResumeFacts) { f.Holder = "s-1" }),
			line: "Claimed by session s-1",
		},
		{
			name: "worker, planning",
			f: claimed(RefineProposal, func(f *ResumeFacts) {
				f.Holder, f.Worker, f.WorkerLine = "", "w-1", workerLine
			}),
			line: workerLine,
		},
		{
			name: "worker, building",
			f: claimed(Build, func(f *ResumeFacts) {
				f.Holder, f.Worker, f.WorkerLine = "", "w-1", workerLine
			}),
			line: workerLine,
		},
		{
			name: "worker, no worker line",
			f:    claimed(Build, func(f *ResumeFacts) { f.Holder, f.Worker = "", "w-1" }),
			line: "Claimed by worker w-1",
		},
		{
			name: "worker, waiting_on empty",
			f: claimed(RefineProposal, func(f *ResumeFacts) {
				f.Holder, f.Worker, f.WorkerLine, f.WaitingOn = "", "w-1", workerLine, ""
			}),
			line: workerLine,
		},
		{
			name: "worker, waiting on human",
			f: claimed(RefineProposal, func(f *ResumeFacts) {
				f.Holder, f.Worker, f.WorkerLine, f.WaitingOn = "", "w-1", workerLine, Human
			}),
			line: "Waiting on you",
		},
		{
			name: "session and worker, waiting on human",
			f: claimed(RefineUnits, func(f *ResumeFacts) {
				f.Worker, f.WorkerLine, f.WaitingOn = "w-1", workerLine, Human
			}),
			line: "Waiting on you · claimed by session 1be8ec19",
		},
		{
			name: "session, waiting on factory",
			f:    claimed(RefineUnits, func(f *ResumeFacts) { f.WaitingOn = Daemon }),
			line: "Waiting on factory · claimed by session 1be8ec19",
		},
		{
			name: "session, waiting on session",
			f:    claimed(RefineUnits, nil),
			line: claimedLine,
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
			line: "Waiting on you · claimed by session 1be8ec19",
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
			kind: ResumeClaim,
			line: claim,
		},
		{
			name: "unclaimed, waiting on human",
			f:    ResumeFacts{PlanID: "p-42", State: RefineProposal, WaitingOn: Human, BlockersKnown: true},
			kind: ResumeClaim,
			line: "Waiting on you · R claims it for a worker",
		},
		{
			name: "unclaimed, waiting on factory",
			f:    ResumeFacts{PlanID: "p-42", State: Build, WaitingOn: Daemon, BlockersKnown: true},
			kind: ResumeClaim,
			line: "Waiting on factory · R claims it for a worker",
		},
		{
			name: "unclaimed, waiting on factory, blocker active",
			f: ResumeFacts{
				PlanID: "p-42", State: Build, WaitingOn: Daemon, BlockersKnown: true,
				Blockers: []Blocker{{Title: "Fix uploads", Active: true}},
			},
			line: "Waiting on Fix uploads",
		},
		{
			name: "claimed by session, waiting on factory",
			f:    claimed(Build, func(f *ResumeFacts) { f.WaitingOn = Daemon }),
			line: "Waiting on factory · claimed by session 1be8ec19",
		},
		{
			name: "building, unclaimed",
			f: ResumeFacts{
				PlanID:        "p-42",
				State:         Build,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: claim,
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
			line: "Claimed by session bb5a623b",
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
			kind: ResumeClaim,
			line: claim,
		},
		{
			name: "new name build",
			f: ResumeFacts{
				State:         Build,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: claim,
		},
		{
			name: "planning",
			f: ResumeFacts{
				State:         RefineProposal,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: claim,
		},
		{
			name: "refining units",
			f: ResumeFacts{
				State:         RefineUnits,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: claim,
		},
		{
			name: "ready",
			f: ResumeFacts{
				State:         Ready,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: claim,
		},
		{
			name: "unprioritized",
			f: ResumeFacts{
				State:         Init,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: claim,
		},
		{
			name: "recording sub-plans",
			f: ResumeFacts{
				State:         RecordSubPlans,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: claim,
		},
		{
			name: "recording sub-plans, waiting on you",
			f: ResumeFacts{
				State:         RecordSubPlans,
				WaitingOn:     Human,
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: "Waiting on you · R claims it for a worker",
		},
		{
			name: "standing-by",
			f: ResumeFacts{
				State:         StandBy,
				WaitingOn:     Session,
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: claim,
		},
		{
			name: "every blocker done",
			f: ResumeFacts{
				State:         Build,
				WaitingOn:     Session,
				Blockers:      done,
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: claim,
		},
		{
			name: "a blocker merged",
			f: ResumeFacts{
				State:         Build,
				WaitingOn:     Session,
				Blockers:      []Blocker{{Title: "a", State: Ended, Outcome: "merged"}},
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: claim,
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
			line: "Claimed by session s-1",
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
			kind: ResumeClaim,
			line: claim,
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
			kind: ResumeClaim,
			line: "Waiting on you · R claims it for a worker",
		},
		{
			name: "waiting on factory",
			f: ResumeFacts{
				State:         Integrate,
				WaitingOn:     Daemon,
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: "Waiting on factory · R claims it for a worker",
		},
		{
			name: "nothing next",
			f: ResumeFacts{
				State:         Integrate,
				BlockersKnown: true,
			},
			kind: ResumeClaim,
			line: claim,
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
			kind: ResumeClaim,
			line: claim,
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

func TestClaimedWhy(t *testing.T) {
	assert.Check(t, cmp.Equal(ClaimedWhy("s-1"), "Claimed by session s-1"))
	assert.Check(t, cmp.Equal(ClaimedWhy("1be8ec19-aaaa-bbbb-cccc-dddddddddddd"), "Claimed by session 1be8ec19"))
}
