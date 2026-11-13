package gitutil

import (
	"os"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"
)

func TestDirtySubmodules(t *testing.T) {
	t.Parallel()

	sub := setupRepo(t)
	commitFile(t, sub, "lib.txt", "lib\n")
	dir := setupRepo(t)
	gitRun(t, dir, "-c", "protocol.file.allow=always", "submodule", "add", sub, "vendor/sub")
	gitRun(t, dir, "commit", "-m", "add submodule")

	got, err := DirtySubmodules(dir)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(got, []string(nil)))

	writeFile(t, dir, "vendor/sub/lib.txt", "edited\n")
	got, err = DirtySubmodules(dir)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(got, []string{"vendor/sub"}))

	gitRun(t, dir+"/vendor/sub", "checkout", "--", "lib.txt")
	writeFile(t, dir, "vendor/sub/new.txt", "new\n")
	got, err = DirtySubmodules(dir)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(got, []string{"vendor/sub"}))

	// Another commit in the submodule moves the commit the snapshot records,
	// so it is in the key and needs no warning.
	err = os.Remove(dir + "/vendor/sub/new.txt")
	assert.NilError(t, err)
	commitFile(t, dir+"/vendor/sub", "more.txt", "more\n")
	got, err = DirtySubmodules(dir)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(got, []string(nil)))
}
