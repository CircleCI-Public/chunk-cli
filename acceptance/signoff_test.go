package acceptance

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

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
	ssh     *fakes.SSHServer
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
	// The commands really run, in the synced checkout, so their exit codes and
	// what they see are the sidecar's.
	cci.ExecRunsLocally = true
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

	return &signoffProject{env: env, cci: cci, ssh: sshSrv, workDir: workDir}
}

// signoff runs `chunk signoff` in the project.
func (p *signoffProject) signoff(t *testing.T) binary.CLIResult {
	t.Helper()
	return binary.RunCLI(t, []string{"signoff"}, p.env, p.workDir)
}

// setCommands replaces the project's config with one that names these commands
// (a JSON array) and the same org, so the tree changes by nothing else.
func (p *signoffProject) setCommands(t *testing.T, commands string) {
	t.Helper()
	cfg := `{"orgID":"org-aaa","commands":` + commands + `}`
	err := os.WriteFile(filepath.Join(p.workDir, ".chunk", "config.json"), []byte(cfg), 0o644)
	assert.NilError(t, err)
}

// ran reports whether the sidecar was asked to run a script containing script.
func (p *signoffProject) ran(script string) bool {
	for _, c := range p.cci.ExecScripts() {
		if strings.Contains(c, script) {
			return true
		}
	}
	return false
}

// assertSidecarUntouched checks that nothing was synced to the sidecar or run
// on it.
func (p *signoffProject) assertSidecarUntouched(t *testing.T) {
	t.Helper()
	sshCommands := p.ssh.Commands()
	assert.Check(t, cmp.Len(sshCommands, 0), "synced to the sidecar")
	execScripts := p.cci.ExecScripts()
	assert.Check(t, cmp.Len(execScripts, 0), "ran on the sidecar")
}

// worktrees lists the paths git knows as worktrees of dir.
func worktrees(t *testing.T, dir string) []string {
	t.Helper()
	var paths []string
	for _, line := range strings.Split(signoffGit(t, dir, nil, "worktree", "list", "--porcelain"), "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			paths = append(paths, path)
		}
	}
	return paths
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
		paths := worktrees(t, p.workDir)
		assert.Check(t, cmp.Len(paths, 1), "worktrees: %v", paths)
	})

	t.Run("a failing command does not stop the rest", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		p.setCommands(t, `[{"name":"a","run":"false","remote":true},{"name":"b","run":"true","remote":true}]`)

		result := p.signoff(t)

		assert.Check(t, result.ExitCode != 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		assert.Check(t, cmp.Contains(result.Stdout, "not signed off"))
		runs := p.cci.CheckRuns()
		assert.Assert(t, cmp.Len(runs, 2)) // gate: runs[0..1] below
		assert.Check(t, cmp.Equal(runs[0].Name, "a"))
		assert.Check(t, cmp.Equal(runs[0].Outcome, "failed"))
		assert.Check(t, cmp.Equal(runs[1].Name, "b"))
		assert.Check(t, cmp.Equal(runs[1].Outcome, "succeeded"))
	})

	t.Run("a command past its timeout is timed_out", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		p.setCommands(t, `[{"name":"slow","run":"sleep 5","remote":true,"timeout":1}]`)

		result := p.signoff(t)

		assert.Check(t, result.ExitCode != 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		assert.Check(t, cmp.Contains(result.Stdout, "timed_out"))
		runs := p.cci.CheckRuns()
		assert.Assert(t, cmp.Len(runs, 1)) // gate: runs[0] below
		assert.Check(t, cmp.Equal(runs[0].Outcome, "timed_out"))
	})

	t.Run("no validation commands", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		p.setCommands(t, `[{"name":"format","run":"true","role":"autofix"}]`)

		onlyAutofix := p.signoff(t)

		assert.Check(t, onlyAutofix.ExitCode != 0, "stdout: %s\nstderr: %s", onlyAutofix.Stdout, onlyAutofix.Stderr)
		assert.Check(t, cmp.Contains(onlyAutofix.Stderr, "no commands to sign off"))

		err := os.Remove(filepath.Join(p.workDir, ".chunk", "config.json"))
		assert.NilError(t, err)
		noConfig := p.signoff(t)

		assert.Check(t, noConfig.ExitCode != 0, "stdout: %s\nstderr: %s", noConfig.Stdout, noConfig.Stderr)
		assert.Check(t, cmp.Contains(noConfig.Stderr, "no commands to sign off"))
		checkRuns := p.cci.CheckRuns()
		assert.Check(t, cmp.Len(checkRuns, 0))
		p.assertSidecarUntouched(t)
	})

	t.Run("no CircleCI token", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		p.env.CircleToken = ""

		result := p.signoff(t)

		assert.Check(t, result.ExitCode != 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		assert.Check(t, cmp.Contains(result.Stderr, "CircleCI token required"))
		checkRuns := p.cci.CheckRuns()
		assert.Check(t, cmp.Len(checkRuns, 0))
		p.assertSidecarUntouched(t)
	})

	t.Run("no origin", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		signoffGit(t, p.workDir, nil, "remote", "remove", "origin")

		result := p.signoff(t)

		assert.Check(t, result.ExitCode != 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		assert.Check(t, cmp.Contains(result.Stderr, "origin remote"))
		checkRuns := p.cci.CheckRuns()
		assert.Check(t, cmp.Len(checkRuns, 0))
		p.assertSidecarUntouched(t)
	})

	t.Run("a hostless origin", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		signoffGit(t, p.workDir, nil, "remote", "set-url", "origin", "/srv/git/x.git")

		result := p.signoff(t)

		assert.Check(t, result.ExitCode != 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		assert.Check(t, cmp.Contains(result.Stderr, "origin remote has no host"))
		assert.Check(t, cmp.Contains(result.Stderr, "/srv/git/x.git"))
		checkRuns := p.cci.CheckRuns()
		assert.Check(t, cmp.Len(checkRuns, 0))
		p.assertSidecarUntouched(t)
	})

	t.Run("not a git repo", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)

		result := binary.RunCLI(t, []string{"signoff"}, p.env, t.TempDir())

		assert.Check(t, result.ExitCode != 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		assert.Check(t, cmp.Contains(result.Stderr, "Not in a git repository"))
		checkRuns := p.cci.CheckRuns()
		assert.Check(t, cmp.Len(checkRuns, 0))
		p.assertSidecarUntouched(t)
	})

	t.Run("a failed post fails signoff", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			status int
			want   string
		}{
			{
				status: http.StatusNotFound,
				want:   "API error",
			},
			{
				status: http.StatusInternalServerError,
				want:   "API error",
			},
			{
				status: http.StatusUnauthorized,
				want:   "Not authorized",
			},
		} {
			t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
				t.Parallel()
				p := newSignoffProject(t)
				p.setCommands(t, `[{"name":"a","run":"echo ran-a","remote":true},{"name":"b","run":"echo ran-b","remote":true}]`)
				p.cci.CheckRunStatus = tc.status

				result := p.signoff(t)

				assert.Check(t, result.ExitCode != 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
				assert.Check(t, cmp.Contains(result.Stderr, tc.want))
				ranA := p.ran("echo ran-a")
				assert.Check(t, ranA, "a did not run")
				ranB := p.ran("echo ran-b")
				assert.Check(t, ranB, "b did not run after a's post failed")
				signedOff := strings.Contains(result.Stdout, "signed off 2")
				assert.Check(t, !signedOff, "stdout: %s", result.Stdout)
				assert.Check(t, cmp.Contains(result.Stdout, "not signed off"))
				checkRuns := p.cci.CheckRuns()
				assert.Check(t, cmp.Len(checkRuns, 0))
			})
		}
	})

	t.Run("gitignored files stay out", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		p.setCommands(t, `[{"name":"clean","run":"test ! -e secret.env","remote":true}]`)
		err := os.WriteFile(filepath.Join(p.workDir, ".gitignore"), []byte("secret.env\n"), 0o644)
		assert.NilError(t, err)
		err = os.WriteFile(filepath.Join(p.workDir, "secret.env"), []byte("TOKEN=1\n"), 0o644)
		assert.NilError(t, err)

		result := p.signoff(t)

		assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		runs := p.cci.CheckRuns()
		assert.Assert(t, cmp.Len(runs, 1)) // gate: runs[0] below
		assert.Check(t, cmp.Equal(runs[0].Outcome, "succeeded"))
	})

	t.Run("a dirty submodule warns", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		sub := gitrepo.SetupGitRepo(t, "test-org", "sub-repo")
		err := os.WriteFile(filepath.Join(sub, "lib.txt"), []byte("lib\n"), 0o644)
		assert.NilError(t, err)
		signoffGit(t, sub, nil, "add", "-A")
		signoffGit(t, sub, nil, "commit", "-m", "add lib.txt")
		signoffGit(t, p.workDir, nil, "-c", "protocol.file.allow=always", "submodule", "add", sub, "vendor/sub")
		signoffGit(t, p.workDir, nil, "commit", "-am", "add submodule")
		err = os.WriteFile(filepath.Join(p.workDir, "vendor", "sub", "lib.txt"), []byte("edited\n"), 0o644)
		assert.NilError(t, err)

		result := p.signoff(t)

		assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		assert.Check(t, cmp.Contains(result.Stderr, "vendor/sub"))
		checkRuns := p.cci.CheckRuns()
		assert.Check(t, cmp.Len(checkRuns, 1))
	})

	t.Run("a clean submodule does not warn", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		sub := gitrepo.SetupGitRepo(t, "test-org", "sub-repo")
		signoffGit(t, p.workDir, nil, "-c", "protocol.file.allow=always", "submodule", "add", sub, "vendor/sub")
		signoffGit(t, p.workDir, nil, "commit", "-am", "add submodule")

		result := p.signoff(t)

		assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		warned := strings.Contains(result.Stderr, "vendor/sub")
		assert.Check(t, !warned, "stderr: %s", result.Stderr)
	})

	t.Run("the checkout is removed after a failure", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		p.setCommands(t, `[{"name":"a","run":"false","remote":true}]`)

		result := p.signoff(t)

		assert.Check(t, result.ExitCode != 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		paths := worktrees(t, p.workDir)
		assert.Check(t, cmp.Len(paths, 1), "worktrees: %v", paths)
	})

	t.Run("a failed snapshot posts nothing", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("root reads files whatever their mode")
		}
		p := newSignoffProject(t)
		unreadable := filepath.Join(p.workDir, "unreadable.txt")
		err := os.WriteFile(unreadable, []byte("x\n"), 0o644)
		assert.NilError(t, err)
		err = os.Chmod(unreadable, 0o000)
		assert.NilError(t, err)

		result := p.signoff(t)

		assert.Check(t, result.ExitCode != 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		assert.Check(t, cmp.Contains(result.Stderr, "unreadable.txt"))
		checkRuns := p.cci.CheckRuns()
		assert.Check(t, cmp.Len(checkRuns, 0))
		p.assertSidecarUntouched(t)
	})

	t.Run("a stale worktree entry is pruned", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		// What a SIGKILLed run leaves behind: a registered worktree whose
		// directory is gone.
		stale := filepath.Join(t.TempDir(), "stale")
		signoffGit(t, p.workDir, nil, "worktree", "add", "--detach", stale)
		err := os.RemoveAll(stale)
		assert.NilError(t, err)
		before := worktrees(t, p.workDir)
		assert.Assert(t, cmp.Len(before, 2))

		result := p.signoff(t)

		assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		paths := worktrees(t, p.workDir)
		assert.Check(t, cmp.Len(paths, 1), "worktrees: %v", paths)
	})

	t.Run("Ctrl-C removes the checkout", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		p.setCommands(t, `[{"name":"wait","run":"sleep 30","remote":true},{"name":"after","run":"echo ran-after","remote":true}]`)

		cmd := exec.Command(binary.Path(), "--insecure-storage", "signoff")
		cmd.Dir = p.workDir
		cmd.Env = p.env.Environ()
		var stdout, stderr strings.Builder
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		err := cmd.Start()
		assert.NilError(t, err)
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		t.Cleanup(func() { _ = cmd.Process.Kill() })

		deadline := time.Now().Add(20 * time.Second)
		for !p.ran("sleep 30") {
			if time.Now().After(deadline) {
				t.Fatalf("the command never started\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
			}
			time.Sleep(50 * time.Millisecond)
		}
		err = cmd.Process.Signal(syscall.SIGINT)
		assert.NilError(t, err)

		select {
		case err := <-done:
			assert.Check(t, err != nil, "signoff exited 0 after Ctrl-C")
		case <-time.After(20 * time.Second):
			t.Fatalf("signoff did not exit after Ctrl-C\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
		}
		checkRuns := p.cci.CheckRuns()
		assert.Check(t, cmp.Len(checkRuns, 0))
		ranA := p.ran("echo ran-after")
		assert.Check(t, !ranA, "a command ran after Ctrl-C")
		paths := worktrees(t, p.workDir)
		assert.Check(t, cmp.Len(paths, 1), "worktrees: %v", paths)
	})

	t.Run("a repo with no commits", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		err := os.RemoveAll(filepath.Join(p.workDir, ".git"))
		assert.NilError(t, err)
		signoffGit(t, p.workDir, nil, "init")
		signoffGit(t, p.workDir, nil, "remote", "add", "origin", "git@github.com:test-org/test-repo.git")

		result := p.signoff(t)

		assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		runs := p.cci.CheckRuns()
		assert.Assert(t, cmp.Len(runs, 1)) // gate: runs[0] below
		assert.Check(t, cmp.Regexp(signoffKeyPattern, runs[0].Key))
		index := filepath.Join(t.TempDir(), "index")
		scratch := []string{"GIT_INDEX_FILE=" + index}
		signoffGit(t, p.workDir, scratch, "add", "-A")
		want := strings.TrimSpace(signoffGit(t, p.workDir, scratch, "write-tree"))
		got := treeKey(t, runs[0].Key)
		assert.Check(t, cmp.Equal(got, want))
	})

	t.Run("local mode runs in the checkout and posts each", func(t *testing.T) {
		t.Parallel()
		p := newLocalSignoffProject(t)
		p.setCommands(t, `[{"name":"lint","run":"test -f tracked.txt","remote":true},{"name":"fail","run":"false","remote":true}]`)

		result := p.signoff(t)

		assert.Check(t, result.ExitCode != 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		runs := p.cci.CheckRuns()
		assert.Assert(t, cmp.Len(runs, 2)) // gate: runs[0..1] below
		assert.Check(t, cmp.Regexp(signoffKeyPattern, runs[0].Key))
		assert.Check(t, cmp.Equal(runs[1].Key, runs[0].Key))
		assert.Check(t, cmp.Equal(runs[0].Name, "lint"))
		assert.Check(t, cmp.Equal(runs[0].Outcome, "succeeded"))
		assert.Check(t, cmp.Equal(runs[1].Name, "fail"))
		assert.Check(t, cmp.Equal(runs[1].Outcome, "failed"))
		sidecars := p.cci.Sidecars
		assert.Check(t, cmp.Len(sidecars, 0), "a sidecar was created")
		execScripts := p.cci.ExecScripts()
		assert.Check(t, cmp.Len(execScripts, 0), "ran on the sidecar")
		assert.Check(t, cmp.Contains(result.Stdout, "locally"))
	})

	t.Run("local mode leaves gitignored files out", func(t *testing.T) {
		t.Parallel()
		p := newLocalSignoffProject(t)
		p.setCommands(t, `[{"name":"clean","run":"test ! -e secret.env","remote":true}]`)
		err := os.WriteFile(filepath.Join(p.workDir, ".gitignore"), []byte("secret.env\n"), 0o644)
		assert.NilError(t, err)
		err = os.WriteFile(filepath.Join(p.workDir, "secret.env"), []byte("TOKEN=1\n"), 0o644)
		assert.NilError(t, err)

		result := p.signoff(t)

		assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		runs := p.cci.CheckRuns()
		assert.Assert(t, cmp.Len(runs, 1)) // gate: runs[0] below
		assert.Check(t, cmp.Equal(runs[0].Outcome, "succeeded"))
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

	t.Run("other values use the sidecar", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		p.env.Extra[signoffLocalEnv] = "true"

		result := p.signoff(t)

		assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		sidecars := p.cci.Sidecars
		assert.Check(t, cmp.Len(sidecars, 1), "no sidecar was created")
		ranOnSidecar := p.ran("test -f tracked.txt")
		assert.Check(t, ranOnSidecar, "the command did not run on the sidecar")
		ranLocally := strings.Contains(result.Stdout, "locally")
		assert.Check(t, !ranLocally, "stdout: %s", result.Stdout)
	})

	t.Run("no sidecar", func(t *testing.T) {
		t.Parallel()
		p := newSignoffProject(t)
		p.cci.CreateStatusCode = http.StatusInternalServerError

		result := p.signoff(t)

		assert.Check(t, result.ExitCode != 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		assert.Check(t, result.Stderr != "")
		checkRuns := p.cci.CheckRuns()
		assert.Check(t, cmp.Len(checkRuns, 0))
		p.assertSidecarUntouched(t)
	})
}
