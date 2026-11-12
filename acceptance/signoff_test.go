package acceptance

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

const signoffKeyPattern = `^github\.com/test-org/test-repo@tree:[0-9a-f]{40}$`

// signoffLocalEnv is the variable that runs the commands on this machine.
const signoffLocalEnv = "CHUNK_SIGNOFF_LOCAL"

// signoffProject is a repository, a fake CircleCI recording the check runs
// posted to it, and the environment to run the binary against both.
type signoffProject struct {
	env     *testenv.TestEnv
	cci     *fakes.FakeCircleCI
	workDir string
}

func newSignoffProject(t *testing.T) *signoffProject {
	t.Helper()
	return newSignoffProjectOn(t, true)
}

// newLocalSignoffProject is a project run with CHUNK_SIGNOFF_LOCAL=1, which has
// no sidecar to reach: no SSH server stands behind it.
func newLocalSignoffProject(t *testing.T) *signoffProject {
	t.Helper()
	p := newSignoffProjectOn(t, false)
	p.env.Extra[signoffLocalEnv] = "1"
	return p
}

func newSignoffProjectOn(t *testing.T, withSidecar bool) *signoffProject {
	t.Helper()
	env := testenv.NewTestEnv(t)
	env.Extra["CIRCLECI_ORG_ID"] = "org-aaa"

	cci := fakes.NewFakeCircleCI()
	var sshSrv *fakes.SSHServer
	if withSidecar {
		sshDir := filepath.Join(env.HomeDir, ".ssh")
		err := os.MkdirAll(sshDir, 0o700)
		assert.NilError(t, err)
		pubKey := fakes.GenerateSSHKeypairAt(t, filepath.Join(sshDir, "chunk_ai"))
		sshSrv = fakes.NewSSHServer(t, pubKey)
		useLocalSidecar(t, env, sshSrv)
		cci.AddKeyURL = sshSrv.Addr()
	}
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	env.CircleCIURL = srv.URL

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	chunkDir := filepath.Join(workDir, ".chunk")
	err := os.MkdirAll(chunkDir, 0o755)
	assert.NilError(t, err)
	// The org is configured, so the first run does not save it into config.json
	// and change the very tree it signs off.
	cfg := `{"orgID":"org-aaa","commands":[{"name":"lint","run":"test -f tracked.txt","remote":true},{"name":"format","run":"true","role":"autofix"}]}`
	err = os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(cfg), 0o644)
	assert.NilError(t, err)
	err = os.WriteFile(filepath.Join(workDir, "tracked.txt"), []byte("one\n"), 0o644)
	assert.NilError(t, err)
	signoffGit(t, workDir, nil, "add", "-A")
	signoffGit(t, workDir, nil, "commit", "-m", "add tracked.txt")

	return &signoffProject{env: env, cci: cci, workDir: workDir}
}

// signoff runs `chunk signoff` in the project.
func (p *signoffProject) signoff(t *testing.T) binary.CLIResult {
	t.Helper()
	return binary.RunCLI(t, []string{"signoff"}, p.env, p.workDir)
}

// signoffGit runs git in dir with extra environment and returns its stdout.
func signoffGit(t *testing.T, dir string, extraEnv []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitrepo.GitEnv(dir), extraEnv...)
	out, err := cmd.Output()
	assert.NilError(t, err, "git %v", args)
	return string(out)
}

// treeKey returns the tree SHA in a posted key.
func treeKey(t *testing.T, key string) string {
	t.Helper()
	_, tree, ok := strings.Cut(key, "@tree:")
	assert.Assert(t, ok, "key %q has no @tree:", key)
	return tree
}

func TestSignoff(t *testing.T) {
	t.Parallel()

	t.Run("posts one run per validation command", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)

		result := p.signoff(t)

		assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		runs := p.cci.CheckRuns()
		assert.Assert(t, cmp.Len(runs, 1)) // gate: runs[0] below
		assert.Check(t, cmp.Equal(runs[0].Name, "lint"))
		assert.Check(t, cmp.Equal(runs[0].Outcome, "succeeded"))
		assert.Check(t, cmp.Regexp(signoffKeyPattern, runs[0].Key))
		startedFirst := runs[0].StartedAt.Before(runs[0].EndedAt)
		assert.Check(t, startedFirst, "started_at %s, ended_at %s", runs[0].StartedAt, runs[0].EndedAt)
		for _, r := range runs {
			assert.Check(t, r.Name != "format", "autofix command was posted")
		}
	})

	t.Run("keys on the working tree's content", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		err := os.WriteFile(filepath.Join(p.workDir, "tracked.txt"), []byte("edited\n"), 0o644)
		assert.NilError(t, err)
		err = os.WriteFile(filepath.Join(p.workDir, "untracked.txt"), []byte("new\n"), 0o644)
		assert.NilError(t, err)

		result := p.signoff(t)

		assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		runs := p.cci.CheckRuns()
		assert.Assert(t, cmp.Len(runs, 1)) // gate: runs[0] below
		assert.Check(t, cmp.Regexp(signoffKeyPattern, runs[0].Key))

		// The same state, staged into a throwaway index.
		index := filepath.Join(t.TempDir(), "index")
		scratch := []string{"GIT_INDEX_FILE=" + index}
		signoffGit(t, p.workDir, scratch, "add", "-A")
		want := strings.TrimSpace(signoffGit(t, p.workDir, scratch, "write-tree"))
		got := treeKey(t, runs[0].Key)
		assert.Check(t, cmp.Equal(got, want))
	})

	t.Run("an uncommitted edit is signed off under its own tree", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)

		first := p.signoff(t)
		assert.Equal(t, first.ExitCode, 0, "stdout: %s\nstderr: %s", first.Stdout, first.Stderr)

		err := os.WriteFile(filepath.Join(p.workDir, "tracked.txt"), []byte("edited\n"), 0o644)
		assert.NilError(t, err)
		edited := p.signoff(t)
		assert.Equal(t, edited.ExitCode, 0, "stdout: %s\nstderr: %s", edited.Stdout, edited.Stderr)

		signoffGit(t, p.workDir, nil, "commit", "-am", "edit tracked.txt")
		committed := p.signoff(t)
		assert.Equal(t, committed.ExitCode, 0, "stdout: %s\nstderr: %s", committed.Stdout, committed.Stderr)

		runs := p.cci.CheckRuns()
		assert.Assert(t, cmp.Len(runs, 3)) // gate: runs[0..2] below
		assert.Check(t, runs[1].Key != runs[0].Key, "an edit kept the key %s", runs[0].Key)
		assert.Check(t, cmp.Equal(runs[2].Key, runs[1].Key))
	})

	t.Run("runs every time", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)

		for range 2 {
			result := p.signoff(t)
			assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		}

		runs := p.cci.CheckRuns()
		assert.Assert(t, cmp.Len(runs, 2)) // gate: runs[0..1] below
		assert.Check(t, cmp.Equal(runs[1].Key, runs[0].Key))
		assert.Check(t, cmp.Equal(runs[1].Name, runs[0].Name))
	})

	t.Run("reuses one sidecar named for the project", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)

		for range 2 {
			result := p.signoff(t)
			assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		}

		// The checkout is a temp directory, so a name taken from it would differ
		// run to run; the project's own name keeps the pool state, and with it
		// the sidecar, the same across runs.
		sidecars := p.cci.Sidecars
		assert.Assert(t, cmp.Len(sidecars, 1)) // gate: sidecars[0] below
		project := filepath.Base(p.workDir)
		fromProject := strings.HasPrefix(sidecars[0].Name, project)
		assert.Check(t, fromProject, "sidecar name %q does not come from project %q", sidecars[0].Name, project)
		fromCheckout := strings.Contains(sidecars[0].Name, "chunk-shadow")
		assert.Check(t, !fromCheckout, "sidecar name %q comes from the checkout", sidecars[0].Name)
	})

	t.Run("leaves the working tree alone", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		err := os.WriteFile(filepath.Join(p.workDir, "tracked.txt"), []byte("staged\n"), 0o644)
		assert.NilError(t, err)
		signoffGit(t, p.workDir, nil, "add", "tracked.txt")
		err = os.WriteFile(filepath.Join(p.workDir, "tracked.txt"), []byte("staged then edited\n"), 0o644)
		assert.NilError(t, err)
		err = os.WriteFile(filepath.Join(p.workDir, "untracked.txt"), []byte("new\n"), 0o644)
		assert.NilError(t, err)

		status := signoffGit(t, p.workDir, nil, "status", "--porcelain")
		stash := signoffGit(t, p.workDir, nil, "stash", "list")
		index := signoffGit(t, p.workDir, nil, "ls-files", "--stage")

		result := p.signoff(t)

		assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		statusAfter := signoffGit(t, p.workDir, nil, "status", "--porcelain")
		stashAfter := signoffGit(t, p.workDir, nil, "stash", "list")
		indexAfter := signoffGit(t, p.workDir, nil, "ls-files", "--stage")
		assert.Check(t, cmp.Equal(statusAfter, status))
		assert.Check(t, cmp.Equal(stashAfter, stash))
		assert.Check(t, cmp.Equal(indexAfter, index))
		var paths []string
		for _, line := range strings.Split(signoffGit(t, p.workDir, nil, "worktree", "list", "--porcelain"), "\n") {
			if path, ok := strings.CutPrefix(line, "worktree "); ok {
				paths = append(paths, path)
			}
		}
		assert.Check(t, cmp.Len(paths, 1), "worktrees: %v", paths)
	})

	t.Run("local mode keys like the sidecar", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		p.env.Extra[signoffLocalEnv] = "1"
		local := p.signoff(t)
		assert.Equal(t, local.ExitCode, 0, "stdout: %s\nstderr: %s", local.Stdout, local.Stderr)

		delete(p.env.Extra, signoffLocalEnv)
		remote := p.signoff(t)
		assert.Equal(t, remote.ExitCode, 0, "stdout: %s\nstderr: %s", remote.Stdout, remote.Stderr)

		runs := p.cci.CheckRuns()
		assert.Assert(t, cmp.Len(runs, 2)) // gate: runs[0..1] below
		assert.Check(t, cmp.Regexp(signoffKeyPattern, runs[0].Key))
		assert.Check(t, cmp.Equal(runs[1].Key, runs[0].Key))
	})
}
