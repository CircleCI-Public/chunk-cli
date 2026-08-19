package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

// The API being behind this binary must not be reported as "upgrade chunk":
// there is nothing newer to install, and that advice sends someone in exactly
// the wrong direction.
func TestOutdatedSidecarAPIPointsTheRightWay(t *testing.T) {
	err := outdatedSidecarAPI(fmt.Errorf("remote test: %w", circleci.ErrOutputFormatUnsupported))
	assert.Assert(t, err != nil, "the sentinel must be recognised through a wrapper")

	um, ok := err.(interface{ UserMessage() string })
	assert.Assert(t, ok)
	assert.Check(t, strings.Contains(um.UserMessage(), "needs a newer"), "got %q", um.UserMessage())
	assert.Check(t, !strings.Contains(strings.ToLower(um.UserMessage()), "upgrade chunk"),
		"the message must not tell someone to upgrade a binary that is already ahead: %q", um.UserMessage())

	s, ok := err.(interface{ Suggestion() string })
	assert.Assert(t, ok)
	assert.Check(t, strings.Contains(s.Suggestion(), "has not been updated yet"), "got %q", s.Suggestion())

	assert.Check(t, outdatedSidecarAPI(errors.New("something else")) == nil,
		"unrelated errors must pass through unmapped")
	assert.Check(t, outdatedSidecarAPI(nil) == nil)
}

// A 410 is used for two conditions with opposite remedies. Telling someone to
// upgrade the CLI when their *sidecar* is the stale one sends them to a version
// that fails identically.
func TestGoneErrorDistinguishesStaleSidecarFromStaleCLI(t *testing.T) {
	t.Run("stale sidecar", func(t *testing.T) {
		err := GoneError(&circleci.StatusError{
			Op:            "stream command output",
			StatusCode:    http.StatusGone,
			ServerMessage: "sidecar is out of date; delete and recreate with: chunk sidecar create",
		})
		assert.Assert(t, err != nil)

		um := err.(interface{ UserMessage() string }).UserMessage()
		s := err.(interface{ Suggestion() string }).Suggestion()
		assert.Check(t, strings.Contains(um, "sidecar is out of date"), "got %q", um)
		assert.Check(t, strings.Contains(s, "chunk sidecar create"), "got %q", s)
		assert.Check(t, !strings.Contains(s, "chunk upgrade"),
			"upgrading the CLI does not fix a stale sidecar: %q", s)

		// The server's message states both the condition and the remedy, so
		// echoing it as detail would print the same thing three times. The
		// suppression must be explicit, or the display falls back to the wrapped
		// error and leaks "410 Gone" wrapping instead.
		assert.Equal(t, err.(interface{ Detail() string }).Detail(), "",
			"a translated message must not also repeat the server's")
		assert.Check(t, err.(interface{ HideDetail() bool }).HideDetail(),
			"an empty detail must be declared, not merely unset")
	})

	t.Run("stale CLI", func(t *testing.T) {
		err := GoneError(&circleci.StatusError{
			StatusCode:    http.StatusGone,
			ServerMessage: "this endpoint has been removed",
		})
		assert.Assert(t, err != nil)
		um := err.(interface{ UserMessage() string }).UserMessage()
		s := err.(interface{ Suggestion() string }).Suggestion()
		assert.Check(t, strings.Contains(um, "chunk CLI is out of date"), "got %q", um)
		assert.Check(t, strings.Contains(s, "chunk upgrade"), "got %q", s)
	})

	t.Run("non-410 passes through", func(t *testing.T) {
		assert.Check(t, GoneError(&circleci.StatusError{StatusCode: http.StatusNotFound}) == nil)
		assert.Check(t, GoneError(errors.New("nope")) == nil)
	})
}

// A 404 or 409 from a sidecar-scoped call used to reach users as bare status
// text under "An unknown error occurred", which named neither the sidecar nor
// what to do about it.
func TestSidecarUnavailableNamesTheSidecarAndTheRemedy(t *testing.T) {
	const id = "ba600ef3-907b-412d-bd01-57752350836f"

	t.Run("a deleted sidecar", func(t *testing.T) {
		err := sidecarUnavailable(id, &circleci.StatusError{
			Op: "exec", StatusCode: http.StatusNotFound, ServerMessage: "Not found",
		})
		assert.Assert(t, err != nil, "a 404 must be recognised")
		var ue *userError
		assert.Assert(t, errors.As(err, &ue), "expected *userError, got %T", err)
		assert.Assert(t, strings.Contains(ue.UserMessage(), id), "the message must name the sidecar: %q", ue.UserMessage())
		assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk sidecar list"))
		assert.Equal(t, ue.UserExitCode(), ExitNotFound)
	})

	t.Run("a paused sidecar", func(t *testing.T) {
		err := sidecarUnavailable(id, &circleci.StatusError{
			Op: "exec", StatusCode: http.StatusConflict, ServerMessage: "sidecar is paused",
		})
		assert.Assert(t, err != nil, "a paused 409 must be recognised")
		var ue *userError
		assert.Assert(t, errors.As(err, &ue), "expected *userError, got %T", err)
		assert.Assert(t, strings.Contains(ue.UserMessage(), "paused"), "got %q", ue.UserMessage())
		// No route resumes a sidecar, so a replacement is the only remedy the
		// caller can act on.
		assert.Assert(t, strings.Contains(ue.Suggestion(), "chunk sidecar create"))
		assert.Equal(t, ue.UserExitCode(), ExitAPIError)
	})

	t.Run("anything else falls through", func(t *testing.T) {
		cases := []error{
			errors.New("dial tcp: connection refused"),
			// An unknown route, not a missing sidecar.
			&circleci.StatusError{Op: "exec", StatusCode: http.StatusNotFound, ServerMessage: "Route Not Found."},
			// A conflict this helper has no advice for.
			&circleci.StatusError{Op: "exec", StatusCode: http.StatusConflict, ServerMessage: "command already running"},
		}
		for _, c := range cases {
			assert.Assert(t, sidecarUnavailable(id, c) == nil, "must not claim to handle %v", c)
		}
	})
}
