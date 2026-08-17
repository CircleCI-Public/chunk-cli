package oauth

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// TestLoginPrintsURLWithoutBrowserPrompt covers the non-interactive path: with
// no terminal on stdin there is nobody to confirm, so Login must print the
// authorize URL and never reach the browser prompt.
func TestLoginPrintsURLWithoutBrowserPrompt(t *testing.T) {
	t.Setenv(config.EnvXDGStateHome, t.TempDir())
	origIsTerminal := stdinIsTerminal
	stdinIsTerminal = func() bool { return false }
	t.Cleanup(func() { stdinIsTerminal = origIsTerminal })

	// A cancelled context lets Login return as soon as it starts waiting for
	// the callback, after the URL has been written.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var status bytes.Buffer
	_, err := Login(ctx, LoginConfig{BaseURL: "https://circleci.example"}, &status)
	assert.ErrorIs(t, err, context.Canceled)

	out := status.String()
	assert.Assert(t, strings.Contains(out, "Open this URL in your browser"), "missing URL instruction:\n%s", out)
	assert.Assert(t, strings.Contains(out, "https://circleci.example/oauth/authorize?"), "missing authorize URL:\n%s", out)
	assert.Assert(t, !strings.Contains(out, "Press Enter"), "must not prompt to open a browser:\n%s", out)
}
