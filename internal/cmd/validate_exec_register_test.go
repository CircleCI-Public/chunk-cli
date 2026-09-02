package cmd

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// The remote exec path now submits, registers the command with the watch daemon,
// then streams. Registration is best-effort, and this is the regression that
// matters most in the whole feature: this code runs inside a Stop hook, in front
// of a command the developer is waiting on, on machines where the daemon is very
// often not running at all. Anything other than "identical behaviour, no delay"
// here is a bug that shows up as a slow agent, not as a missing logs pane.
func TestExecFnWithoutDaemonBehavesIdentically(t *testing.T) {
	// An empty watchd dir: no socket, so every registration fails to connect.
	t.Setenv("CHUNK_WATCHD_DIR", t.TempDir())

	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	var out, errOut strings.Builder
	streams := iostream.Streams{Out: &out, Err: &errOut}
	execFn, _, err := newExecFn(
		context.Background(), client, "sidecar-123", "", t.TempDir(),
		nil, config.ResolvedConfig{}, streams,
	)
	assert.NilError(t, err)

	start := time.Now()
	stdout, stderr, exitCode, err := execFn(context.Background(), "echo hello")
	elapsed := time.Since(start)

	assert.NilError(t, err, "a missing daemon must not fail the command")
	assert.Check(t, cmp.Equal(exitCode, 0))
	// Output is streamed to streams as it arrives, so the returned strings stay
	// empty — callers rely on that to avoid printing everything twice.
	assert.Check(t, cmp.Equal(stdout, ""))
	assert.Check(t, cmp.Equal(stderr, ""))

	// A failed dial to a nonexistent unix socket is refused immediately; if this
	// ever starts waiting out the timeout, every hook on a machine without a
	// daemon pays it on every command.
	assert.Check(t, elapsed < 2*time.Second,
		"registration with no daemon should not stall the command (took %s)", elapsed)
}

// Submission failing must surface as the command's error, not be masked by the
// registration that follows it.
func TestExecFnSubmitFailureIsReported(t *testing.T) {
	t.Setenv("CHUNK_WATCHD_DIR", t.TempDir())

	// A server that rejects everything, so SubmitExec fails.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	streams := iostream.Streams{Out: io.Discard, Err: io.Discard}
	execFn, _, err := newExecFn(
		context.Background(), client, "sidecar-123", "", t.TempDir(),
		nil, config.ResolvedConfig{}, streams,
	)
	assert.NilError(t, err)

	_, _, _, err = execFn(context.Background(), "echo hello")
	assert.Check(t, err != nil, "a rejected submission must return an error")
}

func TestRemoteCommandLabelTruncatesOnRuneBoundaries(t *testing.T) {
	label := remoteCommandLabel("cd /w && " + strings.Repeat("é", 200))
	if !utf8.ValidString(label) {
		t.Errorf("label is not valid UTF-8: %q", label)
	}
	if got := utf8.RuneCountInString(label); got != 120 {
		t.Errorf("label has %d runes, want 120", got)
	}
}
