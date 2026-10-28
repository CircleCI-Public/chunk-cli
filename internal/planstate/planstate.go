// Package planstate reads a factory plan's state, as factory names it:
// "Phase - Actor", such as "Planning - Human", or a bare word such as
// "Unprioritized". Factory may add or rename states before chunk knows them,
// so every reading here has an answer for a state it does not recognise.
package planstate

import "strings"

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

// Parse splits state into its phase and the actor it waits on. A state
// without a recognised actor comes back whole as the phase, with Unknown.
func Parse(state string) (phase string, actor Actor) {
	state = strings.TrimSpace(state)
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
	return strings.EqualFold(strings.TrimSpace(state), Unprioritized)
}
