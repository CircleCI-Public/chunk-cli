package factory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// projectWithOrigin returns a project whose origin is a bare repository on this
// machine, standing in for the host, and a run's worktree on it with one
// commit of the implementer's work.
func projectWithOrigin(t *testing.T) (root, origin string, wt Worktree) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	ctx := context.Background()
	root = newProject(t)
	origin = filepath.Join(t.TempDir(), "origin.git")
	gitOutput(t, filepath.Dir(origin), "init", "-q", "--bare", origin)
	gitOutput(t, root, "remote", "set-url", "origin", origin)

	wt, err := CreateWorktree(ctx, root, filepath.Join(t.TempDir(), "wt"), "run-1", "add a flag")
	assert.NilError(t, err)
	writeFile(t, wt.Path, "flag.go", "package main\n")
	_, err = wt.Commit(ctx, "add a flag")
	assert.NilError(t, err)
	return root, origin, wt
}

func TestWorktreePushPublishesTheBranch(t *testing.T) {
	root, origin, wt := projectWithOrigin(t)
	ctx := context.Background()

	has, err := RemoteHasBranch(ctx, root, "origin", wt.Branch)
	assert.NilError(t, err)
	assert.Assert(t, !has, "the branch was on the remote before it was pushed")

	assert.NilError(t, wt.Push(ctx, "origin"))

	has, err = RemoteHasBranch(ctx, root, "origin", wt.Branch)
	assert.NilError(t, err)
	assert.Assert(t, has)
	assert.Equal(t, gitOutput(t, origin, "rev-parse", wt.Branch), gitOutput(t, wt.Path, "rev-parse", "HEAD"))
	// The branch tracks its remote, so the developer can push more to it.
	assert.Equal(t, gitOutput(t, wt.Path, "rev-parse", "--abbrev-ref", "@{upstream}"), "origin/"+wt.Branch)
}

func TestWorktreePushSaysWhyItFailed(t *testing.T) {
	_, _, wt := projectWithOrigin(t)

	err := wt.Push(context.Background(), "nowhere")

	assert.ErrorContains(t, err, "push "+wt.Branch+" to nowhere")
	assert.ErrorContains(t, err, "nowhere")
}

func TestRemoteHasBranchDistinguishesNoBranchFromNoRemote(t *testing.T) {
	root, _, _ := projectWithOrigin(t)
	ctx := context.Background()

	has, err := RemoteHasBranch(ctx, root, "origin", "main")
	assert.NilError(t, err)
	assert.Assert(t, !has)

	// A remote that cannot be reached is an error, not "no such branch": the
	// answer decides which branch a pull request targets.
	_, err = RemoteHasBranch(ctx, root, "nowhere", "main")
	assert.Assert(t, err != nil)
}

func TestPullRequestTitle(t *testing.T) {
	tests := []struct{ name, prompt, want string }{
		{"the first line", "Add a --verbose flag\n\nAnd document it.", "Add a --verbose flag"},
		{"a markdown heading", "## Add a flag\nbody", "Add a flag"},
		{"leading blank lines and spacing", "\n\n  Add   a\tflag  \n", "Add a flag"},
		{"nothing to say", " \n\n", "chunk factory run run-1"},
		{"cut on a character", strings.Repeat("é", 100), strings.Repeat("é", 71) + "…"},
		{
			"cut between words",
			"Add a short 'About' paragraph to the README describing what this demo app does",
			"Add a short 'About' paragraph to the README describing what this demo…",
		},
		{"no tidy place to cut", "Fix " + strings.Repeat("x", 90), "Fix " + strings.Repeat("x", 67) + "…"},
		{"fits as it is", strings.Repeat("a", 72), strings.Repeat("a", 72)},
		{"a plan, by its outcome", planPrompt, "chunk sidecar sync --quiet prints nothing but errors"},
		{"a plan with no title line", strings.TrimPrefix(planPrompt, "# sidecar-sync-quiet-flag\n\n"), "chunk sidecar sync --quiet prints nothing but errors"},
		{"a plan with nothing in its outcome yet", "# sidecar-sync-quiet-flag\n\n## 1. Outcome\n\n## 2. Design\n", "sidecar-sync-quiet-flag"},
		{
			"a long outcome, cut between words",
			"# x\n\n## 1. Outcome\n\nThe planning screen keeps each plan and its comments in factory, so a plan made here can be built there.\n",
			"The planning screen keeps each plan and its comments in factory, so a…",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PullRequestTitle(tt.prompt, "run-1")
			assert.Equal(t, got, tt.want)
			assert.Assert(t, utf8.ValidString(got))
			assert.Assert(t, utf8.RuneCountInString(got) <= 72, "%d characters", utf8.RuneCountInString(got))
		})
	}
}

func TestPullRequestBodySaysWhatWasAskedAndChecked(t *testing.T) {
	rep := Report{
		Stat: "2 files changed, 10 insertions(+)",
		Outcome: Outcome{Result: ResultPassed, Rounds: 2, Checks: []Check{
			{Name: "bugs", Kind: KindReview, Status: StatusPassed},
			{Name: "style", Kind: KindReview, Status: StatusFailed},
			{Name: "test", Kind: KindValidate, Status: StatusPassed},
		}},
	}
	src := PromptSource{
		Prompts: []review.Prompt{{
			Name: "bugs", File: "bugs.md",
			Body: "# Review Check: Bugs\n\nTries to break the change and proves each\nproblem before reporting it. More detail.\n\n## Rules\n",
		}},
		Owner: "acme", Repo: "widgets", Commit: "abc123", Dir: "custom/reviews",
	}

	got := PullRequestBody("# Add a flag\n\n- and document it", rep, src)

	for _, want := range []string{
		"# Add a flag\n\n- and document it\n",
		"All checks passed after 2 round(s)",
		"- bugs: Tries to break the change and proves each problem before reporting it." +
			" ([prompt](https://github.com/acme/widgets/blob/abc123/custom/reviews/bugs.md))\n",
		"- test\n",
		"Assembled with care by **chunk factory 🏭**",
	} {
		assert.Assert(t, strings.Contains(got, want), "%q is not in:\n%s", want, got)
	}
	for _, unwanted := range []string{"## Request", "> ", "## Changes", "2 files changed", "run ", "(validation)", "style"} {
		assert.Assert(t, !strings.Contains(got, unwanted), "%q is in:\n%s", unwanted, got)
	}
}

func TestPullRequestBodyListsAReviewWithNoParagraphByNameAndLink(t *testing.T) {
	rep := Report{Outcome: Outcome{Rounds: 1, Checks: []Check{{Name: "bare", Kind: KindReview, Status: StatusPassed}}}}
	src := PromptSource{
		Prompts: []review.Prompt{{Name: "bare", File: "bare.md", Body: "# Just a heading\n"}},
		Owner:   "acme", Repo: "widgets", Commit: "abc123",
	}

	got := PullRequestBody("add a flag", rep, src)

	assert.Assert(t, strings.Contains(got, "- bare ([prompt](https://github.com/acme/widgets/blob/abc123/.chunk/reviews/bare.md))\n"), got)
}

func TestPullRequestBodyLeavesOutLinksItCannotMake(t *testing.T) {
	rep := Report{Outcome: Outcome{Rounds: 1, Checks: []Check{
		{Name: "lint", Kind: KindValidate, Status: StatusPassed},
		{Name: "gone", Kind: KindReview, Status: StatusPassed},
	}}}

	got := PullRequestBody("add a flag", rep, PromptSource{})

	assert.Assert(t, strings.Contains(got, "- lint\n"), got)
	assert.Assert(t, strings.Contains(got, "- gone\n"), got)
	assert.Assert(t, !strings.Contains(got, "http"), got)
}

func TestPullRequestBodyKeepsALongPromptShort(t *testing.T) {
	got := PullRequestBody(strings.Repeat("é", 20000), Report{}, PromptSource{})

	assert.Assert(t, utf8.ValidString(got))
	assert.Assert(t, utf8.RuneCountInString(got) < 9000, "the body was %d characters", utf8.RuneCountInString(got))
}

func TestPullRequestBodyEscapesTheLinkAndKeepsTheRequestFromSwallowingTheReview(t *testing.T) {
	rep := Report{Outcome: Outcome{Rounds: 1, Checks: []Check{{Name: "my check", Kind: KindReview, Status: StatusPassed}}}}
	src := PromptSource{
		Prompts: []review.Prompt{{Name: "my check", File: "my check (1)#.md", Body: "```\ncode\n```\n"}},
		Owner:   "acme", Repo: "widgets", Commit: "abc123",
	}

	got := PullRequestBody("fix it\n```go\nfunc f() {", rep, src)

	assert.Assert(t, strings.Contains(got, "/.chunk/reviews/my%20check%20%281%29%23.md"), got)
	// The fence the request left open is closed before the review section.
	assert.Assert(t, strings.Contains(got, "func f() {\n```\n\n## Review"), got)
	// A prompt that opens with a fence has no summary to take, and none leaks in.
	assert.Assert(t, strings.HasPrefix(strings.Split(got, "\n- ")[1], "my check ([prompt]"), got)
}
