package review

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
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
	// A released entry and a cancelled context can both be ready; select
	// would pick either, so a cancelled context must win deterministically.
	if err := ctx.Err(); err != nil {
		return nil, err
	}
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
	var gotEnv map[string]string
	exec := func(_ context.Context, _ *sidecar.PoolEntry, script string, env map[string]string, out circleci.OutputFn, _ func(string)) (int, error) {
		gotEnv = env
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
	}, Options{Credential: Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "sk-test"}})
	assert.NilError(t, err)
	assert.Equal(t, len(gotEnv), 1)
	assert.Equal(t, gotEnv["ANTHROPIC_API_KEY"], "sk-test")
	assert.Equal(t, len(results), 2)

	assert.Equal(t, results[0].Prompt, "a")
	assert.Equal(t, results[0].SidecarID, "sb-1")
	assert.Equal(t, results[0].Output, `findings for: it's "quoted" $(rm -rf /)`)
	assert.Equal(t, results[0].Error, "")

	assert.Equal(t, results[1].Prompt, "b")
	assert.Equal(t, results[1].Output, "partial")
	assert.Equal(t, results[1].Error, "claude exited 1: rate limited")
}

func TestRunPassReportsSubmittedCommand(t *testing.T) {
	t.Parallel()
	pool := newSafePool("sb-1")
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, _ circleci.OutputFn, sub func(string)) (int, error) {
		sub("cmd-9")
		return 0, nil
	}

	type submission struct {
		sidecarID, prompt, commandID string
	}
	var got []submission
	var mu sync.Mutex
	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec, []Prompt{{Name: "a", Body: "x"}}, Options{
		OnSubmitted: func(entry *sidecar.PoolEntry, prompt, commandID string) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, submission{entry.ID, prompt, commandID})
		},
	})
	assert.NilError(t, err)
	assert.Equal(t, len(got), 1)
	assert.Equal(t, got[0], submission{"sb-1", "a", "cmd-9"})
}

func TestRunPassWithoutOnSubmittedPassesNilHook(t *testing.T) {
	t.Parallel()
	pool := newSafePool("sb-1")
	var gotHook bool
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, _ circleci.OutputFn, sub func(string)) (int, error) {
		gotHook = sub != nil
		return 0, nil
	}

	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec, []Prompt{{Name: "a", Body: "x"}}, Options{})
	assert.NilError(t, err)
	assert.Assert(t, !gotHook, "an Execer must be able to skip submission reporting when nobody asked for it")
}

func TestRunPassClaudeMissingStopsPass(t *testing.T) {
	t.Parallel()
	exec := func(context.Context, *sidecar.PoolEntry, string, map[string]string, circleci.OutputFn, func(string)) (int, error) {
		return exitClaudeMissing, nil
	}

	pool := newSafePool("sb-1", "sb-2")
	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}, {Name: "c", Body: "z"}}, Options{})
	assert.Assert(t, errors.Is(err, ErrClaudeMissing), "got %v", err)
}

func TestRunPassShellNotFoundIsOneFailedReview(t *testing.T) {
	t.Parallel()
	exec := func(_ context.Context, _ *sidecar.PoolEntry, script string, _ map[string]string, _ circleci.OutputFn, _ func(string)) (int, error) {
		if promptOf(t, script) == "x" {
			return 127, nil
		}
		return 0, nil
	}

	pool := newSafePool("sb-1", "sb-2")
	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}}, Options{})
	assert.NilError(t, err)
	assert.Equal(t, len(results), 2)
	assert.Equal(t, results[0].Error, "claude exited 127")
	assert.Equal(t, results[1].Error, "")
}

func TestRunPassSendsOnlyTheGivenCredential(t *testing.T) {
	t.Parallel()
	var gotEnv map[string]string
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, env map[string]string, _ circleci.OutputFn, _ func(string)) (int, error) {
		gotEnv = env
		return 0, nil
	}

	pool := newSafePool("sb-1")
	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}},
		Options{Credential: Credential{EnvVar: "CLAUDE_CODE_OAUTH_TOKEN", Value: "sk-ant-oat01-tok"}})
	assert.NilError(t, err)
	assert.Equal(t, len(gotEnv), 1)
	assert.Equal(t, gotEnv["CLAUDE_CODE_OAUTH_TOKEN"], "sk-ant-oat01-tok")
	_, hasKey := gotEnv["ANTHROPIC_API_KEY"]
	assert.Assert(t, !hasKey, "env: %v", gotEnv)
}

func TestRunPassCredentialRejectedStopsPass(t *testing.T) {
	t.Parallel()
	// The 401 lands on stdout, while stderr carries an unrelated warning.
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, out circleci.OutputFn, _ func(string)) (int, error) {
		out(circleci.StreamStderr, []byte("workspace has not been trusted\n"))
		out(circleci.StreamStdout, []byte("Failed to authenticate. API Error: 401 OAuth access token is invalid.\n"))
		return 1, nil
	}

	pool := newSafePool("sb-1", "sb-2")
	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}, {Name: "c", Body: "z"}}, Options{})
	assert.Assert(t, errors.Is(err, ErrCredentialRejected), "got %v", err)
}

// A review that merely quotes a 401 is not an authentication failure, so the
// pass must not be cut short by it.
func TestRunPassSuccessfulReviewQuotingA401(t *testing.T) {
	t.Parallel()
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, out circleci.OutputFn, _ func(string)) (int, error) {
		out(circleci.StreamStdout, []byte("the handler failed to authenticate and returns 401\n"))
		return 0, nil
	}

	pool := newSafePool("sb-1")
	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}}, Options{})
	assert.NilError(t, err)
	assert.Equal(t, results[0].Error, "")
}

func TestRunPassClaudeMissingNoSpuriousFailures(t *testing.T) {
	t.Parallel()
	// One goroutine returns exitClaudeMissing immediately; the rest block until the
	// context is canceled. The context-canceled results must be attributed to
	// ErrClaudeMissing, not shown as generic "exec: context canceled" failures.
	var first atomic.Bool
	exec := func(ctx context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, _ circleci.OutputFn, _ func(string)) (int, error) {
		if first.CompareAndSwap(false, true) {
			return exitClaudeMissing, nil
		}
		<-ctx.Done()
		return 0, ctx.Err()
	}

	pool := newSafePool("sb-1", "sb-2", "sb-3")
	var mu sync.Mutex
	var failedEvents []string
	record := func(e ProgressEvent) {
		if e.State == StateFailed {
			mu.Lock()
			failedEvents = append(failedEvents, e.Prompt+": "+e.Error)
			mu.Unlock()
		}
	}

	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}, {Name: "c", Body: "z"}},
		Options{ProgressFn: record})
	assert.Assert(t, errors.Is(err, ErrClaudeMissing), "got %v", err)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, len(failedEvents), 0, "expected no StateFailed events when claude is missing, got: %v", failedEvents)
}

func TestRunPassTimeout(t *testing.T) {
	t.Parallel()
	exec := func(ctx context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, _ circleci.OutputFn, _ func(string)) (int, error) {
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
	exec := func(context.Context, *sidecar.PoolEntry, string, map[string]string, circleci.OutputFn, func(string)) (int, error) {
		return 0, nil
	}
	// One sidecar; the first review cancels the context, so the second
	// prompt can never acquire one.
	pool := newSafePool("sb-1")
	blocking := func(ctx context.Context, e *sidecar.PoolEntry, s string, env map[string]string, out circleci.OutputFn, sub func(string)) (int, error) {
		cancel()
		return exec(ctx, e, s, env, out, sub)
	}

	results, err := RunPass(ctx, pool.Acquire, pool.Release, blocking, []Prompt{{Name: "a", Body: "x"}, {Name: "b", Body: "y"}}, Options{})
	assert.Assert(t, errors.Is(err, context.Canceled), "got %v", err)
	assert.Equal(t, len(results), 1)
	assert.Equal(t, results[0].Prompt, "a")
}

func TestRunPassProgressFn(t *testing.T) {
	t.Parallel()
	pool := newSafePool("sb-1", "sb-2")
	exec := func(_ context.Context, _ *sidecar.PoolEntry, script string, _ map[string]string, out circleci.OutputFn, _ func(string)) (int, error) {
		out(circleci.StreamStdout, []byte("ok"))
		if promptOf(t, script) == "fail" {
			return 1, nil
		}
		return 0, nil
	}

	type ev struct {
		prompt string
		state  PromptState
	}
	var mu sync.Mutex
	var events []ev
	record := func(e ProgressEvent) {
		mu.Lock()
		events = append(events, ev{e.Prompt, e.State})
		mu.Unlock()
	}

	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "pass", Body: "x"}, {Name: "fail", Body: "fail"}},
		Options{ProgressFn: record})
	assert.NilError(t, err)

	mu.Lock()
	defer mu.Unlock()

	// Both prompts must appear as queued before any running event.
	queued := map[string]bool{}
	for _, e := range events {
		if e.state == StateQueued {
			queued[e.prompt] = true
		}
	}
	assert.Assert(t, queued["pass"] && queued["fail"], "want both prompts queued, got %v", events)

	// Each prompt must end in a terminal state.
	terminal := map[string]PromptState{}
	for _, e := range events {
		if e.state == StateDone || e.state == StateFailed {
			terminal[e.prompt] = e.state
		}
	}
	assert.Equal(t, terminal["pass"], StateDone)
	assert.Equal(t, terminal["fail"], StateFailed)
}

func TestClaudeScript(t *testing.T) {
	t.Parallel()
	script := claudeScriptWithTools("/home/user/my repo", "hi", "claude-sonnet-5", allowedTools, false)
	assert.Assert(t, strings.Contains(script, "cd '/home/user/my repo'"), script)
	assert.Assert(t, strings.Contains(script, "'claude' '-p' '--output-format' 'text'"), script)
	assert.Assert(t, strings.Contains(script, "'--model' 'claude-sonnet-5'"), script)
	assert.Assert(t, strings.Contains(script, "Bash(git diff:*)"), script)
	assert.Assert(t, !strings.Contains(script, "Edit"), script)
	assert.Equal(t, promptOf(t, script), "hi")
}

func TestClaudeScriptStructuredAsksForTheFindingsSchema(t *testing.T) {
	t.Parallel()
	script := claudeScriptWithTools("/home/user/repo", "hi", "", allowedTools, true)
	assert.Assert(t, strings.Contains(script, "'claude' '-p' '--output-format' 'json'"), script)
	assert.Assert(t, strings.Contains(script, "'--json-schema' "+sidecar.ShellEscape(FindingsSchema)), script)
	assert.Equal(t, promptOf(t, script), "hi", "the prompt is sent as written")
}

func TestRunPassStructuredFindings(t *testing.T) {
	t.Parallel()
	pool := newSafePool("sb-1")
	exec := func(_ context.Context, _ *sidecar.PoolEntry, script string, _ map[string]string, out circleci.OutputFn, _ func(string)) (int, error) {
		if promptOf(t, script) == "prose" {
			out(circleci.StreamStdout, []byte("Just prose."))
			return 0, nil
		}
		out(circleci.StreamStdout, []byte(claudeJSON(t, map[string]any{
			"review":   "One issue.",
			"findings": []map[string]any{{"file": "a.go", "line": 1, "severity": "high", "body": "nil deref"}},
		})))
		return 0, nil
	}

	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "ok", Body: "structured"}, {Name: "bad", Body: "prose"}},
		Options{StructuredFindings: true})

	assert.NilError(t, err)
	assert.Equal(t, len(results), 2)
	assert.Equal(t, results[0].Error, "")
	assert.Equal(t, results[0].Output, "One issue.", "the prose replaces the JSON result")
	assert.Equal(t, len(results[0].Parsed.Findings), 1)
	assert.Equal(t, results[0].Parsed.Findings[0].File, "a.go")
	assert.Assert(t, strings.Contains(results[1].Error, "read claude's result"), results[1].Error)
}

func TestRunPassStructuredResultOverTheCapFailsWithAClearError(t *testing.T) {
	t.Parallel()
	pool := newSafePool("sb-1")
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, _ map[string]string, out circleci.OutputFn, _ func(string)) (int, error) {
		out(circleci.StreamStdout, []byte(`{"type":"result","result":"`))
		out(circleci.StreamStdout, []byte(strings.Repeat("x", maxStructuredOutputBytes)))
		return 0, nil
	}

	results, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "big", Body: "x"}}, Options{StructuredFindings: true})

	assert.NilError(t, err)
	assert.Equal(t, len(results), 1)
	assert.Assert(t, strings.Contains(results[0].Error, "is over"), results[0].Error)
	assert.Assert(t, !strings.Contains(results[0].Error, "read claude's result"), results[0].Error)
}

func TestRunPassForwardsACustomBaseURL(t *testing.T) {
	t.Parallel()
	var gotEnv map[string]string
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, env map[string]string, _ circleci.OutputFn, _ func(string)) (int, error) {
		gotEnv = env
		return 0, nil
	}

	pool := newSafePool("sb-1")
	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}},
		Options{Credential: Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "sk-test"}, BaseURL: "https://llm-gateway.example"})
	assert.NilError(t, err)
	assert.Equal(t, gotEnv["ANTHROPIC_BASE_URL"], "https://llm-gateway.example")
}

func TestRunPassOmitsTheDefaultBaseURL(t *testing.T) {
	t.Parallel()
	var gotEnv map[string]string
	exec := func(_ context.Context, _ *sidecar.PoolEntry, _ string, env map[string]string, _ circleci.OutputFn, _ func(string)) (int, error) {
		gotEnv = env
		return 0, nil
	}

	pool := newSafePool("sb-1")
	_, err := RunPass(context.Background(), pool.Acquire, pool.Release, exec,
		[]Prompt{{Name: "a", Body: "x"}},
		Options{Credential: Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "sk-test"}, BaseURL: "https://api.anthropic.com/"})
	assert.NilError(t, err)
	assert.Equal(t, len(gotEnv), 1)
}
