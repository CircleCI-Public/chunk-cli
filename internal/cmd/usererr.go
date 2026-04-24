package cmd

import (
	"errors"
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

const suggestionReauth = "Check your CircleCI token and try again."

func notAuthorized(action string, err error) error {
	if !errors.Is(err, circleci.ErrNotAuthorized) {
		return nil
	}
	return &userError{
		msg:        fmt.Sprintf("Not authorized to %s.", action),
		suggestion: suggestionReauth,
		err:        err,
	}
}

func sshSessionError(err error) error {
	if e, ok := errors.AsType[*sidecar.KeyNotFoundError](err); ok {
		return &userError{
			msg:        fmt.Sprintf("SSH key not found: %s", e.Path),
			suggestion: fmt.Sprintf("Generate one with: ssh-keygen -t ed25519 -f %s\nOr pass --identity-file to use an existing key.", e.Path),
			err:        err,
		}
	}
	if e, ok := errors.AsType[*sidecar.PublicKeyNotFoundError](err); ok {
		return &userError{
			msg:        fmt.Sprintf("SSH public key not found: %s", e.KeyPath),
			suggestion: fmt.Sprintf("Generate a new keypair with: ssh-keygen -t ed25519 -f %s", e.IdentityFile),
			err:        err,
		}
	}
	if errors.Is(err, sidecar.ErrAuthSockNotSet) {
		return &userError{
			msg:        "SSH agent not available.",
			suggestion: "Set " + config.EnvSSHAuthSock + " or pass --identity-file.",
			err:        err,
		}
	}
	return nil
}

// userError is a structured error with a user-facing message and optional
// detail/suggestion for display. For the technical error string, set err when
// you have an underlying error to wrap (its Error() string is used), or set
// errMsg when you only have a string. Do not set both; err takes precedence.
type userError struct {
	msg        string
	detail     string
	suggestion string
	errMsg     string // used only when err == nil
	err        error  // when set, Error() delegates to err.Error()
}

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
