package cmd

import (
	"errors"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// Flows and plans work from this machine, so a remote daemon is refused, and
// before the TTY check: the reason is the same wherever chunk watch is run.
func TestWatchRefusesARemoteDaemon(t *testing.T) {
	t.Setenv("CHUNK_DAEMON_REMOTE_ADDR", "127.0.0.1:1")
	t.Setenv("CHUNK_DAEMON_TCP_TOKEN", "tok")

	cmd := newWatchCmd()
	err := cmd.RunE(cmd, nil)
	ue, ok := errors.AsType[*userError](err)
	assert.Assert(t, ok, "not a user error: %v", err)
	assert.Check(t, cmp.Equal(ue.ErrorCode(), "watch.remote_unsupported"))
	assert.Check(t, cmp.Contains(ue.Suggestion(), "Unset CHUNK_DAEMON_REMOTE_ADDR"))
}
