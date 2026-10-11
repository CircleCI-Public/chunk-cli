package factory

import (
	"context"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
)

func TestDecidePublish(t *testing.T) {
	ok := PublishFacts{Mode: PublishPrompt, Committed: true, Passed: true, HasRemote: true, Interactive: true}
	with := func(f func(*PublishFacts)) PublishFacts { c := ok; f(&c); return c }
	tests := []struct {
		name   string
		facts  PublishFacts
		action PublishAction
		ask    bool
		reason bool
	}{
		{"passed in a terminal asks to open a PR", ok, ActionOpenPR, true, false},
		{"existing PR asks to update it", with(func(f *PublishFacts) { f.HasPR = true }), ActionUpdatePR, true, false},
		{"nothing committed", with(func(f *PublishFacts) { f.Committed = false; f.Mode = PublishPR }), ActionNone, false, true},
		{"no remote", with(func(f *PublishFacts) { f.HasRemote = false }), ActionNone, false, true},
		{"failed run is skipped by default", with(func(f *PublishFacts) { f.Passed = false }), ActionNone, false, true},
		{"headless never prompts", with(func(f *PublishFacts) { f.Interactive = false }), ActionNone, false, false},
		{"off leaves it local", with(func(f *PublishFacts) { f.Mode = PublishOff }), ActionNone, false, false},
		{"--push pushes unasked, even a failed run", with(func(f *PublishFacts) { f.Mode = PublishPush; f.Passed = false; f.Interactive = false }), ActionPush, false, false},
		{"--pr opens unasked", with(func(f *PublishFacts) { f.Mode = PublishPR; f.Interactive = false }), ActionOpenPR, false, false},
		{"--pr with a PR updates it", with(func(f *PublishFacts) { f.Mode = PublishPR; f.HasPR = true }), ActionUpdatePR, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := DecidePublish(tt.facts)
			assert.Equal(t, d.Action, tt.action)
			assert.Equal(t, d.Ask, tt.ask)
			assert.Equal(t, d.Reason != "", tt.reason)
		})
	}
}

func TestCheckHeadBranch(t *testing.T) {
	assert.NilError(t, CheckHeadBranch("chunk/factory/x/run-1", "main"))
	assert.ErrorContains(t, CheckHeadBranch("main", "main"), "not a chunk/factory/")
	assert.ErrorContains(t, CheckHeadBranch("chunk/factory/x/run-1", "chunk/factory/x/run-1"), "default branch")
}

func TestChooseBase(t *testing.T) {
	assert.Equal(t, ChooseBase("", "feature", "main"), "feature")
	assert.Equal(t, ChooseBase("", "", "main"), "main")
	assert.Equal(t, ChooseBase("", "chunk/factory/a/run-0", "main"), "main")
	assert.Equal(t, ChooseBase("release", "feature", "main"), "release")
}

type fakeHost struct {
	existing *PR
	created  []PRCreate
}

func (h *fakeHost) Existing(context.Context, string) (*PR, error) { return h.existing, nil }
func (h *fakeHost) Create(_ context.Context, pr PRCreate) (string, error) {
	h.created = append(h.created, pr)
	return "https://github.com/my-org/my-repo/pull/7", nil
}

// remoteProject is a project with a bare origin that has the default branch,
// and a worktree on a factory branch.
func remoteProject(t *testing.T) (Publication, string) {
	t.Helper()
	root := newProject(t)
	origin := filepath.Join(t.TempDir(), "origin.git")
	gitOutput(t, root, "init", "--bare", origin)
	gitOutput(t, root, "remote", "set-url", "origin", origin)
	gitOutput(t, root, "push", "origin", "HEAD:refs/heads/main")
	gitOutput(t, root, "fetch", "origin")
	wt, err := CreateWorktree(context.Background(), root, filepath.Join(t.TempDir(), "wt"), "run-1", "add a flag")
	assert.NilError(t, err)
	return Publication{
		Dir: wt.Path, Remote: "origin", Head: wt.Branch,
		BaseBranch: wt.BaseBranch, DefaultBranch: "main",
	}, origin
}

func TestPublicationPushesOnlyTheFactoryBranch(t *testing.T) {
	ctx := context.Background()
	pub, origin := remoteProject(t)
	host := &fakeHost{}
	pub.Host = host

	target, err := pub.Resolve(ctx)
	assert.NilError(t, err)
	assert.Assert(t, target.Existing == nil)
	assert.NilError(t, pub.Push(ctx))
	assert.Equal(t, gitOutput(t, origin, "rev-parse", pub.Head), gitOutput(t, pub.Dir, "rev-parse", "HEAD"))

	url, err := pub.OpenPR(ctx, target)
	assert.NilError(t, err)
	assert.Equal(t, url, "https://github.com/my-org/my-repo/pull/7")
	assert.Equal(t, len(host.created), 1)
	assert.Equal(t, host.created[0].Head, pub.Head)
}

func TestPublicationFindsExistingPR(t *testing.T) {
	pub, _ := remoteProject(t)
	pub.Host = &fakeHost{existing: &PR{Number: 3, URL: "u"}}
	target, err := pub.Resolve(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, target.Existing.Number, 3)
}

func TestPublicationRefusesTheDefaultBranch(t *testing.T) {
	ctx := context.Background()
	pub, origin := remoteProject(t)
	pub.Head, pub.DefaultBranch = "main", "main"
	_, err := pub.Resolve(ctx)
	assert.Assert(t, err != nil)
	assert.Assert(t, pub.Push(ctx) != nil)
	assert.Equal(t, gitOutput(t, origin, "for-each-ref", "refs/heads/chunk"), "")
}

func TestPublicationBaseFallsBackToDefaultWhenNotOnRemote(t *testing.T) {
	pub, _ := remoteProject(t)
	pub.BaseBranch = "local-only"
	target, err := pub.Resolve(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, target.Base, "main")
}

func TestCreateWorktreeRecordsBranches(t *testing.T) {
	root := newProject(t)
	wt, err := CreateWorktree(context.Background(), root, filepath.Join(t.TempDir(), "wt"), "run-1", "x")
	assert.NilError(t, err)
	assert.Equal(t, wt.BaseBranch, gitOutput(t, root, "rev-parse", "--abbrev-ref", "HEAD"))
}
