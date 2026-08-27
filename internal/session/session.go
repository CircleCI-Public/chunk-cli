// Package session tracks the identity of the agent session driving a chunk
// invocation, so per-session state (which sidecar this session owns) stays
// separate when several sessions share one working tree.
package session

import (
	"context"
	"os"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// EnvClaudeSessionID is set by Claude Code in the environment of every command
// it runs. It is what makes a session identifiable outside the Stop hook, where
// the session ID arrives in the hook payload instead: an agent running
// `chunk sidecar sync` or `chunk validate --remote` itself would otherwise look
// anonymous and share one sidecar with every other session in the repo.
//
// It lives here rather than in the config package because, like the agent
// signals in internal/telemetry, it is a variable another tool sets rather than
// one of chunk's own. config.EnvChunkSessionID, which users set themselves, does
// live there.
const EnvClaudeSessionID = "CLAUDE_CODE_SESSION_ID"

// key is unexported so no other package can construct it, guaranteeing no collisions.
type key struct{}

// WithID returns a new context carrying the given Claude session ID.
func WithID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, key{}, id)
}

// IDFromCtx returns the Claude session ID stored in ctx, or "" if not set.
func IDFromCtx(ctx context.Context) string {
	id, _ := ctx.Value(key{}).(string)
	return id
}

// IDFromEnv returns the session ID advertised by the environment, or "" when
// nothing in it names a session.
//
// config.EnvChunkSessionID wins over the agent's own variable, so an agent chunk
// does not recognise — or a test — can still pin a session identity by hand.
//
// The value is sanitised because it becomes part of a state file name and of a
// generated sidecar name: everything outside [A-Za-z0-9._-] is dropped and the
// result is capped. Real session IDs are UUIDs, so in practice this changes
// nothing; it only stops a hostile or malformed value from escaping the state
// directory.
func IDFromEnv() string {
	for _, name := range []string{config.EnvChunkSessionID, EnvClaudeSessionID} {
		if id := sanitize(os.Getenv(name)); id != "" {
			return id
		}
	}
	return ""
}

// maxIDLen caps a sanitised session ID. A UUID is 36 characters; the extra room
// leaves other agents' longer IDs intact without letting one blow past the
// filesystem's name limit once the sidecar and branch parts are added.
const maxIDLen = 64

func sanitize(id string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(id) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
		if b.Len() >= maxIDLen {
			break
		}
	}
	return b.String()
}
