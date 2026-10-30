// Package planstate reads a factory plan's state, as factory names it:
// "Phase - Actor", such as "Planning - Human", or a bare word such as
// "Unprioritized". Factory may add or rename states before chunk knows them,
// so every reading here has an answer for a state it does not recognise.
package planstate

import (
	"slices"
	"strings"
)

// Actor is who factory waits on to move a plan on.
type Actor int

const (
	// Unknown is a state that names no actor chunk recognises, or none.
	Unknown Actor = iota
	// Human is a state that waits on the developer.
	Human
	// Agent is a state that waits on factory's agent.
	Agent
)

// Unprioritized is the state of a plan nobody has started.
const Unprioritized = "Unprioritized"

// newNames are the names factory gives states after collapsing their
// actors, each to the phase chunk reads for its old name.
var newNames = map[string]string{
	"init":            Unprioritized,
	"ready":           "Agent Ready",
	"refine-proposal": "Planning",
	"refine-units":    "Planning",
	"stand-by":        "standing-by",
	"build":           "Building",
	"integrate":       "Integrating",
	"ended":           "Done",
}

// Parse splits state into its phase and the actor it waits on. A new name
// comes back as its old phase, with Unknown. A state without a recognised
// actor comes back whole as the phase, with Unknown.
func Parse(state string) (phase string, actor Actor) {
	state = strings.TrimSpace(state)
	if p, ok := newNames[strings.ToLower(state)]; ok {
		return p, Unknown
	}
	i := strings.LastIndex(state, " - ")
	if i < 0 {
		return state, Unknown
	}
	phase, who := strings.TrimSpace(state[:i]), strings.TrimSpace(state[i+len(" - "):])
	switch {
	case strings.EqualFold(who, "Human"):
		return phase, Human
	case strings.EqualFold(who, "Agent"):
		return phase, Agent
	}
	return state, Unknown
}

// IsUnprioritized is whether state is the state of a plan nobody has started.
func IsUnprioritized(state string) bool {
	state = strings.TrimSpace(state)
	return strings.EqualFold(state, Unprioritized) || strings.EqualFold(state, "init")
}

// Move is a way factory offers to move a plan on now: who takes it, as
// factory names them ("human", "session" or "daemon"), and its event, if
// it is one.
type Move struct {
	Actor, Event string
}

// ReadyToApprove is whether a plan in state, which next can move on, waits
// for the developer to approve it: it is in Planning and only a human can
// move it on.
func ReadyToApprove(state string, next []Move) bool {
	phase, _ := Parse(state)
	return strings.EqualFold(phase, "Planning") && WaitsOnHuman(next)
}

// RequestRevision is the event a session may send in any live state to ask
// for a revision of the plan, with its why. Factory lists it in every live
// flow's next, but never waits on it.
const RequestRevision = "request-revision"

// waitedOn is whether factory waits on m to move the plan on.
func waitedOn(m Move) bool {
	return strings.TrimSpace(m.Event) != RequestRevision
}

// WaitsOnHuman is whether next waits only on the developer: it offers
// them a transition other than the escape hatches, and nothing by a
// session or the daemon that factory waits on. An action, such as
// merge-pr, counts.
func WaitsOnHuman(next []Move) bool {
	return !AgentActs(next) && slices.ContainsFunc(next, func(m Move) bool {
		e := strings.TrimSpace(m.Event)
		return strings.TrimSpace(m.Actor) == "human" && e != "abandon" && e != "abandon-integrate"
	})
}

// AgentActs is whether next has a transition a session or the daemon
// takes that factory waits on: RequestRevision is not one.
func AgentActs(next []Move) bool {
	return slices.ContainsFunc(next, func(m Move) bool {
		a := strings.TrimSpace(m.Actor)
		return (a == "session" || a == "daemon") && waitedOn(m)
	})
}

// SessionActs is whether next has a transition a session takes that
// factory waits on: RequestRevision is not one.
func SessionActs(next []Move) bool {
	return slices.ContainsFunc(next, func(m Move) bool {
		return strings.TrimSpace(m.Actor) == "session" && waitedOn(m)
	})
}

// The events that approve a plan ReadyToApprove: its design, so factory
// writes its units, or the plan, so factory builds it.
const (
	approveForUnits    = "approve-for-units"
	approveForBuilding = "approve-for-building"
)

// Stage is what the developer approves next for a plan ReadyToApprove.
type Stage int

const (
	// StageDesign is a plan whose design waits for approval, so factory
	// writes its units.
	StageDesign Stage = iota
	// StagePlan is a plan that waits for approval, so factory builds it.
	StagePlan
	// StageQuestions is a plan factory offers no approval for: it waits for
	// a human to answer 3. Open Questions in comments.
	StageQuestions
)

// StageOf is what the developer approves next, given the events the plan's
// flow applies and how many units the plan has. When events lists exactly
// one approval, that one wins. When it lists both, as a flow factory stored
// before it told the two apart does, or is nil, because it has not loaded or
// there is no flow, the plan's units decide: none is StageDesign, any is
// StagePlan. When it is loaded and lists neither, even empty, it is
// StageQuestions.
func StageOf(events []string, units int) Stage {
	design, build := slices.Contains(events, approveForUnits), slices.Contains(events, approveForBuilding)
	switch {
	case design && !build:
		return StageDesign
	case build && !design:
		return StagePlan
	case !design && events != nil:
		return StageQuestions
	case units == 0:
		return StageDesign
	}
	return StagePlan
}
