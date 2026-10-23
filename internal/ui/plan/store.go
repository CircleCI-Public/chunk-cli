package plan

import "context"

// Comment is a comment on the plan as factory keeps it.
type Comment struct {
	ID       string
	Quote    string // quoted_text
	Body     string
	Resolved bool // resolved_at is set
}

// Store is where the plan and its comments are kept. Before Create, reads
// are empty and writes fail.
type Store interface {
	Create(ctx context.Context, title string) error
	State(ctx context.Context) (string, error)
	Move(ctx context.Context, state string) error
	Text(ctx context.Context) (string, error)
	Comments(ctx context.Context) ([]Comment, error)
	AddComment(ctx context.Context, quote, body string) (Comment, error)
	EditComment(ctx context.Context, id, body string) error
	ResolveComment(ctx context.Context, id string) error
	DeleteComment(ctx context.Context, id string) error
}
