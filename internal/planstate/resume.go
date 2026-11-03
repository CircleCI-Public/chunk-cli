package planstate

import (
	"fmt"
	"strings"
)

// ResumeKind is how chunk starts a plan's agent again.
type ResumeKind int

const (
	// ResumeNone is a plan chunk does not resume now.
	ResumeNone ResumeKind = iota
	// ResumePlanner has the plan's planner claim it again.
	ResumePlanner
	// ResumeBuild starts a build session that claims the plan.
	ResumeBuild
	// ResumeTakeOver releases a claim another session holds on a plan being
	// planned, then resumes its planner.
	ResumeTakeOver
)

// ResumeFacts is what ResumeOf reads of a plan.
type ResumeFacts struct {
	// State is the flow's state, "" when the plan has no flow.
	State string
	Ended bool
	// PlanID is the plan's, and Holder the session claiming it, "" when
	// none does.
	PlanID, Holder string
	// WaitingOn is who factory waits on to move the flow on, "" for nobody.
	WaitingOn string
	// Blockers are the plan's blockers; BlockersKnown is false until they
	// have loaded.
	Blockers      []Blocker
	BlockersKnown bool
}

// Blocker is a plan blocking another, as factory serves it.
type Blocker struct {
	Title, State, Outcome string
	Active                bool
}

// Resume is whether chunk resumes a plan, and how; Why is why not, "" when
// there is nothing to say.
type Resume struct {
	Kind ResumeKind
	Why  string
	// Holder is the session ResumeTakeOver releases.
	Holder string
}

// ResumeOf is whether chunk resumes the plan f describes, and how: a plan
// with a live flow nobody claims, whose blockers are all done, which factory
// waits on a session to move on, in a state chunk knows how to resume.
func ResumeOf(f ResumeFacts) Resume {
	state := strings.TrimSpace(f.State)
	switch {
	case state == "", f.Ended:
		return Resume{}
	case !f.BlockersKnown:
		return Resume{}
	}
	for _, b := range f.Blockers {
		if b.Active {
			return Resume{Why: "Waiting on " + b.Title}
		}
	}
	switch strings.TrimSpace(f.WaitingOn) {
	case Human:
		return Resume{Why: "Waiting on you"}
	case Daemon:
		return Resume{Why: "Waiting on factory"}
	case Blockers:
		return Resume{Why: "Waiting on blockers"}
	case SubPlans:
		return Resume{Why: "Waiting on sub-plans"}
	case Session:
	default:
		if f.Holder != "" {
			return Resume{Why: ClaimedWhy(f.Holder, f.PlanID)}
		}
		return Resume{}
	}
	switch state {
	case Init, Ready, RefineProposal, RefineUnits, RecordSubPlans, StandBy:
		if f.Holder != "" {
			return Resume{Kind: ResumeTakeOver, Holder: f.Holder}
		}
		return Resume{Kind: ResumePlanner}
	case Build, Integrate:
		if f.Holder != "" {
			return Resume{Why: ClaimedWhy(f.Holder, f.PlanID)}
		}
		return Resume{Kind: ResumeBuild}
	}
	return Resume{Why: "chunk can't resume a plan in " + state}
}

// ClaimedWhy is the line for a plan a session holds that chunk does not take
// over.
func ClaimedWhy(holder, planID string) string {
	return fmt.Sprintf("Claimed by session %s · factory daemon release %s frees it", shortID(holder), planID)
}

// TakeOverQuestion is what chunk asks before releasing holder's claim and
// resuming the planner, without the y/n choice.
func TakeOverQuestion(holder string) string {
	return fmt.Sprintf("Plan is claimed by session %s, which chunk watch didn't start. Release it and resume the planner?", shortID(holder))
}

// Line is r as one line: what resuming does when Kind offers it, else Why.
func (r Resume) Line() string {
	switch r.Kind {
	case ResumePlanner:
		return "No session holds this plan · R resumes its planner"
	case ResumeBuild:
		return "No session holds this plan · R starts a build session"
	case ResumeTakeOver:
		return fmt.Sprintf("Claimed by session %s · R releases it and resumes the planner", shortID(r.Holder))
	case ResumeNone:
	}
	return r.Why
}

// shortID is id cut to the length session IDs are usually shown at.
func shortID(id string) string {
	if r := []rune(id); len(r) > 8 {
		return string(r[:8])
	}
	return id
}
