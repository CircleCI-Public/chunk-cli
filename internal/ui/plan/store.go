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
	// Events are the events factory applies to the flow now, by name; one
	// sent at any other time is ignored.
	Events []string
	// Task is the category of the task factory has handed out for the plan
	// and not yet had back, such as "early-plan"; "" when none is out.
	Task string
}

// Store is where the plan and its comments are kept. Before Create, ID and
// reads are empty and writes fail. An existing plan starts with its ID set.
type Store interface {
	ID() string
	Create(ctx context.Context, title string) error
	State(ctx context.Context) (string, error)
	// Flow is the plan's flow as factory has it now; empty before the plan
	// is created or has a flow.
	Flow(ctx context.Context) (Flow, error)
	// AddressComments has factory take up the plan's comments, moving it
	// from a Human state to its phase's Agent state. Factory moves it at
	// its next poll, not before AddressComments returns.
	AddressComments(ctx context.Context) error
	// ApproveDesign approves the plan's design, moving it from Planning -
	// Human for factory to write its units. Factory moves it at its next
	// poll, not before ApproveDesign returns.
	ApproveDesign(ctx context.Context) error
	Text(ctx context.Context) (string, error)
	Comments(ctx context.Context) ([]Comment, error)
	AddComment(ctx context.Context, quote, body string) (Comment, error)
	EditComment(ctx context.Context, id, body string) error
	ResolveComment(ctx context.Context, id string) error
	DeleteComment(ctx context.Context, id string) error
}
