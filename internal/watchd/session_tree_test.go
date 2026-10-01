package watchd

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
)

// These tests run real git in temp directories: they are about what happens to a
// person's files, and a fake git would only prove the fake.

func put(t *testing.T, dir, rel, content string) {
	t.Helper()
	full := filepath.Join(dir, rel)
	assert.NilError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	assert.NilError(t, os.WriteFile(full, []byte(content), 0o644))
}

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, rel))
	assert.NilError(t, err)
	return string(data)
}

func exists(dir, rel string) bool {
	_, err := os.Stat(filepath.Join(dir, rel))
	return err == nil
}

// userRepo is a repository in the state a developer's is mid-task: committed
// files, a file with a staged edit and a further unstaged edit, an untracked
// file, and an ignored one.
func userRepo(t *testing.T) string {
	t.Helper()
	root := initRepo(t)
	put(t, root, ".gitignore", "ignored.log\n")
	put(t, root, "tracked.txt", "t0\n")
	put(t, root, "gone.txt", "g0\n")
	put(t, root, "staged.txt", "s0\n")
	git(t, root, "add", "-A")
	git(t, root, "commit", "-q", "-m", "files")

	put(t, root, "staged.txt", "s1\n")
	git(t, root, "add", "staged.txt")
	put(t, root, "staged.txt", "s2 edited\n") // unstaged on top of the staged edit
	put(t, root, "untracked.txt", "u0\n")
	put(t, root, "ignored.log", "ig\n")
	return root
}

// patchFor produces the patch a sandbox agent would: edit a copy of the user's
// files, then diff the copy's tree against the user's.
func patchFor(t *testing.T, root string, edit func(dir string)) string {
	t.Helper()
	before, err := treeNow(root)
	assert.NilError(t, err)
	copyDir := filepath.Join(t.TempDir(), "sandbox")
	assert.NilError(t, exec.Command("cp", "-a", root, copyDir).Run())
	edit(copyDir)
	after, err := treeNow(copyDir)
	assert.NilError(t, err)
	return git(t, copyDir, "diff", "--binary", "--no-renames", before, after) + "\n"
}

func writePatch(t *testing.T, patch string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "fix.patch")
	assert.NilError(t, os.WriteFile(p, []byte(patch), 0o600))
	return p
}

func paths(files []FileChange) []string {
	var out []string
	for _, f := range files {
		out = append(out, f.Path)
	}
	return out
}

func TestRestoreUndoesEverythingTheFixesChangedAndNothingElse(t *testing.T) {
	root := userRepo(t)
	before, err := treeNow(root)
	assert.NilError(t, err)
	head := git(t, root, "rev-parse", "HEAD")
	indexBefore := git(t, root, "ls-files", "--stage")

	rp, err := saveRestorePoint(t.Context(), root, "sess-1", before)
	assert.NilError(t, err)
	assert.Equal(t, rp.HeadSHA, head)

	patch := patchFor(t, root, func(d string) {
		put(t, d, "tracked.txt", "t1\n")             // tracked, unmodified by the user
		put(t, d, "staged.txt", "s3 edited again\n") // staged edit plus unstaged edit
		put(t, d, "untracked.txt", "u1\n")           // never committed
		assert.NilError(t, os.Remove(filepath.Join(d, "gone.txt")))
		put(t, d, "new/dir/created.txt", "c\n") // a file in a new directory
	})
	files, err := applyPatchToTree(t.Context(), root, writePatch(t, patch))
	assert.NilError(t, err)
	got := paths(files)
	slices.Sort(got)
	assert.DeepEqual(t, got, []string{"gone.txt", "new/dir/created.txt", "staged.txt", "tracked.txt", "untracked.txt"})

	// The fixes are in the working tree...
	assert.Equal(t, read(t, root, "tracked.txt"), "t1\n")
	assert.Equal(t, read(t, root, "staged.txt"), "s3 edited again\n")
	assert.Assert(t, !exists(root, "gone.txt"))
	assert.Assert(t, exists(root, "new/dir/created.txt"))
	// ...and only there: the index and HEAD are exactly as the user left them.
	assert.Equal(t, git(t, root, "ls-files", "--stage"), indexBefore)
	assert.Equal(t, git(t, root, "rev-parse", "HEAD"), head)
	assert.Equal(t, read(t, root, "ignored.log"), "ig\n")

	left, err := treeNow(root)
	assert.NilError(t, err)
	restored, err := restoreSession(t.Context(), root, rp, paths(files), left, false)
	assert.NilError(t, err)
	assert.Equal(t, len(restored), 5)

	// Every byte is back: tracked, staged-plus-unstaged, deleted, untracked and
	// created files alike.
	after, err := treeNow(root)
	assert.NilError(t, err)
	assert.Equal(t, after, before, "the working tree must be exactly as it was before the first fix")
	assert.Equal(t, read(t, root, "staged.txt"), "s2 edited\n")
	assert.Equal(t, git(t, root, "ls-files", "--stage"), indexBefore, "restoring does not touch the index")
	assert.Assert(t, !exists(root, "new"), "the directory the fixes created is removed too")
	assert.Equal(t, read(t, root, "ignored.log"), "ig\n")
}

func TestRestorePointSurvivesAsAGitRefThatShowsInNoBranchList(t *testing.T) {
	root := userRepo(t)
	before, err := treeNow(root)
	assert.NilError(t, err)

	rp, err := saveRestorePoint(t.Context(), root, "sess-2", before)
	assert.NilError(t, err)

	assert.Equal(t, rp.Ref, "refs/chunk/restore/sess-2")
	assert.Equal(t, git(t, root, "rev-parse", rp.Ref+"^{tree}"), before)
	assert.Equal(t, git(t, root, "branch", "--list"), "* main")
	assert.Equal(t, git(t, root, "tag", "--list"), "")
	// The documented manual undo works without the daemon.
	put(t, root, "tracked.txt", "damaged\n")
	git(t, root, "restore", "--source="+rp.Ref, "--worktree", "--", "tracked.txt")
	assert.Equal(t, read(t, root, "tracked.txt"), "t0\n")
}

func TestRestoreRefusesToDiscardEditsMadeAfterTheSessionUnlessForced(t *testing.T) {
	root := userRepo(t)
	before, err := treeNow(root)
	assert.NilError(t, err)
	rp, err := saveRestorePoint(t.Context(), root, "sess-3", before)
	assert.NilError(t, err)
	patch := patchFor(t, root, func(d string) {
		put(t, d, "tracked.txt", "t1\n")
		assert.NilError(t, os.Remove(filepath.Join(d, "gone.txt")))
	})
	files, err := applyPatchToTree(t.Context(), root, writePatch(t, patch))
	assert.NilError(t, err)
	left, err := treeNow(root)
	assert.NilError(t, err)

	// The user keeps working: one file the session changed, one it never touched.
	put(t, root, "tracked.txt", "mine\n")
	put(t, root, "unrelated.txt", "also mine\n")

	_, err = restoreSession(t.Context(), root, rp, paths(files), left, false)
	var edited *EditedSinceError
	assert.Assert(t, errors.As(err, &edited), "got %v", err)
	assert.DeepEqual(t, edited.Paths, []string{"tracked.txt"})
	assert.Assert(t, errors.Is(err, ErrEditedSince))
	assert.Equal(t, read(t, root, "tracked.txt"), "mine\n", "their edit survives a refused restore")
	assert.Assert(t, !exists(root, "gone.txt"), "a refused restore writes nothing at all")

	_, err = restoreSession(t.Context(), root, rp, paths(files), left, true)
	assert.NilError(t, err)
	assert.Equal(t, read(t, root, "tracked.txt"), "t0\n")
	assert.Equal(t, read(t, root, "gone.txt"), "g0\n")
	assert.Equal(t, read(t, root, "unrelated.txt"), "also mine\n", "a file the session never touched is never touched by a restore")
}

func TestRestoreLeavesEditsToFilesTheSessionNeverChangedWithoutForce(t *testing.T) {
	root := userRepo(t)
	before, err := treeNow(root)
	assert.NilError(t, err)
	rp, err := saveRestorePoint(t.Context(), root, "sess-4", before)
	assert.NilError(t, err)
	patch := patchFor(t, root, func(d string) { put(t, d, "tracked.txt", "t1\n") })
	files, err := applyPatchToTree(t.Context(), root, writePatch(t, patch))
	assert.NilError(t, err)
	left, err := treeNow(root)
	assert.NilError(t, err)

	put(t, root, "untracked.txt", "edited later\n")

	_, err = restoreSession(t.Context(), root, rp, paths(files), left, false)
	assert.NilError(t, err, "edits to other files are no reason to refuse")
	assert.Equal(t, read(t, root, "tracked.txt"), "t0\n")
	assert.Equal(t, read(t, root, "untracked.txt"), "edited later\n")
}

// A patch that does not fit must change nothing, not even the files it does fit.
func TestApplyingAPatchThatNoLongerFitsChangesNothing(t *testing.T) {
	root := userRepo(t)
	patch := patchFor(t, root, func(d string) {
		put(t, d, "tracked.txt", "t1\n")
		put(t, d, "gone.txt", "g1\n")
	})
	// The user edited one of the two files in the meantime.
	put(t, root, "gone.txt", "g-user\n")
	before, err := treeNow(root)
	assert.NilError(t, err)

	_, err = applyPatchToTree(t.Context(), root, writePatch(t, patch))

	assert.ErrorContains(t, err, "do not apply")
	after, err := treeNow(root)
	assert.NilError(t, err)
	assert.Equal(t, after, before, "no file may be half-fixed")
	assert.Equal(t, read(t, root, "tracked.txt"), "t0\n")
}

// Noticing that the user's files moved must not cry wolf, and must not miss.
func TestTreeNowChangesWithContentAndNotWithNoise(t *testing.T) {
	root := userRepo(t)
	base, err := treeNow(root)
	assert.NilError(t, err)

	again, err := treeNow(root)
	assert.NilError(t, err)
	assert.Equal(t, again, base, "an untouched tree has a stable identity")

	put(t, root, "ignored.log", "changed but ignored\n")
	later := time.Now().Add(time.Hour)
	assert.NilError(t, os.Chtimes(filepath.Join(root, "tracked.txt"), later, later))
	noisy, err := treeNow(root)
	assert.NilError(t, err)
	assert.Equal(t, noisy, base, "ignored files and timestamps are not changes")

	for name, change := range map[string]func(){
		"edit a tracked file":     func() { put(t, root, "tracked.txt", "edited\n") },
		"edit an untracked file":  func() { put(t, root, "untracked.txt", "edited\n") },
		"add an untracked file":   func() { put(t, root, "brand-new.txt", "x\n") },
		"delete a file":           func() { assert.NilError(t, os.Remove(filepath.Join(root, "gone.txt"))) },
		"edit again, same status": func() { put(t, root, "staged.txt", "s9 changed again\n") },
	} {
		prev, err := treeNow(root)
		assert.NilError(t, err)
		change()
		next, err := treeNow(root)
		assert.NilError(t, err)
		assert.Assert(t, prev != next, "%s must change the tree", name)
	}
}

// The two scripts that run in the sandbox are plain shell and git, so they run
// for real here against a stand-in sandbox.
func TestSandboxScriptsCaptureOnlyTheAgentsChangesAndLeaveTheSandboxAsTheyFoundIt(t *testing.T) {
	root := userRepo(t)
	sandbox := filepath.Join(t.TempDir(), "sandbox")
	assert.NilError(t, exec.Command("cp", "-a", root, sandbox).Run()) // "sync": the user's files, dirt included

	sh := func(script string) string {
		out, err := exec.Command("sh", "-c", script).Output()
		assert.NilError(t, err, script)
		return string(out)
	}
	base := strings.TrimSpace(sh(baselineScript(sandbox)))
	assert.Assert(t, treeSHARe.MatchString(base), base)
	startTree, err := treeNow(sandbox)
	assert.NilError(t, err)
	assert.Equal(t, base, startTree, "the baseline is the sandbox's tree as synced")

	// The agent edits, creates and deletes.
	put(t, sandbox, "tracked.txt", "fixed\n")
	put(t, sandbox, "pkg/new.go", "package pkg\n")
	assert.NilError(t, os.Remove(filepath.Join(sandbox, "gone.txt")))

	patch := sh(diffScript(sandbox, base))

	assert.Assert(t, strings.Contains(patch, "tracked.txt") && strings.Contains(patch, "pkg/new.go") && strings.Contains(patch, "gone.txt"))
	for _, theirs := range []string{"staged.txt", "untracked.txt", "ignored.log"} {
		assert.Assert(t, !strings.Contains(patch, theirs), "the user's own work (%s) is not part of the fix", theirs)
	}
	endTree, err := treeNow(sandbox)
	assert.NilError(t, err)
	assert.Equal(t, endTree, base, "the sandbox is put back for the next user of the pool")

	// And the patch is what applies cleanly to the user's real files.
	files, err := applyPatchToTree(t.Context(), root, writePatch(t, patch))
	assert.NilError(t, err)
	assert.Equal(t, len(files), 3)
	assert.Equal(t, read(t, root, "tracked.txt"), "fixed\n")
	assert.Equal(t, read(t, root, "staged.txt"), "s2 edited\n")
}

func TestCheckPatchPathsRefusesCIConfigGitInternalsAndOutsideThePatch(t *testing.T) {
	diff := func(p string) string { return "diff --git a/" + p + " b/" + p + "\n" }
	assert.NilError(t, checkPatchPaths(diff("src/a.go")))
	assert.NilError(t, checkPatchPaths(diff(".githubish/x")))
	for _, bad := range []string{".github/workflows/x.yml", ".github/actions/y/action.yml", ".circleci/config.yml", ".git/hooks/pre-commit", "../outside.txt", "a/../../b.txt"} {
		assert.Assert(t, checkPatchPaths(diff(bad)) != nil, bad)
	}
}

// Each applied file carries its own line counts, read from git's own numstat for
// the patch (not from a made-up format), and they reach JSON under the names
// "insertions" and "deletions".
func TestAppliedFixesCarryLineCountsPerFileInJSON(t *testing.T) {
	root := userRepo(t)
	patch := patchFor(t, root, func(d string) {
		put(t, d, "tracked.txt", "t1\nt2\n")                        // one line replaced by two: +2 -1
		assert.NilError(t, os.Remove(filepath.Join(d, "gone.txt"))) // deleted: +0 -1
		put(t, d, "dir/a b.txt", "x\ny\nz\n")                       // new, with a space in the name: +3 -0
		assert.NilError(t, os.WriteFile(filepath.Join(d, "blob.bin"), []byte{0, 1, 2, 0}, 0o644))
	})

	files, err := applyPatchToTree(t.Context(), root, writePatch(t, patch))
	assert.NilError(t, err)
	slices.SortFunc(files, func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
	assert.DeepEqual(t, files, []FileChange{
		{Path: "blob.bin"}, // binary files report no line counts
		{Path: "dir/a b.txt", Insertions: 3},
		{Path: "gone.txt", Deletions: 1},
		{Path: "tracked.txt", Insertions: 2, Deletions: 1},
	})

	raw, err := json.Marshal(RoundFix{State: FixApplied, Files: files, Insertions: 5, Deletions: 2})
	assert.NilError(t, err)
	var decoded struct {
		Files []map[string]any `json:"files"`
	}
	assert.NilError(t, json.Unmarshal(raw, &decoded))
	for _, f := range decoded.Files {
		assert.Assert(t, f["insertions"] != nil && f["deletions"] != nil, "file %v lacks line counts", f["path"])
	}
}
