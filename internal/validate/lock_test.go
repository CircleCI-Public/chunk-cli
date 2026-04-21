package validate

import (
	"testing"

	"gotest.tools/v3/assert"
)

func TestTryLockAcquiresAndReleases(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)

	release, acquired := TryLock(dir)
	assert.Assert(t, acquired, "expected first lock to be acquired")

	// Second attempt while first is held should fail.
	_, acquired2 := TryLock(dir)
	assert.Assert(t, !acquired2, "expected second lock to be denied while first is held")

	// After release, lock should be acquirable again.
	release()
	release3, acquired3 := TryLock(dir)
	assert.Assert(t, acquired3, "expected lock to be acquirable after release")
	release3()
}
