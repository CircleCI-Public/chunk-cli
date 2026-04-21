package validate

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockPath returns the advisory lock file path for a given workDir.
func lockPath(workDir string) string {
	return filepath.Join(cacheDir(workDir), "validate.lock")
}

// TryLock attempts to acquire an exclusive advisory lock for running
// validation in workDir. It returns a release function and true on success.
// If the lock is already held (another validate is running), it returns
// a no-op release and false. The lock is automatically released when the
// process exits.
func TryLock(workDir string) (release func(), acquired bool) {
	dir := cacheDir(workDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}, true // can't lock, proceed anyway
	}

	f, err := os.OpenFile(lockPath(workDir), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return func() {}, true // can't open lock file, proceed anyway
	}

	// LOCK_EX|LOCK_NB: exclusive, non-blocking. Returns EWOULDBLOCK if locked.
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return func() {}, false
	}

	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true
}
