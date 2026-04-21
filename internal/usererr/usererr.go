package usererr

import "fmt"

// Error wraps a Go error with a user-facing message.
// Error() delegates to the wrapped error. UserMessage() returns
// human-friendly text (may be capitalized, punctuated).
type Error struct {
	userMsg    string // user-facing message
	detail     string // optional detail line
	suggestion string // optional suggestion line
	err        error  // underlying Go error
}

// New wraps err with a user-facing message.
func New(userMsg string, err error) *Error {
	return &Error{userMsg: userMsg, err: err}
}

// NewDetailed wraps err with structured user-facing fields (brief, detail, suggestion)
// that are kept as plain text. Formatting is applied at the display boundary.
func NewDetailed(brief, detail, suggestion string, err error) *Error {
	return &Error{userMsg: brief, detail: detail, suggestion: suggestion, err: err}
}

// Newf wraps a formatted error with a user-facing message.
func Newf(userMsg string, format string, args ...any) *Error {
	return &Error{userMsg: userMsg, err: fmt.Errorf(format, args...)}
}

func (e *Error) Error() string {
	return e.err.Error()
}

// UserMessage returns the user-facing message.
func (e *Error) UserMessage() string {
	return e.userMsg
}

// Detail returns the optional detail line.
func (e *Error) Detail() string { return e.detail }

// Suggestion returns the optional suggestion line.
func (e *Error) Suggestion() string { return e.suggestion }

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error {
	return e.err
}
