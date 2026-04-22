package validate

import (
	"fmt"
	"io"
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
// warn is an optional writer for diagnostic messages (pass nil to suppress).
func TryLock(workDir string, warn io.Writer) (release func(), acquired bool) {
	dir := cacheDir(workDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// Fail open: if we can't create the lock directory we allow the
		// caller to proceed without mutual exclusion rather than silently
		// skipping validation entirely.
		if warn != nil {
			_, _ = fmt.Fprintf(warn, "chunk validate: cannot create lock directory: %v (proceeding without lock)\n", err)
		}
		return func() {}, true
	}

	f, err := os.OpenFile(lockPath(workDir), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		// Same fail-open policy: prefer running validation without a lock
		// over skipping it due to a filesystem error.
		if warn != nil {
			_, _ = fmt.Fprintf(warn, "chunk validate: cannot open lock file: %v (proceeding without lock)\n", err)
		}
		return func() {}, true
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
