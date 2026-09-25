package review

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// safePool is a concurrency-safe pool for RunPass, which releases from
// goroutines.
type safePool struct {
	free chan *sidecar.PoolEntry
}

func newSafePool(ids ...string) *safePool {
	p := &safePool{free: make(chan *sidecar.PoolEntry, len(ids))}
	for _, id := range ids {
		p.free <- &sidecar.PoolEntry{ID: id, RepoPath: "/home/user/repo"}
	}
	return p
}

func (p *safePool) Acquire(ctx context.Context) (*sidecar.PoolEntry, error) {
	select {
	case e := <-p.free:
		return e, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *safePool) Release(e *sidecar.PoolEntry) { p.free <- e }

var promptRe = regexp.MustCompile(`echo (\S+) \| base64 -d`)

// promptOf decodes the prompt a claudeScript pipes to claude.
func promptOf(t *testing.T, script string) string {
	t.Helper()
	m := promptRe.FindStringSubmatch(script)
	assert.Assert(t, m != nil, "no prompt in script: %s", script)
	b, err := base64.StdEncoding.DecodeString(strings.Trim(m[1], "'"))
	assert.NilError(t, err)
	return string(b)
}

func TestRunPass(t *testing.T) {
	t.Parallel()
	pool := newSafePool("sb-1")
	var gotKey string
	exec := func(_ context.Context, _ *sidecar.PoolEntry, script string, env map[string]string, out circleci.OutputFn) (int, error) {
		gotKey = env["ANTHROPIC_API_KEY"]
		if promptOf(t, script) == "fail please" {
			out(circleci.StreamStdout, []byte("partial"))
			out(circleci.StreamStderr, []byte("rate limited\n"))
			return 1, nil
		}
		out(circleci.StreamStderr, []byte("thinking...\n"))
		out(circleci.StreamStdout, []byte("  findings for: "+promptOf(t, script)+"\n"))
		return 0, nil
	}

	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec, []Prompt{
		{Name: "a", Body: "it's \"quoted\" $(rm -rf /)"},
		{Name: "b", Body: "fail please"},
	}, Options{APIKey: "sk-test"})
	assert.NilError(t, err)
	assert.Equal(t, gotKey, "sk-test")
	assert.Equal(t, len(results), 2)

	assert.Equal(t, results[0].Prompt, "a")
	assert.Equal(t, results[0].SidecarID, "sb-1")
	assert.Equal(t, results[0].Output, `findings for: it's "quoted" $(rm -rf /)`)
	assert.Equal(t, results[0].Error, "")

	assert.Equal(t, results[1].Prompt, "b")
	assert.Equal(t, results[1].Output, "partial")
	assert.Equal(t, results[1].Error, "claude exited 1: rate limited")
}

func TestRunPassClaudeMissingStopsPass(t *testing.T) {
	t.Parallel()
	exec := func(context.Context, *sidecar.PoolEntry, string, map[string]string, circleci.OutputFn) (int, error) {
		return exitNotFound, nil
	}

	pool := newSafePool("sb-1", "sb-2")
	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}, {Name: "c", Body: "z"}}, Options{})
	assert.Assert(t, errors.Is(err, ErrClaudeMissing), "got %v", err)
}

func TestRunPassTimeout(t *testing.T) {
	t.Parallel()
	exec := func(ctx context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, _ circleci.OutputFn) (int, error) {
		<-ctx.Done()
		return 0, ctx.Err()
	}

	pool := newSafePool("sb-1")
	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "slow", Body: "x"}}, Options{Timeout: 10 * time.Millisecond})
	assert.NilError(t, err)
	assert.Equal(t, results[0].Error, "timed out after 10ms")
}

func TestRunPassAcquireFailureKeepsStartedResults(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	exec := func(context.Context, *sidecar.PoolEntry, string, map[string]string, circleci.OutputFn) (int, error) {
		return 0, nil
	}
	// One sidecar, held by the first review until the context is cancelled,
	// so the second prompt can never acquire one.
	pool := newSafePool("sb-1")
	blocking := func(ctx context.Context, e *sidecar.PoolEntry, s string, env map[string]string, out circleci.OutputFn) (int, error) {
		cancel()
		return exec(ctx, e, s, env, out)
	}

	results, err := RunPass(ctx, pool.Acquire, pool.Release, blocking, []Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}}, Options{})
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
	assert.Equal(t, len(results), 1)
	assert.Equal(t, results[0].Prompt, "a")
}

func TestClaudeScript(t *testing.T) {
	t.Parallel()
	script := claudeScript("/home/user/my repo", "hi", "claude-sonnet-5")
	assert.Assert(t, strings.Contains(script, "cd '/home/user/my repo'"), script)
	assert.Assert(t, strings.Contains(script, "'claude' '-p' '--output-format' 'text'"), script)
	assert.Assert(t, strings.Contains(script, "'--model' 'claude-sonnet-5'"), script)
	assert.Assert(t, strings.Contains(script, "Bash(git diff:*)"), script)
	assert.Assert(t, !strings.Contains(script, "Edit"), script)
	assert.Equal(t, promptOf(t, script), "hi")
}
