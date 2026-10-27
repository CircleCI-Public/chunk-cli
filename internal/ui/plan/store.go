package plan

import "context"

// Comment is a comment on the plan as factory keeps it.
type Comment struct {
	ID       string
	Quote    string
	Body     string
	Resolved bool
}

// Store is where the plan and its comments are kept. Before Create, reads
// are empty and writes fail.
type Store interface {
	Create(ctx context.Context, title string) error
	State(ctx context.Context) (string, error)
	// AddressComments has factory take up the plan's comments, moving it
	// from a Human state to its phase's Agent state. Factory moves it at
	// its next poll, not before AddressComments returns.
	AddressComments(ctx context.Context) error
	Text(ctx context.Context) (string, error)
	Comments(ctx context.Context) ([]Comment, error)
	AddComment(ctx context.Context, quote, body string) (Comment, error)
	EditComment(ctx context.Context, id, body string) error
	ResolveComment(ctx context.Context, id string) error
	DeleteComment(ctx context.Context, id string) error
}
