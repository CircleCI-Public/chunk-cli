package plan

import "context"

// Comment is a comment on the plan as factory keeps it.
type Comment struct {
	ID       string
	Quote    string
	Body     string
	Resolved bool
}

// Flow is what the screen reads of the plan's flow in factory.
type Flow struct {
	// State is the flow's state as factory names it, "" before the plan is
	// created or when it has no flow.
	State string
	// Events are the events factory applies to the flow now, by name; one
	// sent at any other time is ignored.
	Events []string
	// Task is the category of the task factory has handed out for the plan
	// and not yet had back, such as "early-plan"; "" when none is out.
	Task string
	// Unit is the unit Task works on, "" when none.
	Unit string
	// Next is every way the flow can move on now, in order.
	Next []Step
	// PR is the flow's pull request's URL, "" while it has none.
	PR         string
	Deviations []string
	// Ended is whether the flow has ended, and Outcome how, as factory
	// names it.
	Ended   bool
	Outcome string
}

// Step is a way the flow can move on now, and who takes it, as factory
// names them: one of Event, Task and Action is set.
type Step struct {
	Actor, Event, Task, Action string
	// Dispatched is whether Task is the flow's outstanding task.
	Dispatched bool
}

// Relative is a plan related to the screen's: its parent, a sub-plan or a
// blocker.
type Relative struct {
	ID, Title, State string
}

// Family is the plans related to the screen's plan. Children are its open
// sub-plans only: factory does not list one that has ended.
type Family struct {
	Parent   *Relative
	Children []Relative
	Blockers []Relative
}

// Store is where the plan and its comments are kept. Before Create, ID and
// reads are empty and writes fail. An existing plan starts with its ID set.
type Store interface {
	ID() string
	// Create makes the plan in factory for message, the developer's first
	// message, naming it for what the message asks for.
	Create(ctx context.Context, message string) error
	// Flow is the plan's flow as factory has it now; empty before the plan
	// is created or has a flow.
	Flow(ctx context.Context) (Flow, error)
	// Family is the plan's parent, open sub-plans and blockers; empty
	// before Create.
	Family(ctx context.Context) (Family, error)
	// AddressComments has factory take up the plan's comments, moving it
	// from a Human state to its phase's Agent state. Factory moves it at
	// its next poll, not before AddressComments returns.
	AddressComments(ctx context.Context) error
	// ResolveConflicts has factory rebase the plan's conflicting PR, moving
	// it from Integrating - Human to Integrating - Agent. Factory moves it at
	// its next poll, not before ResolveConflicts returns.
	ResolveConflicts(ctx context.Context) error
	// ApproveDesign approves the plan's design, moving it from Planning -
	// Human for factory to write its units. Factory moves it at its next
	// poll, not before ApproveDesign returns.
	ApproveDesign(ctx context.Context) error
	Text(ctx context.Context) (string, error)
	// SetText replaces the plan's text with text, which the developer
	// has edited. It is a whole-text write: the caller checks the plan is
	// as it last read it first.
	SetText(ctx context.Context, text string) error
	Comments(ctx context.Context) ([]Comment, error)
	AddComment(ctx context.Context, quote, body string) (Comment, error)
	EditComment(ctx context.Context, id, body string) error
	ResolveComment(ctx context.Context, id string) error
	DeleteComment(ctx context.Context, id string) error
}
