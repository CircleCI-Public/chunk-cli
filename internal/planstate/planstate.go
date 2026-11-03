// Package planstate names factory's flow states and the actors a flow
// waits on, and decides what chunk offers from the fields factory serves:
// state, waiting_on and next. Factory may add states before chunk knows
// them, so every reading here has an answer for a state it does not
// recognise.
package planstate

import (
	"slices"
	"strings"
)

// The flow states factory serves.
const (
	Init           = "init"
	Ready          = "ready"
	RefineProposal = "refine-proposal"
	RefineUnits    = "refine-units"
	RecordSubPlans = "record-sub-plans"
	StandBy        = "stand-by"
	Build          = "build"
	Integrate      = "integrate"
	Ended          = "ended"
)

// Who a flow waits on, as factory serves waiting_on.
const (
	Human    = "human"
	Session  = "session"
	Daemon   = "daemon"
	Blockers = "blockers"
	SubPlans = "sub-plans"
)

// Move is an entry of a flow's next: who takes it, its event if it is one,
// and whether factory needs it, or another required entry, to move on.
type Move struct {
	Actor, Event string
	Required     bool
}

// The events that approve a plan: its design, so factory writes its units,
// or the plan, so factory builds it.
const (
	ApproveForUnits    = "approve-for-units"
	ApproveForBuilding = "approve-for-building"
)

// Approval is the approval the developer is offered: approve-for-building
// when a human entry requires it, even when approve-for-units is required
// too, else approve-for-units when a human entry requires that, else "". An
// approval factory lists but does not require is not offered.
func Approval(next []Move) string {
	required := func(event string) bool {
		return slices.ContainsFunc(next, func(m Move) bool {
			return m.Actor == Human && m.Event == event && m.Required
		})
	}
	switch {
	case required(ApproveForBuilding):
		return ApproveForBuilding
	case required(ApproveForUnits):
		return ApproveForUnits
	}
	return ""
}

const planning = "Planning"

// Step is the label the screens show for state.
func Step(state string) string {
	switch state {
	case Init:
		return "Not started"
	case Ready, RefineProposal, RefineUnits:
		return planning
	case RecordSubPlans, StandBy:
		return "Standing by"
	case Build:
		return "Building"
	case Integrate:
		return "Integrating"
	case Ended:
		return "Done"
	}
	return state
}

// Unprioritized is the state of a plan nobody has started.
const Unprioritized = "Unprioritized"

// newNames are the names factory gives states after collapsing their
// actors, each to the phase chunk reads for its old name.
var newNames = map[string]string{
	"init":            Unprioritized,
	"ready":           "Agent Ready",
	"refine-proposal": planning,
	"refine-units":    planning,
	"stand-by":        "standing-by",
	"build":           "Building",
	"integrate":       "Integrating",
	"ended":           "Done",
}

// Parse is the phase of state: a new name comes back as its old phase, and
// "Phase - Human" or "Phase - Agent" as Phase. Any other state comes back
// whole.
func Parse(state string) string {
	state = strings.TrimSpace(state)
	if p, ok := newNames[strings.ToLower(state)]; ok {
		return p
	}
	i := strings.LastIndex(state, " - ")
	if i < 0 {
		return state
	}
	phase, who := strings.TrimSpace(state[:i]), strings.TrimSpace(state[i+len(" - "):])
	if strings.EqualFold(who, "Human") || strings.EqualFold(who, "Agent") {
		return phase
	}
	return state
}

// IsUnprioritized is whether state is the state of a plan nobody has started.
func IsUnprioritized(state string) bool {
	state = strings.TrimSpace(state)
	return strings.EqualFold(state, Unprioritized) || strings.EqualFold(state, "init")
}

// ReadyToApprove is whether a plan in state, which next can move on, waits
// for the developer to approve it: it is in Planning and only a human can
// move it on.
func ReadyToApprove(state string, next []Move) bool {
	return strings.EqualFold(Parse(state), planning) && WaitsOnHuman(next)
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
	design, build := slices.Contains(events, ApproveForUnits), slices.Contains(events, ApproveForBuilding)
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
