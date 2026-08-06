package validate

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/envspec"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// --- cacheKey ---

func TestCacheKey_SameName_SameParts_Equal(t *testing.T) {
	a := cacheKey("cmd", "part1", "part2")
	b := cacheKey("cmd", "part1", "part2")
	assert.Equal(t, a, b)
}

func TestCacheKey_DifferentName_NotEqual(t *testing.T) {
	a := cacheKey("cmdA", "part1")
	b := cacheKey("cmdB", "part1")
	assert.Assert(t, a != b)
}

func TestCacheKey_BoundaryCollision_NotEqual(t *testing.T) {
	// Without length-prefixing ["ab","c"] and ["a","bc"] would hash identically.
	a := cacheKey("cmd", "ab", "c")
	b := cacheKey("cmd", "a", "bc")
	assert.Assert(t, a != b, "boundary collision: different part splits must produce different keys")
}

func TestCacheKey_EmptyName(t *testing.T) {
	// "" is the commandName for "run all"; must still produce a stable key.
	a := cacheKey("", "hash1", "sha", "status")
	b := cacheKey("", "hash1", "sha", "status")
	assert.Equal(t, a, b)
}

// --- BuildCacheKey ---

func testConfig() *config.ProjectConfig {
	return &config.ProjectConfig{
		Commands: []config.Command{{Name: "test", Run: "go test ./..."}},
	}
}

// testTree is a stand-in for a real fingerprint; gitutil owns the tests that
// prove a digest actually tracks the working tree.
var testTree = gitutil.Worktree{Head: "abc123", Digest: "deadbeef"}

// buildKey asserts the key could be built and returns it.
func buildKey(t *testing.T, in CacheKeyInputs) string {
	t.Helper()
	key, ok := BuildCacheKey(in)
	assert.Assert(t, ok, "expected a usable cache key")
	return key
}

func TestBuildCacheKey_SameInputs_Equal(t *testing.T) {
	in := CacheKeyInputs{Worktree: testTree, Config: testConfig()}
	assert.Equal(t, buildKey(t, in), buildKey(t, in))
}

func TestBuildCacheKey_DifferentCommandName_NotEqual(t *testing.T) {
	all := buildKey(t, CacheKeyInputs{Worktree: testTree, Config: testConfig()})
	named := buildKey(t, CacheKeyInputs{Worktree: testTree, CommandName: "test", Config: testConfig()})
	assert.Assert(t, all != named)
}

func TestBuildCacheKey_DifferentCommands_NotEqual(t *testing.T) {
	edited := testConfig()
	edited.Commands[0].Run = "go test -race ./..."

	a := buildKey(t, CacheKeyInputs{Worktree: testTree, Config: testConfig()})
	b := buildKey(t, CacheKeyInputs{Worktree: testTree, Config: edited})
	assert.Assert(t, a != b)
}

// TestBuildCacheKey_DifferentEnvironment_NotEqual covers a project that gitignores
// .chunk/: editing the environment block changes what the commands run against
// without touching the working tree, so the config has to reach the key whole or
// the next run reports a hit and never re-runs against the new environment.
func TestBuildCacheKey_DifferentEnvironment_NotEqual(t *testing.T) {
	withEnv := testConfig()
	withEnv.Environment = &envspec.Environment{
		Stack: "go",
		Setup: []envspec.Step{{Name: "install", Command: "go mod download"}},
	}
	changedEnv := testConfig()
	changedEnv.Environment = &envspec.Environment{
		Stack: "go",
		Setup: []envspec.Step{{Name: "install", Command: "go mod download && apt-get install -y libpcap-dev"}},
	}

	base := buildKey(t, CacheKeyInputs{Worktree: testTree, Config: testConfig()})
	a := buildKey(t, CacheKeyInputs{Worktree: testTree, Config: withEnv})
	b := buildKey(t, CacheKeyInputs{Worktree: testTree, Config: changedEnv})

	assert.Assert(t, base != a, "adding an environment must invalidate the key")
	assert.Assert(t, a != b, "changing a setup step must invalidate the key")
}

// TestBuildCacheKey_DifferentWorktree_NotEqual pins the wiring that makes the
// cache safe: both halves of the fingerprint have to reach the key, or a tree
// that has moved on reports a hit for the run that validated the old one.
func TestBuildCacheKey_DifferentWorktree_NotEqual(t *testing.T) {
	base := buildKey(t, CacheKeyInputs{Worktree: testTree, Config: testConfig()})

	newCommit := testTree
	newCommit.Head = "def456"
	edited := testTree
	edited.Digest = "cafebabe"

	assert.Assert(t, base != buildKey(t, CacheKeyInputs{Worktree: newCommit, Config: testConfig()}),
		"a new HEAD must invalidate the key")
	assert.Assert(t, base != buildKey(t, CacheKeyInputs{Worktree: edited, Config: testConfig()}),
		"a changed working tree must invalidate the key")
}

// TestBuildCacheKey_UnusableWorktree_NotOK covers the fail-closed contract: with
// no trustworthy git state the key would depend only on the config, so it would
// stay stable across code changes and turn every later run into a false hit.
func TestBuildCacheKey_UnusableWorktree_NotOK(t *testing.T) {
	tests := []struct {
		name string
		tree gitutil.Worktree
	}{
		{name: "zero fingerprint", tree: gitutil.Worktree{}},
		{name: "no head", tree: gitutil.Worktree{Digest: "deadbeef"}},
		{name: "no digest", tree: gitutil.Worktree{Head: "abc123"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := BuildCacheKey(CacheKeyInputs{Worktree: tt.tree, Config: testConfig()})
			assert.Assert(t, !ok, "an unusable fingerprint must not produce a cache key")
		})
	}
}

// TestBuildCacheKey_DifferentTarget_NotEqual covers switching the active
// sidecar: the working tree is untouched, so only the target distinguishes a
// run validated on one sidecar from one that must still be validated on another.
func TestBuildCacheKey_DifferentTarget_NotEqual(t *testing.T) {
	key := func(target string) string {
		t.Helper()
		return buildKey(t, CacheKeyInputs{Worktree: testTree, Config: testConfig(), Target: target})
	}

	local := key("")
	sidecarA := key("sidecar-a\x00img")
	sidecarB := key("sidecar-b\x00img")

	assert.Assert(t, local != sidecarA, "local and sidecar runs must not share a key")
	assert.Assert(t, sidecarA != sidecarB, "different sidecars must not share a key")
	assert.Equal(t, sidecarA, key("sidecar-a\x00img"))
}
