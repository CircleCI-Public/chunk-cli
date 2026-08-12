package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/spf13/cobra"

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

// silentExitError carries an exit code. main.go detects ExitCode() and calls
// os.Exit directly, skipping further error printing. Use after writing your
// own message to stderr.
type silentExitError struct{ code int }

func (e *silentExitError) Error() string { return "" }
func (e *silentExitError) ExitCode() int { return e.code }

var errSilentExit error = &silentExitError{code: 1}

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

// outdatedSidecarAPI maps an unsupported output format to guidance that points
// the right way. The API is behind this binary, not ahead of it, so telling
// someone to upgrade chunk would send them in exactly the wrong direction —
// there is nothing newer to install, and installing it again would not help.
func outdatedSidecarAPI(err error) error {
	if !errors.Is(err, circleci.ErrOutputFormatUnsupported) {
		return nil
	}
	return newUserError("This build of chunk needs a newer CircleCI sidecar API.").
		withCode("sidecar.output_format_unsupported").
		withDetail("The API streamed command output in a format this build no longer reads.").
		withSuggestion("The API has not been updated yet. Install the latest release with 'chunk upgrade', " +
			"or if you built this from source, switch back to a released build until the API catches up.").
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
	if errors.Is(err, context.DeadlineExceeded) {
		return &userError{
			msg:        "Request timed out.",
			suggestion: "Try again. This request may time out on initial key registration.",
			err:        err,
		}
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
	hideDetail bool   // suppress the detail line entirely, see withoutDetail
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

// withoutDetail states that the message and suggestion say everything, so no
// detail line should be shown. Without it an empty detail falls back to the
// wrapped error, which leaks Go-level wrapping like "exec: 410 Gone — ..." and,
// when the message was translated from that same error, repeats itself.
func (e *userError) withoutDetail() *userError { e.hideDetail = true; return e }

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

// HideDetail reports that this error is fully described by its message and
// suggestion, so the display must not fall back to the wrapped error.
func (e *userError) HideDetail() bool { return e.hideDetail }

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

// groupRunE is the RunE for group (parent) commands that have no action of
// their own. It shows help when invoked with no arguments and returns a
// structured error for unknown subcommands.
func groupRunE(cmd *cobra.Command, args []string) error {
	if len(args) == 0 {
		if err := cmd.Help(); err != nil {
			return fmt.Errorf("show help: %w", err)
		}
		return nil
	}
	return newUserError(fmt.Sprintf("%q is not a %s command. Run '%s --help' for available commands.", args[0], cmd.Name(), cmd.CommandPath())).
		withCode("command.unknown").
		withExitCode(ExitBadArgs)
}

// errNoForce returns a structured error for when a confirmation prompt cannot
// be shown (no TTY / CI environment) and --force was not passed.
func errNoForce(action string) error {
	return newUserError(fmt.Sprintf("Cannot confirm %q without an interactive terminal.", action)).
		withCode("interactivity.no_force").
		withSuggestion("Pass --force to bypass this confirmation.").
		withExitCode(ExitBadArgs)
}

// GoneError returns a formatted error when err contains a 410 Gone status, which
// the API uses for two distinct conditions: this CLI being too old for the API,
// and a sidecar being too old for the API. Returns nil for any other error. Use
// at the top of main's error path to intercept 410s before generic handling.
func GoneError(err error) error {
	var se *circleci.StatusError
	if !errors.As(err, &se) || se.StatusCode != http.StatusGone {
		return nil
	}

	// A 410 covers two unrelated conditions with opposite remedies, told apart by
	// the server's wording; see circleci.SidecarOutOfDate. The server's message
	// already names the remedy, so it is shown rather than replaced with a guess.
	if circleci.SidecarOutOfDate(err) {
		// Deliberately no detail. The server's message states the condition and
		// the remedy, both of which are already the message and the suggestion
		// here, so including it says the same thing three times over.
		return newUserError("This sidecar is out of date.").
			withCode("sidecar.out_of_date").
			withSuggestion("Recreate it with: chunk sidecar create").
			withExitCode(ExitAPIError).
			withoutDetail().
			wrap(err)
	}

	detail := se.ServerMessage
	if detail == "" {
		detail = "This server no longer supports this version of chunk CLI."
	}
	return newUserError("chunk CLI is out of date.").
		withCode("cli.upgrade_required").
		withDetail(detail).
		withSuggestion("Run `chunk upgrade` to get the latest version.").
		withExitCode(ExitAPIError).
		wrap(err)
}

// malformedProjectConfigError reports a .chunk/config.json that exists but does
// not parse. Write paths return it instead of continuing, because they would
// otherwise save an empty config over a file they could not read.
func malformedProjectConfigError(err error) *userError {
	return newUserError(msgMalformedProjectConfig).
		withCode("config.parse_failed").
		withDetail(detailMalformedProjectConfig).
		withSuggestion(suggestionFixProjectConfig).
		wrap(err)
}

// nonInteractive reports whether the process is running in a CI/CD environment.
// CI is set by most CI/CD systems to indicate non-interactive pipeline execution.
func nonInteractive() bool {
	return os.Getenv("CI") != ""
}
