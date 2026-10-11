package factory

import (
	"context"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/codex"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/sidecar"
)

type execCall struct {
	id     string
	script string
	env    map[string]string
}

// TestPrepareSetsUpTheHarnessOnEverySidecarAfterTheInstall runs Prepare with
// Codex, whose setup is non-empty, and a recording exec.
func TestPrepareSetsUpTheHarnessOnEverySidecarAfterTheInstall(t *testing.T) {
	var mu sync.Mutex
	var calls []execCall
	exec := func(_ context.Context, e *sidecar.PoolEntry, script string, env map[string]string, _ iostream.OutputFn, _ func(string)) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, execCall{e.ID, script, env})
		return 0, nil
	}
	impl := &sidecar.PoolEntry{ID: "impl", RepoPath: "/work"}
	s := &Sidecars{
		Exec:      exec,
		Install:   "make deps",
		Reviewers: []*sidecar.PoolEntry{{ID: "r1", RepoPath: "/work"}, {ID: "r2", RepoPath: "/work"}},
		Implementer: &Implementer{
			Harness: codex.Harness{}, Entry: impl,
			Credential: harness.Credential{EnvVar: codex.EnvAuthJSON, Value: "c2VjcmV0"},
		},
	}
	// The baseline commit that follows needs real git output; only the order
	// of the setup steps matters here.
	_ = s.Prepare(context.Background())

	setup, _ := codex.Harness{}.Prepare(s.Implementer.Credential)
	installAt := map[string]int{}
	setupAt := map[string]int{}
	for i, c := range calls {
		switch {
		case strings.Contains(c.script, "make deps"):
			installAt[c.id] = i
		case c.script == setup:
			setupAt[c.id] = i
			assert.Equal(t, c.env[codex.EnvAuthJSON], "c2VjcmV0", "%s gets the auth file in its setup environment", c.id)
		default:
			_, leaked := c.env[codex.EnvAuthJSON]
			assert.Assert(t, !leaked, "%s: only the setup gets the auth file", c.id)
		}
	}
	for _, id := range []string{"impl", "r1", "r2"} {
		at, ok := setupAt[id]
		assert.Assert(t, ok, "%s was not set up", id)
		assert.Assert(t, at > installAt[id], "%s was set up before its install", id)
	}
}
