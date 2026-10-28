package acceptance

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// factory is a public command group, so both it and build have to reach the
// user: listed by 'chunk commands', which skips hidden ones, and with help of
// their own.
func TestFactoryIsListedAndDocumented(t *testing.T) {
	env := testenv.NewTestEnv(t)

	list := binary.RunCLI(t, []string{"commands"}, env, env.HomeDir)
	assert.Equal(t, list.ExitCode, 0, "stderr: %s", list.Stderr)
	assert.Assert(t, strings.Contains(list.Stdout, "chunk factory build"), "stdout: %s", list.Stdout)

	help := binary.RunCLI(t, []string{"factory", "--help"}, env, env.HomeDir)
	assert.Equal(t, help.ExitCode, 0, "stderr: %s", help.Stderr)
	for _, want := range []string{"Build and manage factory runs", "build"} {
		assert.Assert(t, strings.Contains(help.Stdout, want), "missing %q in stdout: %s", want, help.Stdout)
	}

	buildHelp := binary.RunCLI(t, []string{"factory", "build", "--help"}, env, env.HomeDir)
	assert.Equal(t, buildHelp.ExitCode, 0, "stderr: %s", buildHelp.Stderr)
	for _, want := range []string{"chunk factory build [prompt|file|-]", "--max-attempts", "--reviews", "--no-validate"} {
		assert.Assert(t, strings.Contains(buildHelp.Stdout, want), "missing %q in stdout: %s", want, buildHelp.Stdout)
	}
}

func TestFactoryWithoutASubcommandShowsHelp(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"factory"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "Available Commands:"), "stdout: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "build"), "stdout: %s", result.Stdout)
}

func TestFactoryRequiresASubcommand(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"factory", "add a flag"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, `"add a flag" is not a factory command`), "stderr: %s", result.Stderr)
}

// Build flags on the group must fail before pflag can consume the prompt as an
// unknown flag's value and turn a legacy invocation into successful help output.
func TestFactoryRejectsBuildFlagsWithoutBuildSubcommand(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"factory", "--no-validate", "add a flag"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "unknown flag: --no-validate"), "stderr: %s", result.Stderr)
}

func TestFactoryBuildWithoutAPromptExitsBadArgs(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// With no argument the prompt is read from stdin, which is empty here.
	result := binary.RunCLIWithStdin(t, []string{"factory", "build"}, env, env.HomeDir, []byte{})

	assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "The prompt on stdin is empty."), "stderr: %s", result.Stderr)
}

// Everything below returns before the chunk daemon is started, so these are
// the paths a first run most often lands on, with nothing running.
func TestFactoryOutsideAGitRepoExitsBadArgs(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"factory", "build", "add a --verbose flag"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "is not inside a git repository."), "stderr: %s", result.Stderr)
}

func TestFactoryWithoutProjectConfigPointsAtInit(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	result := binary.RunCLI(t, []string{"factory", "build", "add a --verbose flag"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "No validate commands configured"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "chunk init"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "chunk factory bootstrap"), "stderr: %s", result.Stderr)
}

// A config that cannot be read is reported as such, not as a project with no
// config: bootstrap, which that would point at, refuses it too.
func TestFactoryWithAnUnreadableProjectConfig(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	assert.NilError(t, os.MkdirAll(filepath.Join(workDir, ".chunk"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, ".chunk", "config.json"), []byte("{"), 0o644))

	result := binary.RunCLI(t, []string{"factory", "build", "add a --verbose flag"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Could not read .chunk/config.json"), "stderr: %s", result.Stderr)
	assert.Assert(t, !strings.Contains(result.Stderr, "No validate commands configured"), "stderr: %s", result.Stderr)
}

// A project with no sidecar image is told its sidecars start from the default
// image, unless one is given for the run. --continue with a run that does not
// exist stops the command just after, before the daemon is started.
func TestFactoryHintsAtAMissingSidecarImage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		extra    map[string]interface{}
		args     []string
		wantHint bool
	}{
		{name: "no image", wantHint: true},
		{name: "image in config", extra: map[string]interface{}{"validation": map[string]any{"sidecarImage": "snap-1"}}},
		{name: "image flag", args: []string{"--image", "snap-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testenv.NewTestEnv(t)
			workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
			writeChunkConfig(t, workDir, tc.extra)

			args := append([]string{"factory", "build", "--continue", "nope"}, tc.args...)
			result := binary.RunCLI(t, append(args, "add a --verbose flag"), env, workDir)

			assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
			assert.Assert(t, strings.Contains(result.Stderr, "No factory run nope to continue"), "stderr: %s", result.Stderr)
			hint := strings.Contains(result.Stderr, "No validation.sidecarImage, so sidecars start from the default image")
			assert.Equal(t, hint, tc.wantHint, "stderr: %s", result.Stderr)
		})
	}
}

// Without reviews or validation commands the loop would pass whatever the
// implementer wrote, so the run is refused instead.
func TestFactoryWithNothingToCheck(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeChunkConfig(t, workDir, nil)

	result := binary.RunCLI(t, []string{"factory", "build", "--no-validate", "add a --verbose flag"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Nothing to check the implementer's work with."), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, ".chunk/reviews"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "Add one .md or .txt file per review to .chunk/reviews"), "stderr: %s", result.Stderr)
	assert.Assert(t, !strings.Contains(result.Stderr, "chunk factory bootstrap"), "stderr: %s", result.Stderr)
}

// chunk factory build --remote provisions a sidecar of its own for the run,
// installs chunk on it with --chunk-binary, and makes the baseline commit
// there, without starting the run itself.
func TestFactoryBuildRemoteProvisionsAHostSidecar(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: "deadbeef\n"}, "correctness")

	// The fake SSH server runs commands for real on this machine; $HOME there
	// is redirected to the sidecar's home so the chunk binary --chunk-binary
	// uploads lands in a temp dir rather than the real one.
	sidecarHome := env.Extra["CHUNK_SIDECAR_HOME"]
	t.Setenv("HOME", sidecarHome)

	binContent := "#!/bin/sh\necho fake chunk\n"
	binPath := filepath.Join(t.TempDir(), "chunk-linux")
	assert.NilError(t, os.WriteFile(binPath, []byte(binContent), 0o755))

	result := binary.RunCLI(t, []string{
		"factory", "build", "--remote", "--chunk-binary", binPath, "--json", "add a --verbose flag",
	}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)

	var report struct {
		SidecarID string `json:"sidecar_id"`
	}
	assert.NilError(t, json.Unmarshal([]byte(result.Stdout), &report), result.Stdout)
	assert.Assert(t, report.SidecarID != "", "stdout: %s", result.Stdout)

	// The pool is created: a single sidecar for the one host.
	creates := 0
	for _, r := range cci.Recorder.AllRequests() {
		if r.Method == "POST" && r.URL.Path == "/api/v3/sidecar/instances" {
			creates++
		}
	}
	assert.Equal(t, creates, 1, "requests: %v", cci.Recorder.AllRequests())

	// The binary is installed on the host for real: --chunk-binary uploads it
	// over the sidecar's SSH connection rather than installing a release.
	data, err := os.ReadFile(filepath.Join(sidecarHome, "chunk"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), binContent)
	info, err := os.Stat(filepath.Join(sidecarHome, "chunk"))
	assert.NilError(t, err)
	assert.Assert(t, info.Mode()&0o100 != 0, "the uploaded chunk should be executable")

	// The baseline commit is exec'd on the host.
	baselineExecs := 0
	for _, r := range cci.Recorder.AllRequests() {
		if strings.HasSuffix(r.URL.Path, "/exec") && strings.Contains(string(r.Body), "chunk factory baseline") {
			baselineExecs++
		}
	}
	assert.Equal(t, baselineExecs, 1, "requests: %v", cci.Recorder.AllRequests())
}

func TestFactoryBuildRemoteRefusesAnUnsupportedFlagCombination(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeChunkConfig(t, workDir, nil)
	writePrompts(t, filepath.Join(workDir, ".chunk", "reviews"), "correctness")

	result := binary.RunCLI(t, []string{"factory", "build", "--remote", "--verbose", "add a --verbose flag"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "--verbose is not supported with --remote yet."), "stderr: %s", result.Stderr)
}

// Once the host is provisioned, chunk factory build --remote starts chunk
// factory build on the host as one foreground exec and follows it: its
// stderr reaches this terminal as it streams, and the run's record is saved
// with Remote set before any of that output arrives. Ctrl-C only detaches -
// it sends no cancel and no delete - and the command still exits 0.
func TestFactoryBuildRemoteFollowsTheRunAndDetachesOnSIGINT(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, &fakes.ExecResponse{
		CommandID: "cmd-1", Stdout: "deadbeef\n", Stderr: "building on the host\n",
	}, "correctness")
	// The baseline commit is the first exec; hold the second one's output -
	// the run itself - until the client disconnects, so there is a genuine
	// window to interrupt it in, rather than a race against a command that
	// has already finished.
	cci.HoldCommandOutputFromExec = 2

	// The fake SSH server runs commands for real on this machine; $HOME there
	// is redirected to the sidecar's home so --chunk-binary's upload lands in
	// a temp dir rather than the real one.
	sidecarHome := env.Extra["CHUNK_SIDECAR_HOME"]
	t.Setenv("HOME", sidecarHome)
	binPath := filepath.Join(t.TempDir(), "chunk-linux")
	assert.NilError(t, os.WriteFile(binPath, []byte("#!/bin/sh\necho fake chunk\n"), 0o755))

	// --reviews is given as an absolute path on this machine; the host has
	// its own checkout, so it must be told the project-relative one.
	cmd := exec.Command(binary.Path(), "--insecure-storage", "factory", "build",
		"--remote", "--chunk-binary", binPath,
		"--reviews", filepath.Join(workDir, ".chunk", "reviews"), "add a --verbose flag")
	cmd.Dir = workDir
	cmd.Env = env.Environ()
	var stderr syncBuffer
	cmd.Stderr = &stderr
	cmd.Stdout = &syncBuffer{}
	assert.NilError(t, cmd.Start())

	waitForOutput(t, &stderr, "building on the host")

	assert.NilError(t, cmd.Process.Signal(syscall.SIGINT))
	waitErr := cmd.Wait()

	assert.NilError(t, waitErr, "stderr: %s", stderr.String())
	assert.Equal(t, cmd.ProcessState.ExitCode(), 0, "stderr: %s", stderr.String())
	assert.Assert(t, strings.Contains(stderr.String(), "continues on sidecar"), "stderr: %s", stderr.String())
	assert.Assert(t, strings.Contains(stderr.String(), "chunk factory pull"), "stderr: %s", stderr.String())

	var runScript string
	for _, r := range cci.Recorder.AllRequests() {
		assert.Assert(t, r.Method != "DELETE" || !strings.HasPrefix(r.URL.Path, "/api/v3/sidecar/instances/"),
			"a detached Ctrl-C must not delete the host sidecar: %v", r)
		if r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/exec") && strings.Contains(string(r.Body), "factory") {
			runScript = string(r.Body)
		}
	}
	assert.Assert(t, strings.Contains(runScript, `'--reviews' '.chunk/reviews'`), "run exec: %s", runScript)

	t.Setenv(config.EnvXDGDataHome, filepath.Join(env.HomeDir, ".local", "share"))
	root := config.CanonicalProjectRoot(workDir)
	dataDir, err := config.RepoDataDir(root)
	assert.NilError(t, err)
	matches, err := filepath.Glob(filepath.Join(dataDir, "factory", "r-*.json"))
	assert.NilError(t, err)
	assert.Equal(t, len(matches), 1, "records: %v", matches)
	data, err := os.ReadFile(matches[0])
	assert.NilError(t, err)
	var rec struct {
		Remote *struct {
			SidecarID string `json:"sidecar_id"`
			CommandID string `json:"command_id"`
			HostRepo  string `json:"host_repo"`
		} `json:"remote"`
	}
	assert.NilError(t, json.Unmarshal(data, &rec))
	assert.Assert(t, rec.Remote != nil, "record: %s", data)
	assert.Assert(t, rec.Remote.SidecarID != "", "record: %s", data)
	assert.Equal(t, rec.Remote.CommandID, "cmd-1")
}

// syncBuffer is a bytes.Buffer safe for a subprocess's output-copying
// goroutine to write to while the test polls it from another.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForOutput polls buf until it contains want, so a check that depends on
// a subprocess's progress does not race it.
func waitForOutput(t *testing.T, buf *syncBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if strings.Contains(buf.String(), want) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %q; output so far: %s", want, buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestFactoryBuildRefusesChunkBinaryWithoutRemote(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeChunkConfig(t, workDir, nil)
	writePrompts(t, filepath.Join(workDir, ".chunk", "reviews"), "correctness")

	result := binary.RunCLI(t, []string{"factory", "build", "--chunk-binary", "dist/chunk-linux", "add a --verbose flag"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "--chunk-binary needs --remote."), "stderr: %s", result.Stderr)
}

// pullTestRepo creates a plain git repo, independent of the project's own
// checkout, with one commit holding the baseline files a pull test edits
// against: a run's worktree, local or on the host, is never the project
// itself.
func pullTestRepo(t *testing.T, name string) string {
	t.Helper()
	dir := gitrepo.SetupGitRepo(t, "test-org", name)
	for file, content := range map[string]string{
		"keep.txt":   "keep\n",
		"edit.txt":   "original\n",
		"delete.txt": "delete me\n",
	} {
		assert.NilError(t, os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644))
	}
	gitrepo.AddFile(t, dir, ".")
	cmd := exec.Command("git", "commit", "-q", "-m", "baseline")
	cmd.Dir = dir
	cmd.Env = gitrepo.GitEnv(dir)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))
	return dir
}

// chunk factory pull brings a finished remote run's work back: it reads the
// host's own run record over SSH for its worktree - not anything the run
// printed - rsyncs it into this run's local worktree, stages what it added so
// 'git diff' shows it rather than "??", and only then tears the host down.
func TestFactoryPullBringsAFinishedRunsWorkBackAndTearsDownTheHost(t *testing.T) {
	env, cci, _, workDir := setupReviewProjectWithSSH(t, nil, "correctness")

	// The record this test writes, and the one 'chunk factory pull' reads
	// back afterward, must land in the same data directory the spawned
	// binary uses.
	t.Setenv(config.EnvXDGDataHome, filepath.Join(env.HomeDir, ".local", "share"))
	root := config.CanonicalProjectRoot(workDir)
	dataDir, err := config.RepoDataDir(root)
	assert.NilError(t, err)

	localWT := pullTestRepo(t, "local-wt")
	hostWT := pullTestRepo(t, "host-wt")

	// What the run did on the host: edited a file, added one, deleted one,
	// and added a binary file, exactly as an implementer's workspace keeps
	// its work - uncommitted changes on top of the baseline.
	assert.NilError(t, os.WriteFile(filepath.Join(hostWT, "edit.txt"), []byte("edited!\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(hostWT, "new.txt"), []byte("new file\n"), 0o644))
	assert.NilError(t, os.Remove(filepath.Join(hostWT, "delete.txt")))
	binaryContent := []byte{0x00, 0x01, 0xff, 0xfe, 'P', 'N', 'G'}
	assert.NilError(t, os.WriteFile(filepath.Join(hostWT, "binary.bin"), binaryContent, 0o644))

	// The host's own run record, read over SSH rather than parsed from any
	// output: it names the worktree the host's 'factory build' actually made.
	hostRec := factory.Record{RunID: "host-run", Worktree: hostWT}
	hostRecJSON, err := json.Marshal(hostRec)
	assert.NilError(t, err)
	cci.ExecResponse = &fakes.ExecResponse{CommandID: "cmd-record-read", Stdout: string(hostRecJSON)}

	// The host's 'chunk factory build' has already finished, successfully.
	ended := "2026-01-15T10:05:00.000Z"
	exitCode := 0
	cci.CommandResponse = &fakes.CommandResponse{
		CreatedAt: "2026-01-15T10:00:00.000Z", EndedAt: &ended, ExitCode: &exitCode, Phase: "ended", SidecarID: "sidecar-host-1",
	}

	rec := factory.Record{
		RunID:    "r-pulltest",
		Prompt:   "add a feature",
		Worktree: localWT,
		Branch:   "chunk/factory/add-a-feature/r-pulltest",
		Baseline: "deadbeef",
		Head:     "deadbeef",
		Remote:   &factory.RemoteRun{SidecarID: "sidecar-host-1", CommandID: "cmd-build-1", HostRepo: "/home/user/test-repo"},
	}
	assert.NilError(t, factory.SaveRecord(dataDir, rec))

	// The run's one-sidecar pool left a state file behind; pulling must clean
	// it up along with the sidecar.
	poolDataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)
	poolStatePath := filepath.Join(poolDataDir, "pools", "factory-remote-"+rec.RunID+".json")
	assert.NilError(t, os.MkdirAll(filepath.Dir(poolStatePath), 0o755))
	assert.NilError(t, os.WriteFile(poolStatePath, []byte("{}"), 0o644))

	result := binary.RunCLI(t, []string{"factory", "pull", rec.RunID}, env, workDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	// The pulled tree: the edit landed, the new and binary files are there,
	// the deleted one is gone, and an untouched file stayed as it was.
	assertFileContent(t, localWT, "edit.txt", "edited!\n")
	assertFileContent(t, localWT, "new.txt", "new file\n")
	assertFileContent(t, localWT, "keep.txt", "keep\n")
	data, err := os.ReadFile(filepath.Join(localWT, "binary.bin"))
	assert.NilError(t, err)
	assert.DeepEqual(t, data, binaryContent)
	_, err = os.Stat(filepath.Join(localWT, "delete.txt"))
	assert.Assert(t, os.IsNotExist(err), "the deleted file should be gone from the pulled tree")

	// The new and binary files were staged with 'git add -A -N', so they show
	// up in 'git diff HEAD' rather than as untracked - and so does the
	// deletion, which staging with '-A' would otherwise hide from a plain
	// 'git diff' by removing it from the index too.
	diffCmd := exec.Command("git", "diff", "HEAD", "--name-only")
	diffCmd.Dir = localWT
	diffCmd.Env = gitrepo.GitEnv(localWT)
	diffOut, err := diffCmd.CombinedOutput()
	assert.NilError(t, err, string(diffOut))
	for _, want := range []string{"edit.txt", "new.txt", "binary.bin", "delete.txt"} {
		assert.Assert(t, strings.Contains(string(diffOut), want), "git diff HEAD --name-only: %s", diffOut)
	}

	assert.Assert(t, strings.Contains(result.Stderr, localWT), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "git -C "+localWT+" diff HEAD"), "stderr: %s", result.Stderr)
	// The record's own Baseline and Branch are the host's and mean nothing
	// here; the printed hint must not mention them.
	assert.Assert(t, !strings.Contains(result.Stderr, rec.Baseline), "stderr should not print the host's baseline: %s", result.Stderr)
	assert.Assert(t, !strings.Contains(result.Stderr, rec.Branch), "stderr should not print the host's branch: %s", result.Stderr)

	// Only once the work is local is the host torn down: its sidecar deleted
	// and the record's remote run cleared.
	var deleted bool
	for _, r := range cci.Recorder.AllRequests() {
		if r.Method == "DELETE" && r.URL.Path == "/api/v3/sidecar/instances/sidecar-host-1" {
			deleted = true
		}
	}
	assert.Assert(t, deleted, "expected the host sidecar to be deleted; requests: %v", cci.Recorder.AllRequests())

	_, err = os.Stat(poolStatePath)
	assert.Assert(t, os.IsNotExist(err), "expected the pool's state file to be removed: %v", err)

	saved, err := factory.LoadRecord(root, rec.RunID)
	assert.NilError(t, err)
	assert.Assert(t, saved.Remote == nil, "record: %+v", saved)
	assert.Equal(t, saved.Worktree, localWT)
}

func assertFileContent(t *testing.T, dir, name, want string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	assert.NilError(t, err)
	assert.Equal(t, string(data), want)
}

// Bootstrap writes the config and sets up the sidecar image, neither of which
// needs GitHub, so a project with another origin, or none, and no GitHub
// token is set up all the same.
func TestFactoryBootstrapNeedsNoGitHubOrigin(t *testing.T) {
	tests := []struct {
		name string
		git  []string
	}{
		{
			name: "gitlab origin",
			git:  []string{"remote", "set-url", "origin", "https://gitlab.com/test-org/test-repo.git"},
		},
		{
			name: "no origin",
			git:  []string{"remote", "remove", "origin"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _, workDir := setupReviewProject(t, nil)
			env.GithubToken = ""
			git := exec.Command("git", tt.git...)
			git.Dir = workDir
			git.Env = gitrepo.GitEnv(workDir)
			out, err := git.CombinedOutput()
			assert.NilError(t, err, string(out))
			err = os.Remove(filepath.Join(workDir, ".chunk", "config.json"))
			assert.NilError(t, err)

			result := binary.RunCLI(t, []string{"factory", "bootstrap", "--skip-sidecar"}, env, workDir)

			assert.Check(t, cmp.Equal(result.ExitCode, 0), "stderr: %s", result.Stderr)
			_, err = os.Stat(filepath.Join(workDir, ".chunk", "config.json"))
			assert.Check(t, err)
			// Narrowed to "needs a GitHub origin": detecting the VCS may warn
			// without a GitHub origin.
			assert.Check(t, !strings.Contains(result.Stderr, "needs a GitHub origin"), "stderr: %s", result.Stderr)
		})
	}
}

// A project with nothing left to set up is told it is ready for chunk
// factory, its review prompts are left as they are, and no sidecar boots to
// find that out.
func TestFactoryBootstrapSaysTheProjectIsAlreadySetUp(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		config map[string]interface{}
	}{
		{
			name:   "config and image",
			args:   []string{"factory", "bootstrap"},
			config: map[string]interface{}{"validation": map[string]interface{}{"sidecarImage": "snap-old"}},
		},
		{
			name: "config and --skip-sidecar",
			args: []string{"factory", "bootstrap", "--skip-sidecar"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, cci, workDir := setupReviewProject(t, nil, "mine")
			writeChunkConfig(t, workDir, tt.config)

			result := binary.RunCLI(t, tt.args, env, workDir)

			assert.Check(t, result.ExitCode != 0, "expected non-zero exit code")
			assert.Check(t, cmp.Contains(result.Stderr, "This project is already set up for chunk factory."))
			assert.Check(t, cmp.Contains(result.Stderr, "chunk factory plan"))
			assert.Check(t, cmp.Len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances"), 0))
			data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "reviews", "mine.md"))
			assert.NilError(t, err)
			assert.Check(t, cmp.Equal(string(data), "review mine"))
		})
	}
}

// A suite that cannot run here is refused before any sidecar starts: no
// CircleCI is configured, so getting further would fail differently.
func TestFactoryEvalRefusesCasesThatCannotRun(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	suite := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(suite, "case.patch"), []byte("diff --git a/x b/x\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(suite, "suite.json"), []byte(`{"cases": [
		{"name": "gone", "revision": "0000000000000000000000000000000000000000", "request": "r", "patch": "case.patch"}
	]}`), 0o644))

	result := binary.RunCLI(t, []string{"factory", "eval", "--suite", suite}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Some cases cannot run in this repository."), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "case gone: revision 0000000000000000000000000000000000000000 is not in this repository"), "stderr: %s", result.Stderr)
}

// Commits carry no labels, so without the grader nothing would score them,
// and a run needs one source of cases or the other.
func TestFactoryEvalRefusesFlagsThatScoreNothing(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"factory", "eval"}, "at least one of the flags in the group [suite commits] is required"},
		{[]string{"factory", "eval", "--commits", "3", "--no-grade"}, "[commits no-grade] were all set"},
		{[]string{"factory", "eval", "--suite", t.TempDir(), "--commits", "3"}, "[commits suite] were all set"},
		{[]string{"factory", "eval", "--suite", t.TempDir(), "--no-grade", "--grader-model", "x"}, "[grader-model no-grade] were all set"},
		// The group is satisfied by --suite being given, so an empty one would
		// otherwise fall through to the commits path and fail there.
		{[]string{"factory", "eval", "--suite", ""}, "--suite needs a directory"},
	} {
		result := binary.RunCLI(t, tc.args, env, workDir)
		assert.Assert(t, result.ExitCode != 0, "%v: expected non-zero exit code", tc.args)
		assert.Assert(t, strings.Contains(result.Stderr, tc.want), "%v: stderr: %s", tc.args, result.Stderr)
	}
}

// A revision with no history to take commits from is refused before any
// sidecar starts.
func TestFactoryEvalRefusesCommitsFromAMissingRevision(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	result := binary.RunCLI(t, []string{"factory", "eval", "--commits", "3", "--from", "no-such-branch"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Could not make cases of the commits on no-such-branch."), "stderr: %s", result.Stderr)
}

// Without a GitHub origin the sidecars cannot name their copy of the
// repository, which is said before any credential or org prompt.
func TestFactoryEvalNeedsAGitHubOrigin(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	cmd := exec.Command("git", "remote", "remove", "origin")
	cmd.Dir = workDir
	cmd.Env = gitrepo.GitEnv(workDir)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))

	result := binary.RunCLI(t, []string{"factory", "eval", "--suite", t.TempDir()}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "chunk factory eval needs a GitHub origin."), "stderr: %s", result.Stderr)
}

// readProjectConfig reads .chunk/config.json from workDir.
func readProjectConfig(t *testing.T, workDir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "config.json"))
	assert.NilError(t, err)
	var cfg map[string]any
	err = json.Unmarshal(data, &cfg)
	assert.NilError(t, err)
	return cfg
}

// claudeOnSidecar answers the check for Claude Code on a sidecar, which would
// otherwise depend on whether this machine has it. It returns how many times
// the check was run.
func claudeOnSidecar(sshSrv *fakes.SSHServer, installed bool) *atomic.Int32 {
	var checks atomic.Int32
	sshSrv.OverrideLocally(func(command string) (string, int, bool) {
		if !strings.Contains(command, "command -v claude") {
			return "", 0, false
		}
		checks.Add(1)
		// A real sidecar runs the command without a shell, so a script not
		// handed to one fails there the way a missing program does.
		if !strings.HasPrefix(command, "bash -l -c ") {
			return "", 127, true
		}
		if installed {
			return "", 0, true
		}
		return "", 97, true
	})
	return &checks
}

// sidecarImages returns the image of each sidecar the CLI created.
func sidecarImages(t *testing.T, cci *fakes.FakeCircleCI) []string {
	t.Helper()
	var images []string
	for _, r := range filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances") {
		if r.URL.Path != "/api/v3/sidecar/instances" {
			continue
		}
		var req struct {
			Data struct {
				Attributes struct {
					Image string `json:"image"`
				} `json:"attributes"`
			} `json:"data"`
		}
		err := json.Unmarshal(r.Body, &req)
		assert.NilError(t, err)
		images = append(images, req.Data.Attributes.Image)
	}
	return images
}

// Bootstrap sets the project's environment up on a sidecar of its own,
// snapshots it and records the snapshot, which is what chunk factory starts
// its sidecars from. Prompts the project has are left as they are.
func TestFactoryBootstrapRecordsTheSidecarImage(t *testing.T) {
	env, cci, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	claudeOnSidecar(sshSrv, true)
	marker := filepath.Join(t.TempDir(), "setup-ran")
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{
			"stack": "go",
			"setup": []map[string]any{{"name": "install", "command": "touch " + marker + " && echo installing"}},
		},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	_, err := os.Stat(marker)
	assert.NilError(t, err, "the setup step did not run")
	cfg := readProjectConfig(t, workDir)
	assert.Equal(t, cfg["validation"].(map[string]any)["sidecarImage"], "snap-1")
	// With no --image, the sidecar starts from the image that has Claude
	// Code, which chunk factory build runs on snapshots of.
	assert.DeepEqual(t, sidecarImages(t, cci), []string{"cimg-base:2026.09-claude"})

	snaps := filterByMethod(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/snapshots")
	assert.Equal(t, len(snaps), 1)
	var body struct {
		Data struct {
			Attributes struct {
				Name string `json:"name"`
			} `json:"attributes"`
		} `json:"data"`
	}
	err = json.Unmarshal(snaps[0].Body, &body)
	assert.NilError(t, err)
	// Dated to the second, so a rerun on the same day gets a name of its own.
	assert.Assert(t, regexp.MustCompile(`^test-repo-\d{4}-\d{2}-\d{2}-\d{6}$`).MatchString(body.Data.Attributes.Name),
		"snapshot name: %s", body.Data.Attributes.Name)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "DELETE", "/api/v3/sidecar/instances/")), 1,
		"the sidecar that was snapshotted is deleted")

	// The setup steps' output is shown as they run, on stderr: stdout is for
	// the --json report.
	assert.Check(t, cmp.Contains(result.Stderr, "installing"))
	assert.Check(t, !strings.Contains(result.Stdout, "installing"), "stdout: %s", result.Stdout)
	assert.Check(t, cmp.Contains(result.Stderr, `chunk factory plan "<what to change>"`))
	// Bootstrap never touches the review prompts the project has.
	data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "reviews", "mine.md"))
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(string(data), "review mine"))
}

// An environment with no setup steps has nothing for a snapshot to hold, so no
// sidecar is made and the default image stays.
func TestFactoryBootstrapRecordsNoImageWithoutSetupSteps(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, nil, "mine")
	writeChunkConfig(t, workDir, map[string]interface{}{"environment": map[string]any{"stack": "unknown", "setup": []any{}}})

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "No setup steps detected"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0)
	_, hasValidation := readProjectConfig(t, workDir)["validation"]
	assert.Assert(t, !hasValidation, "no image is recorded")
}

// A project without .chunk/config.json gets one, as chunk init would write it,
// rather than being sent to run init first.
func TestFactoryBootstrapWritesTheConfig(t *testing.T) {
	for _, extra := range [][]string{{}, {"--json"}} {
		t.Run(strings.Join(append([]string{"bootstrap"}, extra...), " "), func(t *testing.T) {
			env, _, workDir := setupReviewProject(t, nil, "mine")
			err := os.Remove(filepath.Join(workDir, ".chunk", "config.json"))
			assert.NilError(t, err)

			result := binary.RunCLI(t, append([]string{"factory", "bootstrap", "--skip-sidecar"}, extra...), env, workDir)

			assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
			cfg := readProjectConfig(t, workDir)
			assert.Equal(t, cfg["orgID"], "org-aaa")
			vcs := cfg["vcs"].(map[string]any)
			assert.Equal(t, vcs["org"], "test-org")
			assert.Equal(t, vcs["repo"], "test-repo")
			// Hooks belong to chunk init.
			_, err = os.Stat(filepath.Join(workDir, ".claude", "settings.json"))
			assert.Assert(t, os.IsNotExist(err), "bootstrap wrote agent hooks")
			if len(extra) == 0 {
				return
			}
			var report map[string]any
			err = json.Unmarshal([]byte(result.Stdout), &report)
			assert.NilError(t, err, result.Stdout)
			assert.Check(t, cmp.DeepEqual(report, map[string]any{"config_written": true, "sidecar_image": ""}))
		})
	}
}

// With --json, what the run made is reported even when the sidecar image
// could not be set up. A project with a config is used, since a fresh one's
// environment is detected rather than chosen, so its setup step cannot be
// made to fail; TestFactoryBootstrapWritesTheConfig covers config_written.
func TestFactoryBootstrapReportsAsJSONWhenTheImageFails(t *testing.T) {
	env, _, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	claudeOnSidecar(sshSrv, true)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "exit 3"}}},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--json"}, env, workDir)

	assert.Check(t, result.ExitCode != 0, "expected non-zero exit code")
	// With --json the error is JSON on stderr too, so its quotes are escaped.
	assert.Check(t, cmp.Contains(result.Stderr, `Setup step \"install\" exited with status 3`))
	var report map[string]any
	err := json.Unmarshal([]byte(result.Stdout), &report)
	assert.NilError(t, err, result.Stdout)
	assert.Check(t, cmp.DeepEqual(report, map[string]any{"config_written": false, "sidecar_image": ""}))
}

// An --image without Claude Code is snapshotted and recorded, and no
// `command -v claude` check runs on the sidecar.
func TestFactoryBootstrapRecordsAnImageWithoutClaude(t *testing.T) {
	env, cci, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	checks := claudeOnSidecar(sshSrv, false)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "true"}}},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--image", "cimg-base:2026.09"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Check(t, cmp.DeepEqual(sidecarImages(t, cci), []string{"cimg-base:2026.09"}))
	assert.Check(t, cmp.Len(filterByMethod(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/snapshots"), 1))
	cfg := readProjectConfig(t, workDir)
	assert.Check(t, cmp.Equal(cfg["validation"].(map[string]any)["sidecarImage"], "snap-1"))
	assert.Check(t, cmp.Equal(checks.Load(), int32(0)), "bootstrap checked for Claude Code")
}

// Bootstrap's sidecar has a workspace of its own, which the setup steps run
// in, and the project's active sidecar is left as it was.
func TestFactoryBootstrapLeavesTheActiveSidecarAlone(t *testing.T) {
	env, _, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	claudeOnSidecar(sshSrv, true)
	marker := filepath.Join(t.TempDir(), "setup-dir")
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{
			"stack": "go",
			"setup": []map[string]any{{"name": "install", "command": "pwd -P > " + marker}},
		},
	})
	stateDir := projectDataDir(t, env, workDir)
	err := os.MkdirAll(stateDir, 0o755)
	assert.NilError(t, err)
	statePath := filepath.Join(stateDir, sidecar.StateFileName("", gitCurrentBranch(t, workDir)))
	state := []byte(`{"sidecar_ids":["sidecar-mine"],"workspace":"/home/user/mine"}`)
	err = os.WriteFile(statePath, state, 0o644)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	ran, err := os.ReadFile(marker)
	assert.NilError(t, err, "the setup step did not run")
	want, err := filepath.EvalSymlinks(filepath.Join(env.Extra["CHUNK_SIDECAR_HOME"], "test-repo"))
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(string(ran)), want)
	after, err := os.ReadFile(statePath)
	assert.NilError(t, err)
	assert.Equal(t, string(after), string(state))
}

// sidecarImageProject is a project with a saved environment, so a bootstrap
// run only sets up the sidecar image.
func sidecarImageProject(t *testing.T) (*testenv.TestEnv, *fakes.FakeCircleCI, string) {
	t.Helper()
	env, cci, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	claudeOnSidecar(sshSrv, true)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "true"}}},
	})
	return env, cci, workDir
}

// snapshotNames returns the name of each snapshot the CLI asked for.
func snapshotNames(t *testing.T, cci *fakes.FakeCircleCI) []string {
	t.Helper()
	var names []string
	for _, r := range filterByMethod(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/snapshots") {
		var body struct {
			Data struct {
				Attributes struct {
					Name string `json:"name"`
				} `json:"attributes"`
			} `json:"data"`
		}
		err := json.Unmarshal(r.Body, &body)
		assert.NilError(t, err)
		names = append(names, body.Data.Attributes.Name)
	}
	return names
}

func TestFactoryBootstrapNamesTheSnapshotAsAsked(t *testing.T) {
	env, cci, workDir := sidecarImageProject(t)

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--snapshot-name", "compass-env"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.DeepEqual(t, snapshotNames(t, cci), []string{"compass-env"})
	assert.Equal(t, readProjectConfig(t, workDir)["validation"].(map[string]any)["sidecarImage"], "snap-1")
}

// The gate commands run on a sidecar from now on, which chunk validate does
// without being asked, so the run says it marked them.
func TestFactoryBootstrapSaysItMarkedTheGateCommandsRemote(t *testing.T) {
	env, _, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	claudeOnSidecar(sshSrv, true)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"commands":    []map[string]any{{"name": "lint", "run": "golangci-lint run", "role": "gate"}},
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "true"}}},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "Marked install and gate commands as remote"), "stderr: %s", result.Stderr)
	commands := readProjectConfig(t, workDir)["commands"].([]any)
	assert.Equal(t, commands[0].(map[string]any)["remote"], true)
}

func TestFactoryBootstrapReportsTheSidecarImageAsJSON(t *testing.T) {
	env, _, workDir := sidecarImageProject(t)

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--json"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	var report map[string]any
	err := json.Unmarshal([]byte(result.Stdout), &report)
	assert.NilError(t, err, result.Stdout)
	// sidecarImageProject has a config, so none is written.
	assert.Check(t, cmp.DeepEqual(report, map[string]any{"config_written": false, "sidecar_image": "snap-1"}))
}

// A name the org already has is explained, with the ways out, rather than
// left as a bare conflict from the API, and before any sidecar is made for
// the setup steps to run on.
func TestFactoryBootstrapExplainsATakenSnapshotName(t *testing.T) {
	env, cci, workDir := sidecarImageProject(t)
	cci.AddSnapshot(fakes.Snapshot{ID: "snap-old", OrgID: "org-aaa", Name: "compass-env"})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--snapshot-name", "compass-env"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "A snapshot named compass-env already exists"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "--snapshot-name"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "chunk config set validation.sidecarImage snap-old"), "stderr: %s", result.Stderr)
	_, hasValidation := readProjectConfig(t, workDir)["validation"]
	assert.Assert(t, !hasValidation, "no image is recorded")
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0,
		"a sidecar was made for a name that is taken")
}

// A taken name the listing did not show is still explained when the snapshot
// call refuses it, and the sidecar that was made for it is deleted. The fake
// keeps names unique across orgs, so a snapshot filed under no org stands in
// for one created after the listing.
func TestFactoryBootstrapExplainsATakenSnapshotNameAtTheSnapshot(t *testing.T) {
	env, cci, workDir := sidecarImageProject(t)
	cci.AddSnapshot(fakes.Snapshot{ID: "snap-old", Name: "compass-env"})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--snapshot-name", "compass-env"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "A snapshot named compass-env already exists"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "--snapshot-name"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "chunk config set validation.sidecarImage"), "stderr: %s", result.Stderr)
	_, hasValidation := readProjectConfig(t, workDir)["validation"]
	assert.Assert(t, !hasValidation, "no image is recorded")
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "DELETE", "/api/v3/sidecar/instances/")), 1,
		"the sidecar is deleted")
}

// Bootstrap never runs Claude, so it needs no Claude credential: chunk
// factory build asks for its own when it starts.
func TestFactoryBootstrapNeedsNoClaudeCredential(t *testing.T) {
	env, _, workDir := sidecarImageProject(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Check(t, !strings.Contains(result.Stderr, "No Claude credential"), "stderr: %s", result.Stderr)
	cfg := readProjectConfig(t, workDir)
	assert.Check(t, cmp.Equal(cfg["validation"].(map[string]any)["sidecarImage"], "snap-1"))
}

// Bootstrap does not read .chunk/reviews, so one it cannot read changes
// nothing: a configured project is still set up already, and a fresh one
// still gets its config.
func TestFactoryBootstrapIgnoresTheReviewsDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads the directory whatever its mode")
	}
	unreadableReviews := func(t *testing.T, workDir string) {
		t.Helper()
		reviews := filepath.Join(workDir, ".chunk", "reviews")
		err := os.Chmod(reviews, 0o000)
		assert.NilError(t, err)
		t.Cleanup(func() { _ = os.Chmod(reviews, 0o755) })
	}

	t.Run("configured project", func(t *testing.T) {
		env, _, workDir := setupReviewProject(t, nil, "mine")
		unreadableReviews(t, workDir)

		result := binary.RunCLI(t, []string{"factory", "bootstrap", "--skip-sidecar"}, env, workDir)

		assert.Check(t, result.ExitCode != 0, "expected non-zero exit code")
		assert.Check(t, cmp.Contains(result.Stderr, "This project is already set up for chunk factory."))
		assert.Check(t, !strings.Contains(result.Stderr, ".chunk/reviews"), "stderr: %s", result.Stderr)
	})

	t.Run("fresh project", func(t *testing.T) {
		env, _, workDir := setupReviewProject(t, nil, "mine")
		err := os.Remove(filepath.Join(workDir, ".chunk", "config.json"))
		assert.NilError(t, err)
		unreadableReviews(t, workDir)

		result := binary.RunCLI(t, []string{"factory", "bootstrap", "--skip-sidecar"}, env, workDir)

		assert.Check(t, cmp.Equal(result.ExitCode, 0), "stderr: %s", result.Stderr)
		_, err = os.Stat(filepath.Join(workDir, ".chunk", "config.json"))
		assert.Check(t, err)
		assert.Check(t, !strings.Contains(result.Stderr, "Could not read .chunk/reviews"), "stderr: %s", result.Stderr)
	})
}
