package cmd

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/factory"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/smeltd"
	"github.com/smeltdev/smelt/internal/testing/gitrepo"
)

func TestPublishFlagsMode(t *testing.T) {
	tests := []struct {
		name  string
		flags publishFlags
		want  factory.PublishMode
		err   bool
	}{
		{"none", publishFlags{}, factory.PublishPrompt, false},
		{"push", publishFlags{push: true}, factory.PublishPush, false},
		{"pr", publishFlags{pr: true}, factory.PublishPR, false},
		{"pr implies push", publishFlags{pr: true, push: true}, factory.PublishPR, false},
		{"no-publish", publishFlags{none: true}, factory.PublishOff, false},
		{"no-publish with push", publishFlags{none: true, push: true}, "", true},
		{"no-publish with pr", publishFlags{none: true, pr: true}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.flags.mode()
			assert.Equal(t, err != nil, tt.err)
			assert.Equal(t, got, tt.want)
		})
	}
}

type fakePRHost struct {
	existing *factory.PR
	created  []factory.PRCreate
}

func (h *fakePRHost) Existing(context.Context, string) (*factory.PR, error) { return h.existing, nil }
func (h *fakePRHost) Create(_ context.Context, pr factory.PRCreate) (string, error) {
	h.created = append(h.created, pr)
	return "https://github.com/o/r/pull/9", nil
}

// publishProject is a project with a bare origin holding main and a factory
// branch with committed work, described as the finished run the daemon reports.
func publishProject(t *testing.T) (*smeltd.FactoryRun, string) {
	t.Helper()
	root := gitrepo.SetupGitRepo(t, "o", "r")
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, root, "init", "--bare", origin)
	runGit(t, root, "remote", "set-url", "origin", origin)
	runGit(t, root, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, root, "fetch", "origin")
	wt, err := factory.CreateWorktree(context.Background(), factory.WorktreeOptions{Root: root, Path: filepath.Join(t.TempDir(), "wt"), RunID: "run-1", Prompt: "add a flag"})
	assert.NilError(t, err)
	return &smeltd.FactoryRun{
		Prompt: "add a flag", RunID: "run-1", Worktree: wt.Path, Branch: wt.Branch,
		BaseBranch: wt.BaseBranch, DefaultBranch: "main",
		Result: smeltd.ResultPassed, Rounds: 1, Committed: true,
	}, origin
}

func pushed(t *testing.T, origin, branch string) bool {
	t.Helper()
	return runGit(t, origin, "for-each-ref", "refs/heads/"+branch) != ""
}

func TestPublishRun(t *testing.T) {
	existing := &factory.PR{Number: 4, URL: "https://github.com/o/r/pull/4"}
	tests := []struct {
		name        string
		flags       publishFlags
		prompt      bool
		answer      bool
		pr          *factory.PR
		mutate      func(*smeltd.FactoryRun)
		wantAsked   bool
		wantPushed  bool
		wantCreated int
	}{
		{name: "prompt yes opens a PR", prompt: true, answer: true, wantAsked: true, wantPushed: true, wantCreated: 1},
		{name: "prompt no does nothing", prompt: true, answer: false, wantAsked: true},
		{name: "prompt yes with a PR only pushes", prompt: true, answer: true, pr: existing, wantAsked: true, wantPushed: true},
		{name: "headless does not prompt", prompt: false},
		{name: "--push pushes without a PR", flags: publishFlags{push: true}, wantPushed: true},
		{name: "--pr opens without asking", flags: publishFlags{pr: true}, wantPushed: true, wantCreated: 1},
		{name: "--pr with a PR does not create another", flags: publishFlags{pr: true}, pr: existing, wantPushed: true},
		{name: "--no-publish does nothing", flags: publishFlags{none: true}, prompt: true},
		{name: "failed run is not offered", prompt: true, mutate: func(f *smeltd.FactoryRun) { f.Result = smeltd.ResultExhausted }},
		{name: "failed run with --pr is published", flags: publishFlags{pr: true}, mutate: func(f *smeltd.FactoryRun) { f.Result = smeltd.ResultExhausted }, wantPushed: true, wantCreated: 1},
		{name: "uncommitted run is not published", flags: publishFlags{pr: true}, mutate: func(f *smeltd.FactoryRun) { f.Committed = false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, origin := publishProject(t)
			if tt.mutate != nil {
				tt.mutate(f)
			}
			asked := false
			orig := confirmPublish
			confirmPublish = func(string, bool) (bool, error) { asked = true; return tt.answer, nil }
			t.Cleanup(func() { confirmPublish = orig })
			host := &fakePRHost{existing: tt.pr}
			var out, errOut bytes.Buffer

			err := publishRun(t.Context(), iostream.Streams{Out: &out, Err: &errOut}, f, tt.flags, tt.prompt, host)

			assert.NilError(t, err)
			assert.Equal(t, asked, tt.wantAsked)
			assert.Equal(t, pushed(t, origin, f.Branch), tt.wantPushed)
			assert.Equal(t, len(host.created), tt.wantCreated)
			if tt.wantCreated > 0 {
				assert.Equal(t, host.created[0].Base, "main")
				assert.Equal(t, host.created[0].Head, f.Branch)
			}
		})
	}
}

func TestPublishRunRefusesTheDefaultBranch(t *testing.T) {
	f, origin := publishProject(t)
	f.Branch = "main"
	host := &fakePRHost{}
	err := publishRun(t.Context(), iostream.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, f, publishFlags{pr: true}, false, host)
	assert.Assert(t, err != nil)
	assert.Equal(t, len(host.created), 0)
	assert.Assert(t, !pushed(t, origin, "smelt"))
}

func TestPublishRunRejectsConflictingFlags(t *testing.T) {
	f, _ := publishProject(t)
	err := publishRun(t.Context(), iostream.Streams{Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}, f, publishFlags{none: true, push: true}, false, &fakePRHost{})
	assert.Assert(t, err != nil)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = gitrepo.GitEnv(dir)
	out, err := c.CombinedOutput()
	assert.NilError(t, err, string(out))
	return strings.TrimSpace(string(out))
}
