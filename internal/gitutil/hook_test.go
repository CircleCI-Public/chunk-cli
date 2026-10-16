package gitutil

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
)

const testHookCommand = "chunk validate"

func TestCommonDirNotARepo(t *testing.T) {
	_, err := CommonDirCtx(context.Background(), t.TempDir())
	assert.Assert(t, errors.Is(err, ErrNotGitRepo), "got %v", err)
}

func TestCommonDirIsAbsolute(t *testing.T) {
	dir := setupRepo(t)
	got, err := CommonDirCtx(context.Background(), dir)
	assert.NilError(t, err)
	assert.Assert(t, filepath.IsAbs(got), "got %q", got)
	assert.Equal(t, filepath.Base(got), ".git")
}

// From a subdirectory git answers relative to its own working directory, which
// is the directory with its symlinks resolved. A symlink to a directory deeper
// than the link itself changes how many ".." reach the repository, so the
// answer has to be joined to the resolved directory, not the one given.
func TestCommonDirFromSubdirectory(t *testing.T) {
	repo := setupRepo(t)
	sub := filepath.Join(repo, "a", "b")
	assert.NilError(t, os.MkdirAll(sub, 0o755))
	want, err := filepath.EvalSymlinks(filepath.Join(repo, ".git"))
	assert.NilError(t, err)

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Skipf("cannot create symlinks: %v", err)
	}

	for name, dir := range map[string]string{"subdirectory": sub, "symlinked subdirectory": link} {
		t.Run(name, func(t *testing.T) {
			got, err := CommonDirCtx(context.Background(), dir)
			assert.NilError(t, err)
			got, err = filepath.EvalSymlinks(got)
			assert.NilError(t, err, "the common dir must exist")
			assert.Equal(t, got, want)
		})
	}
}

func TestEnsurePreCommitNewFile(t *testing.T) {
	gitDir := filepath.Join(setupRepo(t), ".git")

	outcome, err := EnsurePreCommit(gitDir, testHookCommand)
	assert.NilError(t, err)
	assert.Equal(t, outcome, HookCreated)

	path := filepath.Join(gitDir, "hooks", "pre-commit")
	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, string(data), "#!/bin/sh\nchunk validate\n")

	info, err := os.Stat(path)
	assert.NilError(t, err)
	assert.Assert(t, info.Mode()&0o111 != 0, "pre-commit hook must be executable")
}

func TestEnsurePreCommitAlreadyUpToDate(t *testing.T) {
	gitDir := filepath.Join(setupRepo(t), ".git")

	_, err := EnsurePreCommit(gitDir, testHookCommand)
	assert.NilError(t, err)
	outcome, err := EnsurePreCommit(gitDir, testHookCommand)
	assert.NilError(t, err)
	assert.Equal(t, outcome, HookUpToDate)

	data, err := os.ReadFile(filepath.Join(gitDir, "hooks", "pre-commit"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), "#!/bin/sh\nchunk validate\n")
}

func TestEnsurePreCommitAppendsToExisting(t *testing.T) {
	gitDir := filepath.Join(setupRepo(t), ".git")
	hooksDir := filepath.Join(gitDir, "hooks")
	assert.NilError(t, os.MkdirAll(hooksDir, 0o755))

	existing := "#!/bin/sh\nnpm test"
	assert.NilError(t, os.WriteFile(filepath.Join(hooksDir, "pre-commit"), []byte(existing), 0o755))

	outcome, err := EnsurePreCommit(gitDir, testHookCommand)
	assert.NilError(t, err)
	assert.Equal(t, outcome, HookAppended)

	data, err := os.ReadFile(filepath.Join(hooksDir, "pre-commit"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), existing+"\nchunk validate\n")

	info, err := os.Stat(filepath.Join(hooksDir, "pre-commit"))
	assert.NilError(t, err)
	assert.Assert(t, info.Mode()&0o111 != 0, "pre-commit hook must be executable")
}

func TestEnsurePreCommitSymlinkSkipped(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("symlink test not meaningful as root")
	}
	dir := setupRepo(t)
	hooksDir := filepath.Join(dir, ".git", "hooks")
	assert.NilError(t, os.MkdirAll(hooksDir, 0o755))

	// The target does not run the command, so the append path would normally
	// be taken.
	target := filepath.Join(dir, "pre-commit-real")
	assert.NilError(t, os.WriteFile(target, []byte("#!/bin/sh\nnpm test\n"), 0o755))
	hookPath := filepath.Join(hooksDir, "pre-commit")
	assert.NilError(t, os.Symlink(target, hookPath))

	outcome, err := EnsurePreCommit(filepath.Join(dir, ".git"), testHookCommand)
	assert.NilError(t, err)
	assert.Equal(t, outcome, HookSymlink)

	linfo, err := os.Lstat(hookPath)
	assert.NilError(t, err)
	assert.Assert(t, linfo.Mode()&os.ModeSymlink != 0, "pre-commit must still be a symlink")
	data, err := os.ReadFile(target)
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(data), testHookCommand), "symlink target must not have been modified")
}
