package factory

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"gotest.tools/v3/assert"
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := PullRequestTitle(tt.prompt, "run-1")
			assert.Equal(t, got, tt.want)
			assert.Assert(t, utf8.ValidString(got))
		})
	}
}

func TestPullRequestBodySaysWhatWasAskedChangedAndChecked(t *testing.T) {
	rep := Report{
		Stat: "2 files changed, 10 insertions(+)",
		Outcome: Outcome{Result: ResultPassed, Rounds: 2, Checks: []Check{
			{Name: "bugs", Kind: KindReview, Status: StatusPassed},
			{Name: "style", Kind: KindReview, Status: StatusFailed},
			{Name: "test", Kind: KindValidate, Status: StatusPassed},
		}},
	}

	got := PullRequestBody("Add a flag\nand document it", "20261002-150405-ab12cd", rep)

	for _, want := range []string{
		"> Add a flag\n> and document it\n",
		"2 files changed, 10 insertions(+)",
		"All checks passed after 2 round(s)",
		"- bugs (review)\n",
		"- test (validation)\n",
		"run 20261002-150405-ab12cd",
	} {
		assert.Assert(t, strings.Contains(got, want), "%q is not in:\n%s", want, got)
	}
	// Only checks that passed are listed as passing.
	assert.Assert(t, !strings.Contains(got, "style"), got)
}

func TestPullRequestBodyKeepsALongPromptShort(t *testing.T) {
	got := PullRequestBody(strings.Repeat("é", 20000), "run-1", Report{})

	assert.Assert(t, utf8.ValidString(got))
	assert.Assert(t, utf8.RuneCountInString(got) < 9000, "the body was %d characters", utf8.RuneCountInString(got))
}
