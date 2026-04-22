package usererr

import "fmt"

// ExitError signals a specific process exit code to main.
// The error output is assumed to have already been written to stderr/stdout,
// so main should exit with Code without printing anything additional.
type ExitError struct {
	Code int
}

func (e *ExitError) Error() string { return fmt.Sprintf("exit %d", e.Code) }

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

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error {
	return e.err
}
