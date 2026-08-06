package validate

import (
	"encoding/json"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/hashutil"
)

// CachedResult records the timestamp of a successful validate run. Only
// successful runs are cached; failures are never stored so the agent always
// retries after a fix, even when the working tree has not changed.
//
// CachedAt is a debugging breadcrumb: presence of the entry is what marks a run
// as successful, and expiry works off the entry file's modification time, which
// records the same instant. It is here so a cache directory can be read by eye.
type CachedResult struct {
	CachedAt time.Time `json:"cached_at"`
}

// cacheKey builds a content-addressed key from a name and ordered content parts.
// The name stays in the clear so entries can be told apart by eye; everything
// else is hashed.
func cacheKey(name string, parts ...string) string {
	return name + "\x00" + hashutil.SumParts(parts...)
}

// CacheKeyInputs collects the working-tree fingerprint plus everything outside
// the tree that can change the outcome of a validate run.
type CacheKeyInputs struct {
	// Worktree fingerprints the tree the run will validate.
	Worktree gitutil.Worktree
	// CommandName is the single command being run, or "" when all commands run.
	CommandName string
	// Config is the project config driving the run, hashed whole. The commands
	// are the obvious input, but the environment block decides what those
	// commands run against, and a project that gitignores .chunk/ gets no
	// invalidation from the working-tree digest when any of it changes.
	Config *config.ProjectConfig
	// Target identifies where the commands execute: "" for a local run,
	// otherwise an opaque description of the sidecar. Sidecar routing depends on
	// mutable state outside the repo, so it has to participate in the key — a
	// working tree validated against one sidecar says nothing about another.
	Target string
}

// BuildCacheKey constructs the cache key for a validate run from the working-tree
// fingerprint, the serialized project config, and the execution target.
//
// The second return value is false when the fingerprint is the zero Worktree —
// gitutil.Fingerprint could not establish the tree's state — or the config cannot
// be serialized. Callers must not read or write the cache in that case: with no
// trustworthy git state the key would depend only on the config and would
// therefore stay stable across code changes, turning every subsequent run into a
// false cache hit.
func BuildCacheKey(in CacheKeyInputs) (string, bool) {
	if in.Worktree.Head == "" || in.Worktree.Digest == "" {
		return "", false
	}
	cfgBytes, err := json.Marshal(in.Config)
	if err != nil {
		return "", false
	}
	return cacheKey(in.CommandName, string(cfgBytes), in.Target, in.Worktree.Head, in.Worktree.Digest), true
}
