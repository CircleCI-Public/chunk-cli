package watchd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

// The task may be about CI, so CI definitions are the implementer's to change;
// git's own directory and anything outside the repository never are.
func TestCheckPatchPathsRefusesGitInternalsAndOutsideThePatch(t *testing.T) {
	diff := func(p string) string { return "diff --git a/" + p + " b/" + p + "\n" }
	for _, ok := range []string{"src/a.go", ".githubish/x", ".github/workflows/x.yml", ".circleci/config.yml"} {
		assert.NilError(t, checkPatchPaths(diff(ok)), ok)
	}
	for _, bad := range []string{".git/hooks/pre-commit", ".git/config", "../outside.txt", "a/../../b.txt"} {
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
