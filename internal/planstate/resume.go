package planstate

import (
	"fmt"
	"slices"
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
)

// ResumeFacts is what ResumeOf reads of a plan.
type ResumeFacts struct {
	// State is the flow's state, "" when the plan has no flow.
	State string
	Ended bool
	// PlanID is the plan's, and Holder the session claiming it, "" when
	// none does.
	PlanID, Holder string
	Next           []Move
	// Blockers are the plan's blockers; BlockersKnown is false until they
	// have loaded.
	Blockers      []Blocker
	BlockersKnown bool
}

// Blocker is a plan blocking another: its title, and its flow's state and
// outcome.
type Blocker struct{ Title, State, Outcome string }

// Resume is whether chunk resumes a plan, and how; Why is why not, "" when
// there is nothing to say.
type Resume struct {
	Kind ResumeKind
	Why  string
}

// ResumeOf is whether chunk resumes the plan f describes, and how: a plan
// with a live flow nobody claims, whose blockers are all done, which factory
// waits on a session to move on, in a phase chunk knows how to resume.
func ResumeOf(f ResumeFacts) Resume {
	state := strings.TrimSpace(f.State)
	switch {
	case state == "":
		return Resume{Why: "No flow: factory hands this plan no tasks"}
	case f.Ended:
		return Resume{}
	case f.Holder != "":
		return Resume{Why: fmt.Sprintf("Claimed by session %s · factory daemon release %s frees it", shortID(f.Holder), f.PlanID)}
	case !f.BlockersKnown:
		return Resume{}
	}
	for _, b := range f.Blockers {
		if !BlockerDone(b.State, b.Outcome) {
			return Resume{Why: "Waiting on " + b.Title}
		}
	}
	if !slices.ContainsFunc(f.Next, func(m Move) bool { return strings.TrimSpace(m.Actor) == "session" }) {
		switch {
		case waitsOnHuman(f.Next):
			return Resume{Why: "Waiting on you"}
		case AgentActs(f.Next):
			return Resume{Why: "Waiting on factory"}
		}
		return Resume{}
	}
	phase, _ := Parse(state)
	switch {
	case IsUnprioritized(state), strings.EqualFold(phase, "Planning"), strings.EqualFold(phase, "standing-by"):
		return Resume{Kind: ResumePlanner}
	case strings.EqualFold(phase, "Building"), strings.EqualFold(phase, "Integrating"):
		return Resume{Kind: ResumeBuild}
	}
	return Resume{Why: "chunk can't resume a plan in " + state}
}

// Line is r as one line: what resuming does when Kind offers it, else Why.
func (r Resume) Line() string {
	switch r.Kind {
	case ResumePlanner:
		return "No session holds this plan · R resumes its planner"
	case ResumeBuild:
		return "No session holds this plan · R starts a build session"
	case ResumeNone:
	}
	return r.Why
}

// BlockerDone is whether a blocker in state, ended with outcome, no longer
// holds the plan it blocks: its flow merged, whichever name its end state
// has. Both are read as given, so callers strip escapes first.
func BlockerDone(state, outcome string) bool {
	state = strings.TrimSpace(state)
	return state == "Done" || strings.EqualFold(state, "ended") && outcome == "merged"
}

// shortID is id cut to the length session IDs are usually shown at.
func shortID(id string) string {
	if r := []rune(id); len(r) > 8 {
		return string(r[:8])
	}
	return id
}
