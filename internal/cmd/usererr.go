package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// Exit codes for specific failure modes. Commands should return errors that
// carry one of these codes so scripts can distinguish failure categories.
const (
	ExitGeneral   = 1 // unclassified error
	ExitBadArgs   = 2 // bad arguments or flag misuse
	ExitAuthError = 3 // authentication failure
	ExitAPIError  = 4 // API call failure
	ExitNotFound  = 5 // resource not found
)

const suggestionReauth = "Check your CircleCI token and try again."

func notAuthorized(action string, err error) error {
	if !errors.Is(err, circleci.ErrNotAuthorized) {
		return nil
	}
	return newUserError(fmt.Sprintf("Not authorized to %s.", action)).
		withCode("auth.not_authorized").
		withSuggestion(suggestionReauth).
		withExitCode(ExitAuthError).
		wrap(err)
}

func sshSessionError(err error) error {
	if e, ok := errors.AsType[*sidecar.KeyNotFoundError](err); ok {
		return newUserError(fmt.Sprintf("SSH key not found: %s", e.Path)).
			withCode("ssh.key_not_found").
			withSuggestion(fmt.Sprintf("Generate one with: ssh-keygen -t ed25519 -f %s\nOr pass --identity-file to use an existing key.", e.Path)).
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	if e, ok := errors.AsType[*sidecar.PublicKeyNotFoundError](err); ok {
		return newUserError(fmt.Sprintf("SSH public key not found: %s", e.KeyPath)).
			withCode("ssh.public_key_not_found").
			withSuggestion(fmt.Sprintf("Generate a new keypair with: ssh-keygen -t ed25519 -f %s", e.IdentityFile)).
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	if errors.Is(err, sidecar.ErrAuthSockNotSet) {
		return newUserError("SSH agent not available.").
			withCode("ssh.auth_sock_not_set").
			withSuggestion("Set " + config.EnvSSHAuthSock + " or pass --identity-file.").
			withExitCode(ExitBadArgs).
			wrap(err)
	}
	return nil
}

// userError is a structured error with a user-facing message, optional
// detail/suggestion, a namespaced code for machine parsing, and a specific
// exit code for script use. Construct with newUserError() and chain builder
// methods, or use struct literals within this package.
type userError struct {
	code       string // namespaced identifier, e.g. "auth.token_missing"
	msg        string
	detail     string
	suggestion string
	exitCode   int    // 0 means ExitGeneral
	errMsg     string // used only when err == nil
	err        error  // when set, Error() delegates to err.Error()
}

// newUserError creates a userError with the given user-facing message.
// Chain builder methods to set additional fields.
func newUserError(msg string) *userError {
	return &userError{msg: msg}
}

func (e *userError) withCode(code string) *userError     { e.code = code; return e }
func (e *userError) withDetail(detail string) *userError { e.detail = detail; return e }
func (e *userError) withSuggestion(s string) *userError  { e.suggestion = s; return e }
func (e *userError) withExitCode(code int) *userError    { e.exitCode = code; return e }
func (e *userError) wrap(err error) *userError           { e.err = err; return e }
func (e *userError) wrapMsg(msg string) *userError       { e.errMsg = msg; return e }

func (e *userError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	if e.errMsg != "" {
		return e.errMsg
	}
	return e.msg
}

func (e *userError) UserMessage() string { return e.msg }
func (e *userError) Detail() string      { return e.detail }
func (e *userError) Suggestion() string  { return e.suggestion }
func (e *userError) Unwrap() error       { return e.err }

// ErrorCode returns the namespaced error code, e.g. "auth.token_missing".
// Empty string means no code was set.
func (e *userError) ErrorCode() string { return e.code }

// UserExitCode returns the specific exit code for this error.
// Distinct from ExitCode() (the silent-exit interface used by HookExitError).
func (e *userError) UserExitCode() int {
	if e.exitCode != 0 {
		return e.exitCode
	}
	return ExitGeneral
}

// errNoForce returns a structured error for when a confirmation prompt cannot
// be shown (no TTY / CI environment) and --force was not passed.
func errNoForce(action string) error {
	return newUserError(fmt.Sprintf("Cannot confirm %q without an interactive terminal.", action)).
		withCode("interactivity.no_force").
		withSuggestion("Pass --force to bypass this confirmation.").
		withExitCode(ExitBadArgs)
}

// nonInteractive reports whether the process is running in a CI/CD environment.
// CI is set by most CI/CD systems to indicate non-interactive pipeline execution.
func nonInteractive() bool {
	return os.Getenv("CI") != ""
}
