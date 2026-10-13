package commandutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func writeGoFile(t *testing.T, workDir, path string) {
	t.Helper()
	full := filepath.Join(workDir, path)
	assert.NilError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	assert.NilError(t, os.WriteFile(full, []byte("package "+filepath.Base(filepath.Dir(full))+"\n"), 0o644))
}

func git(t *testing.T, workDir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = workDir
	cmd.Env = gitrepo.GitEnv(workDir)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))
}

func TestExpandCommandNamesChangedPackages(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "o", "r")
	writeGoFile(t, workDir, "a/a.go")
	writeGoFile(t, workDir, "b/b.go")
	git(t, workDir, "add", ".")
	git(t, workDir, "commit", "-m", "add")
	writeGoFile(t, workDir, "a/a2.go")
	git(t, workDir, "add", ".")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "b", "b.go"), []byte("package b\n\nvar X = 1\n"), 0o644))

	got := ExpandCommand(workDir, "go test {{CHANGED_PACKAGES}}")

	assert.Equal(t, got, "go test ./a ./b")
}

// A package the change deletes is left out: go test fails on a directory
// that is gone.
func TestExpandCommandLeavesOutDeletedPackages(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "o", "r")
	writeGoFile(t, workDir, "gone/gone.go")
	writeGoFile(t, workDir, "kept/kept.go")
	git(t, workDir, "add", ".")
	git(t, workDir, "commit", "-m", "add")
	git(t, workDir, "rm", "-rq", "gone")
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, "kept", "kept.go"), []byte("package kept\n\nvar X = 1\n"), 0o644))

	got := ExpandCommand(workDir, "go test {{CHANGED_PACKAGES}}")

	assert.Equal(t, got, "go test ./kept")
}

func TestExpandCommandFallsBackToAllWhenOnlyDeletionsChanged(t *testing.T) {
	workDir := gitrepo.SetupGitRepo(t, "o", "r")
	writeGoFile(t, workDir, "gone/gone.go")
	git(t, workDir, "add", ".")
	git(t, workDir, "commit", "-m", "add")
	git(t, workDir, "rm", "-rq", "gone")

	got := ExpandCommand(workDir, "go test {{CHANGED_PACKAGES}}")

	assert.Equal(t, got, "go test ./...")
}
