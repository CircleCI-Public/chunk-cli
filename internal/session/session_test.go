package session

import (
	"context"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

func TestIDFromEnvEmptyWhenUnset(t *testing.T) {
	t.Setenv(config.EnvChunkSessionID, "")
	t.Setenv(EnvClaudeSessionID, "")

	assert.Equal(t, IDFromEnv(), "")
}

func TestIDFromEnvReadsAgentVar(t *testing.T) {
	t.Setenv(config.EnvChunkSessionID, "")
	t.Setenv(EnvClaudeSessionID, "3e4cd11d-b9d0-4d63-b01e-b244f910924c")

	assert.Equal(t, IDFromEnv(), "3e4cd11d-b9d0-4d63-b01e-b244f910924c")
}

// TestIDFromEnvExplicitOverrideWins covers pinning the identity by hand, which
// is the escape hatch for agents chunk does not recognise.
func TestIDFromEnvExplicitOverrideWins(t *testing.T) {
	t.Setenv(config.EnvChunkSessionID, "pinned")
	t.Setenv(EnvClaudeSessionID, "from-claude")

	assert.Equal(t, IDFromEnv(), "pinned")
}

// TestIDFromEnvSanitises matters because the ID becomes part of a state file
// name: a value carrying path separators must not be able to steer a write out
// of the state directory.
func TestIDFromEnvSanitises(t *testing.T) {
	t.Setenv(config.EnvChunkSessionID, " ../../etc/passwd\n")

	got := IDFromEnv()
	assert.Assert(t, !strings.ContainsAny(got, "/\\ \n"), "got %q", got)
	assert.Equal(t, got, "....etcpasswd")
}

func TestIDFromEnvCapsLength(t *testing.T) {
	t.Setenv(config.EnvChunkSessionID, strings.Repeat("a", maxIDLen*2))

	assert.Equal(t, len(IDFromEnv()), maxIDLen)
}

func TestIDFromCtxRoundTrip(t *testing.T) {
	ctx := WithID(context.Background(), "sess-1")

	assert.Equal(t, IDFromCtx(ctx), "sess-1")
	assert.Equal(t, IDFromCtx(context.Background()), "")
}
