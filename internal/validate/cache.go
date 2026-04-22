package validate

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// cacheDir returns the per-workDir directory used for validate state files
// (lock, attempts). It is keyed on the absolute path of workDir.
func cacheDir(workDir string) string {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		abs = workDir
	}
	h := sha256.Sum256([]byte(abs))
	return filepath.Join(os.TempDir(), "chunk-run", "cache", fmt.Sprintf("%x", h[:8]))
}

// computeContentHash returns a hash of the current HEAD commit plus the diff
// against HEAD, used to detect whether uncommitted changes have shifted
// between Stop hook invocations.
func computeContentHash(workDir string) string {
	h := sha256.New()

	head, err := exec.Command("git", "-C", workDir, "rev-parse", "HEAD").Output()
	if err != nil {
		h.Write([]byte("no-head"))
	} else {
		h.Write([]byte(strings.TrimSpace(string(head))))
	}
	h.Write([]byte("\n"))

	diff, err := exec.Command("git", "-C", workDir, "diff", "HEAD").Output()
	if err == nil {
		h.Write(diff)
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}
