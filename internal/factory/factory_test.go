package factory

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

func TestParseReviewReadsStructuredOutput(t *testing.T) {
	t.Parallel()
	v, fb, err := ParseReview(`{"type":"result","is_error":false,"result":"","structured_output":{"verdict":"warn","feedback":" rename x "}}`)
	assert.NilError(t, err)
	assert.Equal(t, v, VerdictWarn)
	assert.Equal(t, fb, "rename x")
}

func TestParseReviewFallsBackToResultText(t *testing.T) {
	t.Parallel()
	v, _, err := ParseReview(`{"is_error":false,"result":"{\"verdict\":\"approved\",\"feedback\":\"\"}"}`)
	assert.NilError(t, err)
	assert.Equal(t, v, VerdictApproved)
}

func TestParseReviewRejectsBadAnswers(t *testing.T) {
	t.Parallel()
	for name, out := range map[string]string{
		"not json":        "looks good to me",
		"claude error":    `{"is_error":true,"result":"overloaded"}`,
		"unknown verdict": `{"structured_output":{"verdict":"lgtm","feedback":""}}`,
		"no verdict":      `{"structured_output":{"feedback":"x"}}`,
	} {
		_, _, err := ParseReview(out)
		assert.Assert(t, err != nil, name)
	}
}

func TestPassed(t *testing.T) {
	t.Parallel()
	ok := Check{ValidatePassed: true, Reviews: []ReviewResult{{Name: "a", Verdict: VerdictApproved}}}
	warned := Check{ValidatePassed: true, Reviews: []ReviewResult{{Name: "a", Verdict: VerdictWarn}}}
	blocked := Check{ValidatePassed: true, Reviews: []ReviewResult{{Name: "a", Verdict: VerdictBlocked}}}
	broken := Check{ValidatePassed: true, Reviews: []ReviewResult{{Name: "a", Err: "timed out"}}}
	failing := Check{ValidatePassed: false, Reviews: []ReviewResult{{Name: "a", Verdict: VerdictApproved}}}

	assert.Assert(t, Passed(ok, VerdictBlocked))
	assert.Assert(t, Passed(warned, VerdictBlocked))
	assert.Assert(t, !Passed(warned, VerdictWarn))
	assert.Assert(t, !Passed(blocked, VerdictBlocked))
	assert.Assert(t, !Passed(broken, VerdictBlocked))
	assert.Assert(t, !Passed(failing, VerdictBlocked))
}

func TestFeedbackIncludesOnlyFailures(t *testing.T) {
	t.Parallel()
	fb := Feedback(Check{
		ValidatePassed: false,
		ValidateOutput: "FAIL test_hello",
		Reviews: []ReviewResult{
			{Name: "security", Verdict: VerdictApproved, Feedback: "fine"},
			{Name: "style", Verdict: VerdictWarn, Feedback: "rename x"},
			{Name: "tests", Verdict: VerdictBlocked, Feedback: "no tests"},
		},
	}, VerdictBlocked)
	assert.Assert(t, strings.Contains(fb, "FAIL test_hello"), fb)
	assert.Assert(t, strings.Contains(fb, "tests (blocked)"), fb)
	assert.Assert(t, !strings.Contains(Feedback(Check{ValidatePassed: true, Reviews: []ReviewResult{{Name: "x", Err: "timed out"}}}, VerdictBlocked), "timed out"))
	assert.Assert(t, !strings.Contains(fb, "rename x"), fb)
	assert.Assert(t, !strings.Contains(fb, "fine"), fb)
}

func TestRunFeedsFailuresBackUntilPass(t *testing.T) {
	t.Parallel()
	var prompts []string
	checks := []Check{
		{ValidatePassed: false, ValidateOutput: "boom"},
		{ValidatePassed: true, Reviews: []ReviewResult{{Name: "r", Verdict: VerdictApproved}}},
	}
	attempts, err := Run(context.Background(), Options{
		Intent:      "build it",
		MaxAttempts: 5,
		Work: func(_ context.Context, _ int, prompt string) error {
			prompts = append(prompts, prompt)
			return nil
		},
		Check: func(_ context.Context, n int) (Check, error) { return checks[n-1], nil },
	})
	assert.NilError(t, err)
	assert.Equal(t, len(attempts), 2)
	assert.Assert(t, attempts[1].Passed)
	assert.Equal(t, prompts[0], "build it")
	assert.Assert(t, strings.HasPrefix(prompts[1], "build it"), prompts[1])
	assert.Assert(t, strings.Contains(prompts[1], "boom"), prompts[1])
}

func TestRunStopsAtMaxAttempts(t *testing.T) {
	t.Parallel()
	works := 0
	attempts, err := Run(context.Background(), Options{
		Intent:      "x",
		MaxAttempts: 2,
		Work:        func(context.Context, int, string) error { works++; return nil },
		Check: func(context.Context, int) (Check, error) {
			return Check{ValidatePassed: true, Reviews: []ReviewResult{{Name: "r", Verdict: VerdictBlocked, Feedback: "no"}}}, nil
		},
	})
	assert.Assert(t, errors.Is(err, ErrNotConverged))
	assert.Equal(t, works, 2)
	assert.Equal(t, len(attempts), 2)
}

func TestRunStopsOnWorkError(t *testing.T) {
	t.Parallel()
	boom := errors.New("sidecar gone")
	_, err := Run(context.Background(), Options{
		Intent: "x",
		Work:   func(context.Context, int, string) error { return boom },
		Check:  func(context.Context, int) (Check, error) { t.Fatal("check after failed work"); return Check{}, nil },
	})
	assert.Assert(t, errors.Is(err, boom))
}

func TestRunStopsWhenCanceledDuringCheck(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	attempts, err := Run(ctx, Options{
		Intent:      "x",
		MaxAttempts: 3,
		Work:        func(context.Context, int, string) error { return nil },
		Check: func(context.Context, int) (Check, error) {
			cancel()
			return Check{ValidatePassed: false}, nil
		},
	})
	assert.Assert(t, errors.Is(err, context.Canceled), "err: %v", err)
	assert.Equal(t, len(attempts), 0)
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("hi\n"), 0o644))
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func TestCreateWorktreeApplyAndCommit(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@t")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@t")
	ctx := context.Background()
	repo := newRepo(t)
	assert.NilError(t, os.WriteFile(filepath.Join(repo, "old.txt"), []byte("rename me\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(repo, "gone.txt"), []byte("delete me\n"), 0o644))
	git(t, repo, "add", "-A")
	git(t, repo, "commit", "-q", "-m", "more files")

	wt, err := CreateWorktree(ctx, repo, time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC))
	assert.NilError(t, err)
	assert.Equal(t, wt.Branch, "chunk/factory-20260930-120000")
	assert.Equal(t, git(t, wt.Dir, "rev-parse", "HEAD"), wt.Base)
	// The developer's tree sees neither the worktree nor pool state.
	assert.Equal(t, git(t, repo, "status", "--porcelain"), "")

	// A second run in the same repository adds the exclude only once.
	wt2, err := CreateWorktree(ctx, repo, time.Date(2026, 9, 30, 12, 0, 1, 0, time.UTC))
	assert.NilError(t, err)
	assert.Assert(t, wt2.Dir != wt.Dir)
	exclude, err := os.ReadFile(filepath.Join(repo, ".git", "info", "exclude"))
	assert.NilError(t, err)
	assert.Equal(t, strings.Count(string(exclude), poolStateExclude), 1)

	// A patch as the worker's DiffScript produces it, from a worker copy of
	// the worktree: an added, a renamed, a deleted and a binary file.
	worker := t.TempDir()
	git(t, worker, "clone", "-q", wt.Dir, ".")
	assert.NilError(t, os.WriteFile(filepath.Join(worker, "hello.py"), []byte("print('hi')\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(worker, "logo.bin"), []byte{0, 1, 2, 0xff, 0}, 0o644))
	git(t, worker, "mv", "old.txt", "new.txt")
	git(t, worker, "rm", "-q", "gone.txt")
	git(t, worker, "add", "-A")
	patch := git(t, worker, "diff", "--cached", "--binary", wt.Base) + "\n"

	assert.NilError(t, ApplyPatch(ctx, wt.Dir, patch))
	assert.NilError(t, os.MkdirAll(filepath.Join(wt.Dir, ".chunk"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(wt.Dir, ".chunk", "factory-pool.json"), []byte("{}"), 0o644))
	assert.NilError(t, CommitAll(ctx, wt.Dir, "factory: attempt 1"))

	files := git(t, wt.Dir, "show", "--name-status", "--format=", "HEAD")
	for _, want := range []string{"A\thello.py", "A\tlogo.bin", "D\tgone.txt"} {
		assert.Assert(t, strings.Contains(files, want), files)
	}
	assert.Assert(t, strings.Contains(files, "new.txt"), files)
	assert.Assert(t, !strings.Contains(files, "factory-pool.json"), files)
	bin, err := os.ReadFile(filepath.Join(wt.Dir, "logo.bin"))
	assert.NilError(t, err)
	assert.DeepEqual(t, bin, []byte{0, 1, 2, 0xff, 0})
	assert.Assert(t, errors.Is(CommitAll(ctx, wt.Dir, "again"), ErrNothingToCommit))
	assert.Equal(t, git(t, repo, "status", "--porcelain"), "")
}

func TestWorkScriptQuotesPrompt(t *testing.T) {
	t.Parallel()
	script := WorkScript("/home/user/app", `it's "quoted" $(rm -rf /)`, "")
	assert.Assert(t, !strings.Contains(script, "rm -rf"), script)
	assert.Assert(t, strings.Contains(script, "'--dangerously-skip-permissions'"), script)
	assert.Assert(t, strings.Contains(DiffScript("/r", "abc"), "diff --cached --binary 'abc'"))
}
