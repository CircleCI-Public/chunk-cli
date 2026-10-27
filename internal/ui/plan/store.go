package plan

import "context"

// Comment is a comment on the plan as factory keeps it.
type Comment struct {
	ID       string
	Quote    string
	Body     string
	Resolved bool
}

// Store is where the plan and its comments are kept. Before Create, ID and
// reads are empty and writes fail. An existing plan starts with its ID set.
type Store interface {
	ID() string
	Create(ctx context.Context, title string) error
	State(ctx context.Context) (string, error)
	// Events are the events factory applies to the plan's flow now, by
	// name; one sent at any other time is ignored.
	Events(ctx context.Context) ([]string, error)
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
