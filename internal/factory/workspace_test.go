package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// newWorkspace returns a workspace over a real git repo, run through localExec
// so the scripts the implementer's sidecar would run are exercised for real.
func newWorkspace(t *testing.T) (*workspace, string) {
	t.Helper()
	dir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	// The repo's own config must not supply an identity: a sidecar image may
	// have none, and the baseline commit must not depend on it.
	return &workspace{exec: localExec(t.TempDir()), entry: &sidecar.PoolEntry{ID: "impl", RepoPath: dir}}, dir
}

func TestWorkspaceBaselineIncludesDevelopersUncommittedWork(t *testing.T) {
	w, dir := newWorkspace(t)
	writeFile(t, dir, "mine.go", "package main\n")

	assert.NilError(t, w.commitBaseline(context.Background()))
	c, err := w.collect(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, c.Empty(), "the developer's own work is baseline, not change: %+v", c)
}

func TestWorkspaceCollectsEditsAndNewFiles(t *testing.T) {
	w, dir := newWorkspace(t)
	writeFile(t, dir, "main.go", "package main\n")
	assert.NilError(t, w.commitBaseline(context.Background()))

	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")
	writeFile(t, dir, "new.go", "package main\n")
	c, err := w.collect(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, c.Stat, "2 files changed, 3 insertions(+)")

	// New files must show in `git diff HEAD`, which is what reviewers read.
	diff := gitOutput(t, dir, "diff", "HEAD", "--name-only")
	assert.Equal(t, diff, "main.go\nnew.go")

	again, err := w.collect(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, again.Fingerprint, c.Fingerprint, "unchanged work fingerprints the same")

	writeFile(t, dir, "new.go", "package main\n\n// edited\n")
	edited, err := w.collect(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, edited.Fingerprint != c.Fingerprint)
}

// TestWorkspaceFoldsImplementerCommitsBack covers an implementer that commits
// despite being told not to: its work must stay visible as the change.
func TestWorkspaceFoldsImplementerCommitsBack(t *testing.T) {
	w, dir := newWorkspace(t)
	assert.NilError(t, w.commitBaseline(context.Background()))

	writeFile(t, dir, "feature.go", "package main\n")
	gitrepo.AddFile(t, dir, "feature.go")
	gitOutput(t, dir, "commit", "-m", "sneaky")

	c, err := w.collect(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, c.Stat, "1 file changed, 1 insertion(+)")
	assert.Equal(t, gitOutput(t, dir, "rev-parse", "HEAD"), w.baseline)
}

func TestWorkspaceCollectNeedsBaseline(t *testing.T) {
	w, _ := newWorkspace(t)
	_, err := w.collect(context.Background())
	assert.ErrorContains(t, err, "no baseline")
}

func TestRunValidation(t *testing.T) {
	dir := t.TempDir()
	entry := &sidecar.PoolEntry{ID: "impl", RepoPath: dir}
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "marker"), nil, 0o644))

	var submitted, done []string
	checks := runValidation(context.Background(), localExec(t.TempDir()), entry, []config.Command{
		// Commands run in the workspace.
		{Name: "in-workspace", Run: "test -f marker"},
		{Name: "failing", Run: "echo compiling; echo 'undefined: foo' >&2; exit 1"},
		{Name: "hangs", Run: "sleep 10", Timeout: 1},
	}, func(c config.Command, commandID string) {
		assert.Assert(t, commandID != "")
		submitted = append(submitted, c.Name)
	}, func(c Check) { done = append(done, c.Name) })

	// Each command reports its remote ID as it starts, so its output can be
	// replayed from the dashboard.
	assert.DeepEqual(t, submitted, []string{"in-workspace", "failing", "hangs"})
	assert.DeepEqual(t, done, []string{"in-workspace", "failing", "hangs"})
	assert.Equal(t, checks[0].Status, StatusPassed)
	assert.Equal(t, checks[1].Status, StatusFailed)
	// Both streams are fed back: compilers report on stderr.
	assert.Assert(t, strings.Contains(checks[1].Feedback, "undefined: foo"), checks[1].Feedback)
	assert.Equal(t, checks[2].Status, StatusFailed)
	assert.Assert(t, strings.Contains(checks[2].Feedback, "timed out after 1s"))
	assert.Assert(t, checks[2].Duration < 5*time.Second)
}
