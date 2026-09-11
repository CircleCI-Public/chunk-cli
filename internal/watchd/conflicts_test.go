package watchd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// git runs a git command in dir with an isolated environment, so a developer's
// own global config cannot change what these tests observe.
func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{
		fmt.Sprintf("HOME=%s", dir),
		fmt.Sprintf("PATH=%s", os.Getenv("PATH")),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@test.com",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@test.com",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s failed: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeCommit(t *testing.T, dir, rel, content, msg string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644)
	assert.NilError(t, err)
	git(t, dir, "add", rel)
	git(t, dir, "commit", "-m", msg)
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-b", "main")
	writeCommit(t, dir, "base.txt", "base\n", "base")
	return dir
}

// cloneWithRemoteHead makes a clone of upstream with origin/HEAD recorded, which
// is what DefaultRemoteBranchIn reads to find the merge target.
func cloneWithRemoteHead(t *testing.T, upstream string) string {
	t.Helper()
	clone := t.TempDir()
	git(t, clone, "init", "-b", "main")
	git(t, clone, "remote", "add", "origin", upstream)
	git(t, clone, "fetch", "origin")
	git(t, clone, "reset", "--hard", "origin/main")
	git(t, clone, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return clone
}

func TestEvaluateConflictReportsConflictAgainstDefaultBranch(t *testing.T) {
	upstream := initRepo(t)
	writeCommit(t, upstream, "shared.txt", "original\n", "shared")

	clone := cloneWithRemoteHead(t, upstream)
	git(t, clone, "checkout", "-b", "feature")
	writeCommit(t, clone, "shared.txt", "feature edit\n", "feature")

	// The merge target moves after the branch forked, which is what creates the
	// conflict the developer has not seen yet.
	writeCommit(t, upstream, "shared.txt", "upstream edit\n", "upstream")

	state, _ := evaluateConflict(context.Background(), clone, nil, time.Time{})
	assert.Assert(t, state != nil)
	assert.Check(t, cmp.Equal(state.Unavailable, ""))
	assert.Check(t, state.Conflicted)
	assert.Check(t, cmp.Equal(state.Branch, "feature"))
	assert.Check(t, cmp.Equal(state.Target, "origin/main"))
	assert.Check(t, cmp.DeepEqual(state.Paths, []string{"shared.txt"}))
	assert.Check(t, cmp.Equal(state.TotalPaths, 1))
}

func TestEvaluateConflictFetchesTheMergeTarget(t *testing.T) {
	// Without the background fetch the tracking ref would still point at the
	// commit the clone was made from, and the conflict above would be invisible.
	upstream := initRepo(t)
	writeCommit(t, upstream, "shared.txt", "original\n", "shared")

	clone := cloneWithRemoteHead(t, upstream)
	beforeFetch := git(t, clone, "rev-parse", "origin/main")

	git(t, clone, "checkout", "-b", "feature")
	writeCommit(t, clone, "shared.txt", "feature edit\n", "feature")
	writeCommit(t, upstream, "shared.txt", "upstream edit\n", "upstream")

	state, fetchedAt := evaluateConflict(context.Background(), clone, nil, time.Time{})
	assert.Assert(t, state != nil)

	afterFetch := git(t, clone, "rev-parse", "origin/main")
	assert.Check(t, afterFetch != beforeFetch, "the fetch must advance origin/main")
	assert.Check(t, cmp.Equal(state.TargetSHA, afterFetch))
	assert.Check(t, !fetchedAt.IsZero(), "a fetch that ran must be recorded")
	assert.Check(t, !state.TargetStale)
}

func TestEvaluateConflictSkipsFetchWithinInterval(t *testing.T) {
	upstream := initRepo(t)
	clone := cloneWithRemoteHead(t, upstream)
	git(t, clone, "checkout", "-b", "feature")
	writeCommit(t, clone, "feature.txt", "feature\n", "feature")

	// Upstream moves, but the last fetch was recent, so this pass must not go
	// back to the remote — the whole point of FetchInterval.
	writeCommit(t, upstream, "other.txt", "other\n", "other")
	recent := time.Now()

	before := git(t, clone, "rev-parse", "origin/main")
	state, fetchedAt := evaluateConflict(context.Background(), clone, nil, recent)
	after := git(t, clone, "rev-parse", "origin/main")

	assert.Assert(t, state != nil)
	assert.Check(t, cmp.Equal(after, before), "origin/main must not move when the fetch is skipped")
	assert.Check(t, cmp.Equal(fetchedAt, recent), "the prior fetch time must carry forward")
	assert.Check(t, cmp.Equal(state.TargetFetchedAt, recent))
}

func TestEvaluateConflictCleanBranchReportsNoConflict(t *testing.T) {
	upstream := initRepo(t)
	clone := cloneWithRemoteHead(t, upstream)
	git(t, clone, "checkout", "-b", "feature")
	writeCommit(t, clone, "feature.txt", "feature\n", "feature")
	writeCommit(t, upstream, "other.txt", "other\n", "other")

	state, _ := evaluateConflict(context.Background(), clone, nil, time.Time{})
	assert.Assert(t, state != nil)
	// A real all-clear: not conflicted, and no reason it could not check.
	assert.Check(t, !state.Conflicted)
	assert.Check(t, cmp.Equal(state.Unavailable, ""))
	assert.Check(t, cmp.Len(state.Paths, 0))
}

func TestEvaluateConflictReusesResultWhenNeitherSideMoved(t *testing.T) {
	// The cache that keeps an idle daemon from re-merging the same two commits
	// every minute. Detected by handing back a prior result whose conflict flag
	// contradicts the repository: if it were recomputed, the answer would flip.
	upstream := initRepo(t)
	clone := cloneWithRemoteHead(t, upstream)
	git(t, clone, "checkout", "-b", "feature")
	writeCommit(t, clone, "feature.txt", "feature\n", "feature")

	first, _ := evaluateConflict(context.Background(), clone, nil, time.Time{})
	assert.Assert(t, first != nil)
	assert.Check(t, !first.Conflicted)

	poisoned := *first
	poisoned.Conflicted = true
	poisoned.Paths = []string{"stale.txt"}

	second, _ := evaluateConflict(context.Background(), clone, &poisoned, time.Now())
	assert.Assert(t, second != nil)
	assert.Check(t, second.Conflicted, "an unchanged HEAD and target must reuse the prior answer")
	assert.Check(t, cmp.DeepEqual(second.Paths, []string{"stale.txt"}))
}

func TestEvaluateConflictRecomputesWhenHeadMoves(t *testing.T) {
	upstream := initRepo(t)
	writeCommit(t, upstream, "shared.txt", "original\n", "shared")
	clone := cloneWithRemoteHead(t, upstream)
	git(t, clone, "checkout", "-b", "feature")
	writeCommit(t, clone, "feature.txt", "feature\n", "feature")

	first, fetchedAt := evaluateConflict(context.Background(), clone, nil, time.Time{})
	assert.Assert(t, first != nil)
	assert.Check(t, !first.Conflicted)

	// A new commit on the branch conflicts with the target, and must be seen
	// even though the target itself has not moved.
	writeCommit(t, upstream, "shared.txt", "upstream edit\n", "upstream")
	git(t, clone, "fetch", "origin")
	writeCommit(t, clone, "shared.txt", "feature edit\n", "conflicting")

	second, _ := evaluateConflict(context.Background(), clone, first, fetchedAt)
	assert.Assert(t, second != nil)
	assert.Check(t, second.Conflicted, "a moved HEAD must be re-merged, not served from cache")
	assert.Check(t, cmp.DeepEqual(second.Paths, []string{"shared.txt"}))
}

func TestEvaluateConflictOnDefaultBranchHasNothingToCompare(t *testing.T) {
	upstream := initRepo(t)
	clone := cloneWithRemoteHead(t, upstream)

	state, _ := evaluateConflict(context.Background(), clone, nil, time.Time{})
	assert.Assert(t, state != nil)
	assert.Check(t, !state.Conflicted)
	// Reported rather than silently clean: a manual run has to be able to say
	// why it found nothing.
	assert.Check(t, cmp.Contains(state.Unavailable, "merge target"))
}

func TestEvaluateConflictWithoutRemoteHeadIsUnavailable(t *testing.T) {
	dir := initRepo(t)
	git(t, dir, "checkout", "-b", "feature")

	state, _ := evaluateConflict(context.Background(), dir, nil, time.Time{})
	assert.Assert(t, state != nil)
	assert.Check(t, !state.Conflicted)
	assert.Check(t, cmp.Contains(state.Unavailable, "no default branch"))
}

func TestEvaluateConflictDetachedHeadIsUnavailable(t *testing.T) {
	upstream := initRepo(t)
	clone := cloneWithRemoteHead(t, upstream)
	writeCommit(t, clone, "extra.txt", "extra\n", "extra")
	head := git(t, clone, "rev-parse", "HEAD")
	git(t, clone, "checkout", "--detach", head)

	state, _ := evaluateConflict(context.Background(), clone, nil, time.Time{})
	assert.Assert(t, state != nil)
	assert.Check(t, !state.Conflicted)
	assert.Check(t, cmp.Contains(state.Unavailable, "detached"))
}

func TestEvaluateConflictUnreachableRemoteStillAnswersButMarksStale(t *testing.T) {
	// A failed fetch is not a failed check: the tracking ref on disk still
	// supports a useful answer, and TargetStale is what stops it being
	// presented as current.
	upstream := initRepo(t)
	writeCommit(t, upstream, "shared.txt", "original\n", "shared")
	clone := cloneWithRemoteHead(t, upstream)
	git(t, clone, "checkout", "-b", "feature")
	writeCommit(t, clone, "shared.txt", "feature edit\n", "feature")

	// Point origin at a path that does not exist, so the fetch cannot succeed.
	git(t, clone, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "gone"))

	state, _ := evaluateConflict(context.Background(), clone, nil, time.Time{})
	assert.Assert(t, state != nil)
	assert.Check(t, state.TargetStale, "a failed fetch must be reported as stale")
	// The comparison still ran, against the ref the clone already had.
	assert.Check(t, cmp.Equal(state.Unavailable, ""))
	assert.Check(t, cmp.Equal(state.Target, "origin/main"))
}

func TestEvaluateConflictCapsReportedPaths(t *testing.T) {
	upstream := initRepo(t)
	for i := range MaxConflictPaths + 5 {
		writeCommit(t, upstream, fmt.Sprintf("f%d.txt", i), "original\n", "add")
	}
	clone := cloneWithRemoteHead(t, upstream)

	git(t, clone, "checkout", "-b", "feature")
	for i := range MaxConflictPaths + 5 {
		err := os.WriteFile(filepath.Join(clone, fmt.Sprintf("f%d.txt", i)), []byte("feature\n"), 0o644)
		assert.NilError(t, err)
	}
	git(t, clone, "add", ".")
	git(t, clone, "commit", "-m", "feature edits")

	git(t, clone, "checkout", "main")
	for i := range MaxConflictPaths + 5 {
		err := os.WriteFile(filepath.Join(upstream, fmt.Sprintf("f%d.txt", i)), []byte("upstream\n"), 0o644)
		assert.NilError(t, err)
	}
	git(t, upstream, "add", ".")
	git(t, upstream, "commit", "-m", "upstream edits")
	git(t, clone, "checkout", "feature")

	state, _ := evaluateConflict(context.Background(), clone, nil, time.Time{})
	assert.Assert(t, state != nil)
	assert.Check(t, state.Conflicted)
	assert.Check(t, cmp.Len(state.Paths, MaxConflictPaths))
	// The real count survives the cut, so a reader is never left inferring it
	// from a list that happens to sit exactly on the cap.
	assert.Check(t, cmp.Equal(state.TotalPaths, MaxConflictPaths+5))
}
