// Package planstate names factory's flow states and the actors a flow
// waits on, and decides what chunk offers from the fields factory serves:
// state, waiting_on and next. Factory may add states before chunk knows
// them, so every reading here has an answer for a state it does not
// recognise.
package planstate

import "slices"

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
