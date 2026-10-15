package localprune

import (
	"bytes"
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// setXDG points chunk's data and state directories, and the home directory
// chunk falls back to for its oldest leftovers, at fresh temp directories.
func setXDG(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv(config.EnvXDGStateHome, t.TempDir())
	t.Setenv("HOME", t.TempDir())
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitrepo.GitEnv(dir)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, "git %v: %s", args, out)
}

// initRepo creates a git repository on branch main with one commit, standing
// in for a developer's project root.
func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "commit", "-q", "--allow-empty", "-m", "init")
	return root
}

// registerProject writes root's project data directory and its breadcrumb,
// as chunk does on every save of project state, and returns the directory.
func registerProject(t *testing.T, root string) string {
	t.Helper()
	dir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)
	assert.NilError(t, sidecar.RegisterProjectRoot(dir, root))
	return dir
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	assert.NilError(t, os.WriteFile(path, data, 0o600))
}

func writeRecord(t *testing.T, path string, rec factory.Record) {
	t.Helper()
	data, err := json.Marshal(rec)
	assert.NilError(t, err)
	writeFile(t, path, data)
}

// paths returns the Path field of every item in the plan, for membership checks.
func paths(plan *Plan) []string {
	out := make([]string, len(plan.Items))
	for i, it := range plan.Items {
		out[i] = it.Path
	}
	return out
}

func TestScanLegacyPoolState(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	dir := registerProject(t, root)

	migrated := filepath.Join(root, ".chunk", "migrated-pool.json")
	writeFile(t, migrated, []byte(`{}`))
	writeFile(t, filepath.Join(dir, "pools", "migrated.json"), []byte(`{}`))

	stillOnly := filepath.Join(root, ".chunk", "onlylegacy-pool.json")
	writeFile(t, stillOnly, []byte(`{}`))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, contains(paths(plan), migrated), "migrated pool state should be pruned")
	assert.Assert(t, !contains(paths(plan), stillOnly), "pool state with no migrated copy must survive")
}

func TestScanLegacyReviewRun(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	dir := registerProject(t, root)

	legacy := filepath.Join(root, ".chunk", "review-run.json")
	writeFile(t, legacy, []byte(`{}`))
	current := filepath.Join(dir, "review-run.json")
	writeFile(t, current, []byte(`{}`))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, contains(paths(plan), legacy))
	assert.Assert(t, !contains(paths(plan), current))
}

func TestScanLegacyReviewRunWithoutMigratedCopySurvives(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	registerProject(t, root)

	legacy := filepath.Join(root, ".chunk", "review-run.json")
	writeFile(t, legacy, []byte(`{}`))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, !contains(paths(plan), legacy))
}

func TestScanLegacyHooksDisabled(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	dir := registerProject(t, root)

	legacy := filepath.Join(root, ".chunk", "hooks-disabled")
	writeFile(t, legacy, nil)
	current := filepath.Join(dir, "hooks-disabled")
	writeFile(t, current, nil)

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, contains(paths(plan), legacy))
	assert.Assert(t, !contains(paths(plan), current))
}

func TestScanLegacyHooksDisabledWithoutMigratedCopySurvives(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	registerProject(t, root)

	legacy := filepath.Join(root, ".chunk", "hooks-disabled")
	writeFile(t, legacy, nil)

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, !contains(paths(plan), legacy))
}

func TestScanValidateCacheAlwaysDropped(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	dir := registerProject(t, root)

	cache := filepath.Join(dir, "validate-cache")
	writeFile(t, filepath.Join(cache, "entry.json"), []byte(`{}`))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, contains(paths(plan), cache))
}

func TestScanLegacySidecarFiles(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	dir := registerProject(t, root)

	bareJSON := filepath.Join(dir, "sidecar.json")
	bareDiff := filepath.Join(dir, "sidecar.diff")
	writeFile(t, bareJSON, []byte(`{}`))
	writeFile(t, bareDiff, []byte(`diff`))

	orphanDiff := filepath.Join(dir, "sidecar.abc.diff")
	writeFile(t, orphanDiff, []byte(`diff`))

	pairedDiff := filepath.Join(dir, "sidecar.def.diff")
	pairedJSON := filepath.Join(dir, "sidecar.def.json")
	writeFile(t, pairedDiff, []byte(`diff`))
	writeFile(t, pairedJSON, []byte(`{}`))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	got := paths(plan)
	assert.Assert(t, contains(got, bareDiff))
	assert.Assert(t, contains(got, bareJSON))
	assert.Assert(t, contains(got, orphanDiff))
	assert.Assert(t, !contains(got, pairedDiff))
	assert.Assert(t, !contains(got, pairedJSON))
}

func TestScanLegacySidecarJSONAloneSurvives(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	dir := registerProject(t, root)

	bareJSON := filepath.Join(dir, "sidecar.json")
	writeFile(t, bareJSON, []byte(`{}`))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, !contains(paths(plan), bareJSON))
}

func TestScanLegacyFactoryDiscardedBranch(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	dir := registerProject(t, root)

	branch := "chunk/factory/discarded/1"
	worktree := filepath.Join(dir, "factory", "1")
	runGit(t, root, "worktree", "add", "-q", "-b", branch, worktree, "HEAD")
	// Detach the worktree from branch so the branch can be deleted while the
	// worktree, abandoned but not cleaned up, is still sitting on disk.
	runGit(t, worktree, "checkout", "-q", "--detach")
	runGit(t, root, "branch", "-D", branch)

	recordPath := filepath.Join(dir, "factory", "1.json")
	writeRecord(t, recordPath, factory.Record{
		RunID: "1", Branch: branch, Worktree: worktree,
	})
	poolPath := factory.PoolStatePath(dir, "1")
	writeFile(t, poolPath, []byte(`{}`))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	got := paths(plan)
	assert.Assert(t, contains(got, recordPath))
	assert.Assert(t, contains(got, poolPath))
	assert.Assert(t, contains(got, worktree))

	assert.NilError(t, plan.Apply(context.Background()))
	_, statErr := os.Stat(worktree)
	assert.Assert(t, os.IsNotExist(statErr))
	assertWorktreeDeregistered(t, root, worktree)
}

func TestScanLegacyFactoryMergedBranch(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	dir := registerProject(t, root)

	branch := "chunk/factory/pulled/2"
	worktree := filepath.Join(dir, "factory", "2")
	runGit(t, root, "worktree", "add", "-q", "-b", branch, worktree, "HEAD")
	runGit(t, worktree, "commit", "-q", "--allow-empty", "-m", "work")
	runGit(t, root, "merge", "-q", "--no-ff", "-m", "merge", branch)

	recordPath := filepath.Join(dir, "factory", "2.json")
	writeRecord(t, recordPath, factory.Record{RunID: "2", Branch: branch, Worktree: worktree})

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	got := paths(plan)
	assert.Assert(t, contains(got, recordPath))
	assert.Assert(t, contains(got, worktree))

	assert.NilError(t, plan.Apply(context.Background()))
	_, statErr := os.Stat(worktree)
	assert.Assert(t, os.IsNotExist(statErr))
	assertWorktreeDeregistered(t, root, worktree)

	// The branch itself is the developer's to keep or delete; removing the
	// worktree must not touch it, and must leave it droppable without the
	// "used by worktree" error a plain rm -rf would cause.
	runGit(t, root, "branch", "-d", branch)
}

func TestScanLegacyFactoryUnmergedBranchSurvives(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	dir := registerProject(t, root)

	branch := "chunk/factory/unpulled/3"
	worktree := filepath.Join(dir, "factory", "3")
	runGit(t, root, "worktree", "add", "-q", "-b", branch, worktree, "HEAD")
	runGit(t, worktree, "commit", "-q", "--allow-empty", "-m", "work in progress")

	recordPath := filepath.Join(dir, "factory", "3.json")
	writeRecord(t, recordPath, factory.Record{
		RunID: "3", Branch: branch, Worktree: worktree,
	})

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	got := paths(plan)
	assert.Assert(t, !contains(got, recordPath))
	assert.Assert(t, !contains(got, worktree))
}

// TestScanLegacyFactoryInvalidGitRepoKeepsRecord covers a project root that
// exists but is not a usable git repository: a rev-parse against its branch
// fails for a reason that says nothing about the branch itself, and that must
// not be read as the branch being deleted or merged.
func TestScanLegacyFactoryInvalidGitRepoKeepsRecord(t *testing.T) {
	setXDG(t)
	root := t.TempDir() // never git-initialised
	dir := registerProject(t, root)

	worktree := filepath.Join(dir, "factory", "4")
	assert.NilError(t, os.MkdirAll(worktree, 0o700))
	recordPath := filepath.Join(dir, "factory", "4.json")
	writeRecord(t, recordPath, factory.Record{
		RunID: "4", Branch: "chunk/factory/unknown/4", Worktree: worktree,
	})

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	got := paths(plan)
	assert.Assert(t, !contains(got, recordPath), "a record must survive when its run's state cannot be verified")
	assert.Assert(t, !contains(got, worktree))
}

// assertWorktreeDeregistered fails the test if git still lists worktree as a
// worktree of root: Apply must remove the registration, not just the files.
func assertWorktreeDeregistered(t *testing.T, root, worktree string) {
	t.Helper()
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = root
	cmd.Env = gitrepo.GitEnv(root)
	out, err := cmd.Output()
	assert.NilError(t, err)
	assert.Assert(t, !bytes.Contains(out, []byte(worktree)), "worktree %s still registered:\n%s", worktree, out)
}

func TestScanOrphanProjectWithoutBreadcrumbIsRemoved(t *testing.T) {
	setXDG(t)
	base, err := config.AppData()
	assert.NilError(t, err)
	orphan := filepath.Join(base, "deadbeef")
	assert.NilError(t, os.MkdirAll(orphan, 0o700))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, contains(paths(plan), orphan))
}

func TestScanOrphanProjectWithMissingRootIsRemoved(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	dir := registerProject(t, root)
	assert.NilError(t, os.RemoveAll(root))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, contains(paths(plan), dir))
}

func TestScanOrphanProjectWithFactoryWorkSurvives(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	dir := registerProject(t, root)
	writeFile(t, filepath.Join(dir, "factory", "1.json"), []byte(`{}`))
	assert.NilError(t, os.RemoveAll(root))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, !contains(paths(plan), dir))
}

func TestScanHomeLeftovers(t *testing.T) {
	setXDG(t)
	home, err := os.UserHomeDir()
	assert.NilError(t, err)

	factoryDir := filepath.Join(home, ".chunk", "factory")
	writeFile(t, filepath.Join(factoryDir, "1.json"), []byte(`{}`))

	daemonDir := filepath.Join(home, ".chunk", "daemon")
	assert.NilError(t, os.MkdirAll(daemonDir, 0o700))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	got := paths(plan)
	assert.Assert(t, contains(got, factoryDir))
	assert.Assert(t, contains(got, daemonDir))
}

func TestScanHomeDaemonListeningSurvives(t *testing.T) {
	setXDG(t)
	home, err := os.UserHomeDir()
	assert.NilError(t, err)

	daemonDir := filepath.Join(home, ".chunk", "daemon")
	assert.NilError(t, os.MkdirAll(daemonDir, 0o700))
	sock := filepath.Join(daemonDir, "daemon.sock")
	ln, err := net.Listen("unix", sock)
	assert.NilError(t, err)
	defer func() { _ = ln.Close() }()

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, !contains(paths(plan), daemonDir))
}

func TestPlanApplyRemovesEverything(t *testing.T) {
	setXDG(t)
	root := initRepo(t)
	dir := registerProject(t, root)

	cache := filepath.Join(dir, "validate-cache")
	writeFile(t, filepath.Join(cache, "entry.json"), []byte(`{}`))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, contains(paths(plan), cache))

	assert.NilError(t, plan.Apply(context.Background()))
	_, err = os.Stat(cache)
	assert.Assert(t, os.IsNotExist(err))

	rescan, err := Scan(context.Background())
	assert.NilError(t, err)
	assert.Equal(t, len(rescan.Items), 0)
}

// TestScanSkipsRepoDataDir covers the repo-* data directories that hold the
// current, live locations for factory records and the validate cache: they
// have no project-root breadcrumb (only per-checkout directories get one), so
// without an explicit skip they would be scanned as orphaned project data and
// queued for wholesale removal.
func TestScanSkipsRepoDataDir(t *testing.T) {
	setXDG(t)
	base, err := config.AppData()
	assert.NilError(t, err)
	repoDir := filepath.Join(base, "repo-deadbeef")
	cache := filepath.Join(repoDir, "validate-cache", "entry.json")
	writeFile(t, cache, []byte(`{}`))

	plan, err := Scan(context.Background())
	assert.NilError(t, err)
	got := paths(plan)
	assert.Assert(t, !contains(got, repoDir))
	assert.Assert(t, !contains(got, cache))
	assert.Assert(t, !contains(got, filepath.Join(repoDir, "validate-cache")))
}

func contains(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}
