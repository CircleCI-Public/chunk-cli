package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// fakeReviewer is a claude that, run as a review, records the change it was
// given to review — the reviewer's `git diff HEAD`, as the real reviewer is
// told to read it — and reports no findings.
const fakeReviewer = `mkdir -p "$HOME/seen"
git diff HEAD --binary > "$HOME/seen/$$"
printf '%s\n' '{"type":"result","subtype":"success","is_error":false,"result":"","structured_output":{"review":"ok","findings":[]}}'`

func TestScopePromptsIncludesOriginalRequestAndReusableInstructions(t *testing.T) {
	prompts := []review.Prompt{{Name: "testing", Body: "Check tests for every requested behavior."}}
	got := scopePrompts("Add shareable search.\nPreserve the URL hash.", prompts)

	assert.Equal(t, len(got), 1)
	assert.Equal(t, got[0].Name, "testing")
	for _, want := range []string{
		"## Requested change\n\nAdd shareable search.\nPreserve the URL hash.",
		"## Change under review\n\nThe change under review is the uncommitted work",
		"## Review instructions\n\nCheck tests for every requested behavior.",
	} {
		assert.Assert(t, strings.Contains(got[0].Body, want), "missing %q in:\n%s", want, got[0].Body)
	}
	// Composing the runtime context must not rewrite the reusable prompt loaded
	// from the repository.
	assert.Equal(t, prompts[0].Body, "Check tests for every requested behavior.")
}

// TestCheckReviewsTheImplementersLatestWork runs several rounds of Check
// against an implementer whose work changes every round: edits, new files, a
// new file deleted again, a tracked file deleted and restored, an edit
// reverted. Each round, every review must see exactly the implementer's
// change as it is now, never an earlier round's code.
func TestCheckReviewsTheImplementersLatestWork(t *testing.T) {
	ctx := context.Background()
	s, impl, reviewers, home := newSidecarsFixture(t, nil)
	var trees []ReviewerTree
	s.OnReviewerTree = func(rt ReviewerTree) { trees = append(trees, rt) }

	// edit writes a file of the implementer's as it would in a round of its
	// own. Rounds are minutes apart, so each edit's mtime is too. rsync skips a
	// file whose size and mtime are unchanged, and several of these edits keep
	// the size, so without this the test's rounds, run within one instant,
	// would look unchanged to it in a way no real run does.
	var mtime time.Time
	edit := func(name, content string) {
		writeFile(t, impl.RepoPath, name, content)
		assert.NilError(t, os.Chtimes(filepath.Join(impl.RepoPath, name), mtime, mtime))
	}

	rounds := []struct {
		name string
		// work is what the implementer does this round, on top of the last.
		work func()
		// want is the implementer's change as it stands after work, by
		// `git diff HEAD --name-status`.
		want string
	}{
		{
			name: "first implementation",
			work: func() {
				edit("main.go", "package main\n\n// v1\n")
				edit("pkg/a.go", "package pkg\n")
				edit("notes.txt", "round 1\n")
			},
			want: "M\tmain.go\nA\tnotes.txt\nA\tpkg/a.go",
		},
		{
			name: "fixes: edit again, drop a new file, delete a tracked one, add another",
			work: func() {
				edit("main.go", "package main\n\n// v2\n")
				assert.NilError(t, os.Remove(filepath.Join(impl.RepoPath, "pkg", "a.go")))
				assert.NilError(t, os.Remove(filepath.Join(impl.RepoPath, "README.md")))
				edit("notes.txt", "round 2\n")
				edit("pkg/b.go", "package pkg\n\nvar B = 2\n")
			},
			want: "D\tREADME.md\nM\tmain.go\nA\tnotes.txt\nA\tpkg/b.go",
		},
		{
			name: "more fixes: revert main.go, restore README.md",
			work: func() {
				edit("main.go", "package main\n")
				edit("README.md", "# my-repo\n")
				edit("pkg/b.go", "package pkg\n\nvar B = 3\n")
			},
			want: "A\tnotes.txt\nA\tpkg/b.go",
		},
	}

	var previous string
	for i, round := range rounds {
		mtime = time.Now().Add(time.Duration(i+1) * time.Minute)
		round.work()
		change, err := s.Collect(ctx)
		assert.NilError(t, err, round.name)
		assert.Assert(t, !change.Empty(), round.name)
		assert.Equal(t, gitOutput(t, impl.RepoPath, "diff", "HEAD", "--name-status"), round.want, round.name)
		implDiff := gitOutput(t, impl.RepoPath, "diff", "HEAD", "--binary")
		assert.Assert(t, implDiff != previous, "%s: the implementer's change did not move", round.name)
		previous = implDiff

		assert.NilError(t, os.RemoveAll(filepath.Join(home, "seen")))
		trees = nil
		checks, err := s.Check(ctx, i+1)
		assert.NilError(t, err, round.name)
		assert.Equal(t, len(checks), len(s.Prompts), round.name)
		for _, c := range checks {
			assert.Equal(t, c.Status, StatusPassed, "%s: %s: %s", round.name, c.Name, c.Error)
		}

		// What each review was handed is the implementer's change, line for
		// line: blob IDs in the diff are content hashes, so equal diffs mean
		// equal files even though each member has its own baseline commit.
		seen, err := os.ReadDir(filepath.Join(home, "seen"))
		assert.NilError(t, err, round.name)
		assert.Equal(t, len(seen), len(s.Prompts), round.name)
		for _, f := range seen {
			got := readFile(t, filepath.Join(home, "seen"), f.Name())
			assert.Equal(t, strings.TrimRight(got, "\n"), implDiff, "%s: a review saw other code", round.name)
		}
		// Every reviewer is current, including one that ran no review this
		// round and would hand stale code to the next.
		for _, r := range reviewers {
			assert.Equal(t, gitOutput(t, r.RepoPath, "diff", "HEAD", "--binary"), implDiff, "%s: %s is stale", round.name, r.ID)
		}
		// The --verbose canary agrees: every reviewer, in order, has the
		// change this round collected.
		assert.Equal(t, len(trees), len(reviewers), round.name)
		for j, rt := range trees {
			assert.Equal(t, rt.Round, i+1)
			assert.Equal(t, rt.SidecarID, reviewers[j].ID)
			assert.Equal(t, rt.Want, change.Fingerprint)
			assert.Assert(t, rt.Matches(), "%s: %+v", round.name, rt)
		}
	}
}

// newSidecarsFixture returns Sidecars whose implementer and two reviewers are
// clones on this machine, prepared as a run leaves them. wrap, when set, wraps
// the exec every script runs through.
func newSidecarsFixture(t *testing.T, wrap func(review.Execer) review.Execer) (*Sidecars, *sidecar.PoolEntry, []*sidecar.PoolEntry, string) {
	t.Helper()
	ctx := context.Background()
	root := setupRepo(t)
	wt, err := CreateWorktree(ctx, root, filepath.Join(t.TempDir(), "wt"), "run-1")
	assert.NilError(t, err)

	// Every pool member starts from the worktree, as the pool's sync leaves
	// them.
	member := func() string {
		dir := filepath.Join(t.TempDir(), "my-repo")
		gitOutput(t, root, "clone", "-q", "--branch", wt.Branch, root, dir)
		return dir
	}
	impl := &sidecar.PoolEntry{ID: "impl", RepoPath: member()}
	reviewers := []*sidecar.PoolEntry{{ID: "rev-1", RepoPath: member()}, {ID: "rev-2", RepoPath: member()}}

	home := t.TempDir()
	installFakeClaude(t, home, fakeReviewer)
	exec := withoutPkill(localExec(home))
	if wrap != nil {
		exec = wrap(exec)
	}
	pool := make(chan *sidecar.PoolEntry, len(reviewers))
	for _, r := range reviewers {
		pool <- r
	}
	s := &Sidecars{
		Exec:        exec,
		Implementer: &Implementer{Entry: impl},
		Acquire: func(ctx context.Context) (*sidecar.PoolEntry, error) {
			select {
			case e := <-pool:
				return e, nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
		Release:   func(e *sidecar.PoolEntry) { pool <- e },
		Reviewers: reviewers,
		Relay:     newRelay(t, wt.Path),
		Request:   "make the requested change",
		Prompts:   []review.Prompt{{Name: "bugs", Body: "find bugs"}, {Name: "style", Body: "check style"}},
		Review: review.Options{
			Credential:         review.Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "k"},
			StructuredFindings: true,
		},
	}
	assert.NilError(t, s.Prepare(ctx))
	return s, impl, reviewers, home
}

// setupRepo returns a repository with a committed README.md and main.go for
// the implementer to change.
func setupRepo(t *testing.T) string {
	t.Helper()
	root := localRepo(t)
	gitOutput(t, root, "remote", "add", "origin", "https://github.com/my-org/my-repo.git")
	writeFile(t, root, "README.md", "# my-repo\n")
	writeFile(t, root, "main.go", "package main\n")
	gitOutput(t, root, "add", ".")
	gitOutput(t, root, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "base")
	return root
}

// withoutPkill skips Check's sweep for reviews left running from an earlier
// round. The fake sidecars are this machine, where it would kill any real
// `claude -p` the developer has running.
func withoutPkill(run review.Execer) review.Execer {
	return func(ctx context.Context, e *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(string)) (int, error) {
		if strings.Contains(script, "pkill") {
			return 0, nil
		}
		return run(ctx, e, script, env, onOutput, onSubmitted)
	}
}

// TestCheckReportsAReviewerWithOtherCode guards the --verbose canary: a
// reviewer whose tree differs from the implementer's must be reported, not
// matched.
func TestCheckReportsAReviewerWithOtherCode(t *testing.T) {
	ctx := context.Background()
	// rev-2 gains a file the implementer never wrote just before its change
	// is fingerprinted, as a relay that went wrong would leave it.
	divert := func(run review.Execer) review.Execer {
		return func(ctx context.Context, e *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(string)) (int, error) {
			if e.ID == "rev-2" && strings.Contains(script, "git add -A -N") {
				script = strings.Replace(script, "git reset -q", "echo stray > stray.txt && git reset -q", 1)
			}
			return run(ctx, e, script, env, onOutput, onSubmitted)
		}
	}
	s, impl, _, _ := newSidecarsFixture(t, divert)
	var trees []ReviewerTree
	s.OnReviewerTree = func(rt ReviewerTree) { trees = append(trees, rt) }

	writeFile(t, impl.RepoPath, "main.go", "package main\n\n// v1\n")
	_, err := s.Collect(ctx)
	assert.NilError(t, err)
	_, err = s.Check(ctx, 1)
	assert.NilError(t, err)

	assert.Equal(t, len(trees), 2)
	assert.Assert(t, trees[0].Matches(), "%+v", trees[0])
	assert.Equal(t, trees[1].SidecarID, "rev-2")
	assert.Assert(t, !trees[1].Matches(), "a reviewer with other code matched: %+v", trees[1])
	assert.NilError(t, trees[1].Err)
	assert.Assert(t, trees[1].Fingerprint != "")
}
