package validate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// ResultCache is a read/write store for validate run outcomes.
type ResultCache interface {
	Get(key string) (CachedResult, bool)
	Put(key string, r CachedResult) error
}

// CachedResult records the timestamp of a successful validate run. Only
// successful runs are cached; failures are never stored so the agent always
// retries after a fix, even when the working tree has not changed.
//
// CachedAt is informational: presence of the entry is what marks a run as
// successful, and nothing currently reads the timestamp. It exists as a
// debugging breadcrumb and a hook for a future TTL.
type CachedResult struct {
	CachedAt time.Time `json:"cached_at"`
}

// CacheKey builds a content-addressed key from a name and ordered content
// parts. Parts are length-prefixed before hashing to prevent boundary
// collisions (["ab","c"] and ["a","bc"] produce different keys).
func CacheKey(name string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		writePart(h, p)
	}
	return name + "\x00" + hex.EncodeToString(h.Sum(nil))
}

// CacheKeyInputs collects the working-tree fingerprint plus everything outside
// the tree that can change the outcome of a validate run.
type CacheKeyInputs struct {
	// Worktree fingerprints the tree the run will validate.
	Worktree gitutil.Worktree
	// CommandName is the single command being run, or "" when all commands run.
	CommandName string
	// Commands is the configured command set.
	Commands []config.Command
	// Target identifies where the commands execute: "" for a local run,
	// otherwise an opaque description of the sidecar. Sidecar routing depends on
	// mutable state outside the repo, so it has to participate in the key — a
	// working tree validated against one sidecar says nothing about another.
	Target string
}

// BuildCacheKey constructs the cache key for a validate run from the working-tree
// fingerprint, the serialized commands, and the execution target.
//
// The second return value is false when the fingerprint is the zero Worktree —
// gitutil.Fingerprint could not establish the tree's state — or the commands
// cannot be serialized. Callers must not read or write the cache in that case:
// with no trustworthy git state the key would depend only on the config and
// would therefore stay stable across code changes, turning every subsequent run
// into a false cache hit.
func BuildCacheKey(in CacheKeyInputs) (string, bool) {
	if in.Worktree.Head == "" || in.Worktree.Digest == "" {
		return "", false
	}
	cfgBytes, err := json.Marshal(in.Commands)
	if err != nil {
		return "", false
	}
	return CacheKey(in.CommandName, sha256hex(cfgBytes), in.Target, in.Worktree.Head, in.Worktree.Digest), true
}

// writePart mixes s into h length-prefixed, so that concatenations of different
// parts cannot collide.
func writePart(h io.Writer, s string) {
	_, _ = fmt.Fprintf(h, "%d:", len(s))
	_, _ = io.WriteString(h, s)
}

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
