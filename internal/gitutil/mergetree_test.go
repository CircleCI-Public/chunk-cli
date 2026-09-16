package gitutil

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

// commitFile writes rel and commits it on the current branch.
func commitFile(t *testing.T, dir, rel, content, msg string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	assert.NilError(t, err)
	err = os.WriteFile(path, []byte(content), 0o644)
	assert.NilError(t, err)
	gitRun(t, dir, "add", rel)
	gitRun(t, dir, "commit", "-m", msg)
}

func TestPreviewMergeCleanWhenBranchesTouchDifferentFiles(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "base.txt", "base\n", "base")

	gitRun(t, dir, "checkout", "-b", "feature")
	commitFile(t, dir, "feature.txt", "feature\n", "feature")

	gitRun(t, dir, "checkout", "main")
	commitFile(t, dir, "other.txt", "other\n", "other")

	preview, err := PreviewMerge(context.Background(), dir, "feature", "main")
	assert.NilError(t, err)
	assert.Check(t, preview.Clean)
	assert.Check(t, cmp.Len(preview.Paths, 0))
}

func TestPreviewMergeReportsConflictingPaths(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "shared.txt", "original\n", "base")

	gitRun(t, dir, "checkout", "-b", "feature")
	commitFile(t, dir, "shared.txt", "feature edit\n", "feature")

	gitRun(t, dir, "checkout", "main")
	commitFile(t, dir, "shared.txt", "main edit\n", "main")

	preview, err := PreviewMerge(context.Background(), dir, "feature", "main")
	assert.NilError(t, err)
	assert.Check(t, !preview.Clean)
	// Only the conflicted path, not git's "CONFLICT (content): ..." prose that
	// follows it in the same output.
	assert.Check(t, cmp.DeepEqual(preview.Paths, []string{"shared.txt"}))
}

func TestPreviewMergeLeavesTheWorkingTreeAlone(t *testing.T) {
	// The whole reason merge-tree is the primitive: this runs against a
	// checkout somebody is working in, and must not touch their index, HEAD, or
	// files. A real `git merge` here would leave conflict markers on disk.
	dir := setupRepo(t)
	commitFile(t, dir, "shared.txt", "original\n", "base")

	gitRun(t, dir, "checkout", "-b", "feature")
	commitFile(t, dir, "shared.txt", "feature edit\n", "feature")

	gitRun(t, dir, "checkout", "main")
	commitFile(t, dir, "shared.txt", "main edit\n", "main")

	// An uncommitted edit, to prove the preview neither reads nor disturbs it.
	err := os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("uncommitted\n"), 0o644)
	assert.NilError(t, err)

	headBefore := gitRun(t, dir, "rev-parse", "HEAD")
	branchBefore := gitRun(t, dir, "rev-parse", "--abbrev-ref", "HEAD")

	preview, err := PreviewMerge(context.Background(), dir, "feature", "main")
	assert.NilError(t, err)
	assert.Check(t, !preview.Clean)

	headAfter := gitRun(t, dir, "rev-parse", "HEAD")
	branchAfter := gitRun(t, dir, "rev-parse", "--abbrev-ref", "HEAD")
	assert.Check(t, cmp.Equal(headAfter, headBefore))
	assert.Check(t, cmp.Equal(branchAfter, branchBefore))

	// The uncommitted content survives untouched, and is still the only thing
	// git reports as changed — no merge state was left behind.
	content, err := os.ReadFile(filepath.Join(dir, "shared.txt"))
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(string(content), "uncommitted\n"))
	// gitRun trims, so the porcelain status arrives without its leading column.
	status := gitRun(t, dir, "status", "--porcelain")
	assert.Check(t, cmp.Equal(status, "M shared.txt"))
}

func TestPreviewMergeIgnoresUncommittedConflicts(t *testing.T) {
	// A conflict that exists only in unstaged edits is invisible here, because
	// the merge is of commits. Pinned as a test because every caller has to say
	// so out loud, or the silence reads as an all-clear it did not check.
	dir := setupRepo(t)
	commitFile(t, dir, "shared.txt", "original\n", "base")

	gitRun(t, dir, "checkout", "-b", "feature")
	commitFile(t, dir, "untouched.txt", "x\n", "feature")

	gitRun(t, dir, "checkout", "main")
	commitFile(t, dir, "shared.txt", "main edit\n", "main")
	gitRun(t, dir, "checkout", "feature")

	// Would conflict with main's committed change if it were committed.
	err := os.WriteFile(filepath.Join(dir, "shared.txt"), []byte("feature edit\n"), 0o644)
	assert.NilError(t, err)

	preview, err := PreviewMerge(context.Background(), dir, "feature", "main")
	assert.NilError(t, err)
	assert.Check(t, preview.Clean, "uncommitted edits must not register as a conflict")
}

func TestPreviewMergeErrorsOnUnknownRevision(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "base.txt", "base\n", "base")

	_, err := PreviewMerge(context.Background(), dir, "main", "no-such-branch")
	// git exits 1 for this, the same code as a conflicted merge, so a reading
	// that trusted the exit code alone would report a conflict in no files.
	assert.Check(t, err != nil)
	assert.Check(t, cmp.ErrorContains(err, "not something we can merge"))
}

func TestDefaultRemoteBranchInReadsRemoteHead(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "base.txt", "base\n", "base")
	gitRun(t, dir, "remote", "add", "origin", "https://example.com/x/y.git")
	gitRun(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")

	remote, branch, err := DefaultRemoteBranchIn(dir)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(remote, "origin"))
	assert.Check(t, cmp.Equal(branch, "main"))
}

func TestDefaultRemoteBranchInPrefersOriginOverUpstream(t *testing.T) {
	// A fork checkout has both. origin wins, because that is the remote whose
	// tracking ref a fetch would refresh.
	dir := setupRepo(t)
	commitFile(t, dir, "base.txt", "base\n", "base")
	gitRun(t, dir, "remote", "add", "origin", "https://example.com/me/y.git")
	gitRun(t, dir, "remote", "add", "upstream", "https://example.com/them/y.git")
	gitRun(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	gitRun(t, dir, "symbolic-ref", "refs/remotes/upstream/HEAD", "refs/remotes/upstream/trunk")

	remote, branch, err := DefaultRemoteBranchIn(dir)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(remote, "origin"))
	assert.Check(t, cmp.Equal(branch, "main"))
}

func TestDefaultRemoteBranchInFallsBackToUpstream(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "base.txt", "base\n", "base")
	gitRun(t, dir, "remote", "add", "upstream", "https://example.com/them/y.git")
	gitRun(t, dir, "symbolic-ref", "refs/remotes/upstream/HEAD", "refs/remotes/upstream/trunk")

	remote, branch, err := DefaultRemoteBranchIn(dir)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(remote, "upstream"))
	assert.Check(t, cmp.Equal(branch, "trunk"))
}

func TestDefaultRemoteBranchInErrorsWithoutRemoteHead(t *testing.T) {
	dir := setupRepo(t)
	_, _, err := DefaultRemoteBranchIn(dir)
	assert.Check(t, err != nil)
}

func TestRevParseCtxRejectsNonCommits(t *testing.T) {
	dir := setupRepo(t)
	commitFile(t, dir, "base.txt", "base\n", "base")

	sha, err := RevParseCtx(context.Background(), dir, "main")
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(sha, 40))

	// A tree is a valid object but not a commit, and must not resolve here —
	// merge-tree would fail confusingly if one reached it.
	treeSHA := gitRun(t, dir, "rev-parse", "main^{tree}")
	_, err = RevParseCtx(context.Background(), dir, treeSHA)
	assert.Check(t, err != nil)
}

func TestFetchRemoteBranchUpdatesTrackingRef(t *testing.T) {
	// A real fetch between two local repositories. The refspec is explicit in
	// FetchRemoteBranch precisely so this works regardless of what the remote's
	// configured fetch refspec happens to be, so a local remote is a fair test.
	upstream := setupRepo(t)
	commitFile(t, upstream, "base.txt", "base\n", "base")

	clone := setupRepo(t)
	gitRun(t, clone, "remote", "add", "origin", upstream)

	err := FetchRemoteBranch(context.Background(), clone, "origin", "main")
	assert.NilError(t, err)

	fetched, err := RevParseCtx(context.Background(), clone, "origin/main")
	assert.NilError(t, err)
	want := gitRun(t, upstream, "rev-parse", "main")
	assert.Check(t, cmp.Equal(fetched, want))

	// A second commit upstream is picked up by a second fetch, so the tracking
	// ref genuinely advances rather than being written once at add time.
	commitFile(t, upstream, "next.txt", "next\n", "next")
	err = FetchRemoteBranch(context.Background(), clone, "origin", "main")
	assert.NilError(t, err)
	advanced, err := RevParseCtx(context.Background(), clone, "origin/main")
	assert.NilError(t, err)
	wantAdvanced := gitRun(t, upstream, "rev-parse", "main")
	assert.Check(t, cmp.Equal(advanced, wantAdvanced))
}

func TestFetchRemoteBranchErrorsOnMissingRemote(t *testing.T) {
	dir := setupRepo(t)
	err := FetchRemoteBranch(context.Background(), dir, "origin", "main")
	assert.Check(t, err != nil)
}
