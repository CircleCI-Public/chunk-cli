package usererr

// Error wraps a Go error with a user-facing message.
// Error() delegates to the wrapped error. UserMessage() returns
// human-friendly text (may be capitalized, punctuated).
type Error struct {
	userMsg string // user-facing message
	err     error  // underlying Go error
}

// New wraps err with a user-facing message.
func New(userMsg string, err error) *Error {
	return &Error{userMsg: userMsg, err: err}
}

func (e *Error) Error() string {
	return e.err.Error()
}

// UserMessage returns the user-facing message.
func (e *Error) UserMessage() string {
	return e.userMsg
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error {
	return e.err
}
