package planstate

import (
	"fmt"
	"strings"
)

// ResumeKind is how chunk hands a plan to a worker again.
type ResumeKind int

const (
	// ResumeNone is a plan chunk does not resume now.
	ResumeNone ResumeKind = iota
	// ResumeClaim has the default worker claim the plan.
	ResumeClaim
)

// ResumeFacts is what ResumeOf reads of a plan.
type ResumeFacts struct {
	// State is the flow's state, "" when the plan has no flow.
	State string
	Ended bool
	// PlanID is the plan's, and Holder the session claiming it, "" when
	// none does.
	PlanID, Holder string
	// Worker is the ID of the factory worker claiming the plan, "" when none
	// does. It is set whether or not a session works the plan; ResumeOf treats
	// the plan as a worker's when Holder is "" and Worker is set.
	Worker string
	// WorkerLine is the line for that worker, built by the caller from what it
	// read: "Worker <hostname> · <state>", or the unreadable wording.
	WorkerLine string
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
}

// ResumeOf is whether chunk resumes the plan f describes, and how: a plan
// with a live flow that no session or worker claims and no active blocker
// holds, whoever factory waits on. A wait on a session offers it only in a
// state chunk knows how to resume. A held plan shows who holds it, and a
// wait on blockers or sub-plans offers nothing.
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
		return waitOrClaim(f, "Waiting on you")
	case Daemon:
		return waitOrClaim(f, "Waiting on factory")
	case Blockers:
		return Resume{Why: "Waiting on blockers"}
	case SubPlans:
		return Resume{Why: "Waiting on sub-plans"}
	case Session:
	default:
		if r := held(f); r.Why != "" {
			return r
		}
		return Resume{Kind: ResumeClaim}
	}
	switch state {
	case Init, Ready, RefineProposal, RefineUnits, RecordSubPlans, StandBy, Build, Integrate:
		if r := held(f); r.Why != "" {
			return r
		}
		return Resume{Kind: ResumeClaim}
	}
	return Resume{Why: "chunk can't resume a plan in " + state}
}

// waitOrClaim is wait for a plan a worker holds, wait with the session's claim
// after it for one a session holds, and for one nobody holds the same line
// with R offered to claim it for a worker.
func waitOrClaim(f ResumeFacts, wait string) Resume {
	if f.Holder != "" {
		claimed := ClaimedWhy(f.Holder)
		r := []rune(claimed)
		return Resume{Why: wait + " · " + strings.ToLower(string(r[:1])) + string(r[1:])}
	}
	if held(f).Why != "" {
		return Resume{Why: wait}
	}
	return Resume{Kind: ResumeClaim, Why: wait + " · R claims it for a worker"}
}

// held is the line for a plan a session or a worker claims, the zero Resume
// when nobody does. A session claim wins: chunk offers nothing for it.
func held(f ResumeFacts) Resume {
	switch {
	case f.Holder != "":
		return Resume{Why: ClaimedWhy(f.Holder)}
	case f.Worker != "":
		if f.WorkerLine != "" {
			return Resume{Why: f.WorkerLine}
		}
		return Resume{Why: "Claimed by worker " + shortID(f.Worker)}
	}
	return Resume{}
}

// ClaimedWhy is the line for a plan a session holds, which chunk does not
// take over.
func ClaimedWhy(holder string) string {
	return fmt.Sprintf("Claimed by session %s", shortID(holder))
}

// Line is r as one line: what resuming does when Kind offers it, else Why.
func (r Resume) Line() string {
	if r.Kind == ResumeClaim && r.Why != "" {
		return r.Why
	}
	if r.Kind == ResumeClaim {
		return "No worker holds this plan · R claims it for a worker"
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
