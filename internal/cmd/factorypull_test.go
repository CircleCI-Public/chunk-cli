package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// factoryPullTestRepo sets up a git repository to key records to, with
// chunk's data directories isolated to a temp dir, without needing rsync or
// ssh: enough for the record bookkeeping tests, which never touch a sidecar
// for real.
func factoryPullTestRepo(t *testing.T) (root, dataDir string) {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root = gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	var err error
	dataDir, err = config.RepoDataDir(root)
	assert.NilError(t, err)
	return root, dataDir
}

func saveRecordAt(t *testing.T, dataDir string, rec factory.Record, mtime time.Time) {
	t.Helper()
	assert.NilError(t, factory.SaveRecord(dataDir, rec))
	path := filepath.Join(dataDir, "factory", rec.RunID+".json")
	assert.NilError(t, os.Chtimes(path, mtime, mtime))
}

// With no run id, a pull picks the most recently saved record that has a
// remote run, by its file's own modification time: a record carries no
// timestamp of its own.
func TestLatestRemoteRunIDPicksTheMostRecentlySavedRecord(t *testing.T) {
	_, dataDir := factoryPullTestRepo(t)
	now := time.Now()

	saveRecordAt(t, dataDir, factory.Record{RunID: "r-older", Remote: &factory.RemoteRun{SidecarID: "sb-1", CommandID: "cmd-1"}}, now.Add(-time.Hour))
	saveRecordAt(t, dataDir, factory.Record{RunID: "r-newer", Remote: &factory.RemoteRun{SidecarID: "sb-2", CommandID: "cmd-2"}}, now)
	// A local run, with no remote run, must not be picked over either above.
	saveRecordAt(t, dataDir, factory.Record{RunID: "local-run"}, now.Add(time.Hour))
	// A pool state file beside the records must not be read as one.
	assert.NilError(t, os.WriteFile(filepath.Join(dataDir, "factory", "r-newer.pool.json"), []byte("not json"), 0o600))

	runID, err := latestRemoteRunID(dataDir)

	assert.NilError(t, err)
	assert.Equal(t, runID, "r-newer")
}

// With no remote run anywhere in the project, chunk factory pull must say so
// rather than picking a local run or failing some other way.
func TestLatestRemoteRunIDErrorsWithNoRemoteRun(t *testing.T) {
	_, dataDir := factoryPullTestRepo(t)
	saveRecordAt(t, dataDir, factory.Record{RunID: "local-run"}, time.Now())

	_, err := latestRemoteRunID(dataDir)

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), "No remote factory run to pull.")
	assert.Equal(t, ue.UserExitCode(), ExitBadArgs)
}

// The same refusal applies to a named run that exists but was never started
// with --remote, or was already pulled.
func TestFactoryPullTargetRefusesANamedRunWithNoRemote(t *testing.T) {
	root, dataDir := factoryPullTestRepo(t)
	saveRecordAt(t, dataDir, factory.Record{RunID: "local-run"}, time.Now())

	_, err := factoryPullTarget(root, dataDir, []string{"local-run"})

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.Contains(ue.UserMessage(), "local-run"), "message: %s", ue.UserMessage())
	assert.Assert(t, strings.Contains(ue.UserMessage(), "nothing to pull"), "message: %s", ue.UserMessage())
}

func TestFactoryPullTargetErrorsOnAnUnknownRun(t *testing.T) {
	root, dataDir := factoryPullTestRepo(t)

	_, err := factoryPullTarget(root, dataDir, []string{"nope"})

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.Contains(ue.UserMessage(), "No factory run nope"), "message: %s", ue.UserMessage())
}

// newPullTestClient wires a *circleci.Client to a fake CircleCI API that
// never runs anything for real, which is enough for pullRemoteRun's failure
// paths: they fail before anything ever reaches a sidecar for real.
func newPullTestClient(t *testing.T, cci *fakes.FakeCircleCI) *circleci.Client {
	t.Helper()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	return client
}

func endedCommand(exitCode int) *fakes.CommandResponse {
	ended := "2026-01-15T10:05:00.000Z"
	return &fakes.CommandResponse{CreatedAt: "2026-01-15T10:00:00.000Z", EndedAt: &ended, ExitCode: &exitCode, Phase: "ended"}
}

// A pull that fails before the work is local - here, because the host's own
// run record cannot be parsed - must not touch the sidecar or the record: a
// pull has to be safe to run again.
func TestPullRemoteRunLeavesTheSidecarAndRecordAloneOnAnEarlyFailure(t *testing.T) {
	root, _ := factoryPullTestRepo(t)
	cci := fakes.NewFakeCircleCI()
	// The command has already ended; the default exec response ("ok\n") is not
	// JSON, so reading the host's record fails before anything else happens.
	cci.CommandResponse = endedCommand(0)
	client := newPullTestClient(t, cci)

	var out iostream.Streams
	rec := factory.Record{
		RunID:    "r-test",
		Worktree: filepath.Join(t.TempDir(), "wt"),
		Remote:   &factory.RemoteRun{SidecarID: "sb-1", CommandID: "cmd-1", HostRepo: "/home/user/test-repo"},
	}
	wantRemote := *rec.Remote

	err := pullRemoteRun(context.Background(), client, root, out, func(iostream.Level, string) {}, &rec)

	assert.Assert(t, err != nil, "expected the pull to fail")
	assert.Assert(t, rec.Remote != nil, "Remote must not be cleared on failure")
	assert.Equal(t, *rec.Remote, wantRemote)

	for _, r := range cci.Recorder.AllRequests() {
		assert.Assert(t, r.Method != "DELETE", "a failed pull must not delete anything: %v", r)
	}
}

// unfinishedCommand is what GetCommand reports for a host run that has not
// ended yet: no EndedAt.
func unfinishedCommand() *fakes.CommandResponse {
	return &fakes.CommandResponse{CreatedAt: "2026-01-15T10:00:00.000Z", Phase: "running"}
}

// newPullSSHClient wires a *circleci.Client to a fake CircleCI API whose
// sidecars run commands on this machine for real, over the same WebSocket SSH
// tunnel chunk uses, so RsyncPull runs for real rather than against a canned
// response.
func newPullSSHClient(t *testing.T) (*fakes.FakeCircleCI, *circleci.Client) {
	t.Helper()
	for _, bin := range []string{"rsync", "ssh"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	t.Setenv(config.EnvHome, t.TempDir())
	sshSrv := fakes.NewSSHServerAcceptingAnyKey(t)
	sshSrv.RunLocally()

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	return cci, client
}

// pullWorktreeRepo creates a plain git repo with one baseline commit, standing
// in for a run's worktree, local or on the host.
func pullWorktreeRepo(t *testing.T, name string) string {
	t.Helper()
	dir := gitrepo.SetupGitRepo(t, "test-org", name)
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "edit.txt"), []byte("original\n"), 0o644))
	gitrepo.AddFile(t, dir, ".")
	cmd := exec.Command("git", "commit", "-q", "-m", "baseline")
	cmd.Dir = dir
	cmd.Env = gitrepo.GitEnv(dir)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))
	return dir
}

// This is the behavior the request calls out by name: a pull on a run that
// has not finished yet must follow it exactly as --remote does - the host's
// stderr reaching the terminal - before it ever reads the host's record or
// touches a worktree. Inverting or dropping the EndedAt == nil case in
// pullRemoteRun would skip straight to rsyncing a worktree the host might not
// have finished writing, yet still happen to pass here if the host's stderr
// were never checked; asserting on the streamed stderr is what catches that.
func TestPullRemoteRunFollowsAnUnfinishedHostRunThenPullsItsWork(t *testing.T) {
	root, _ := factoryPullTestRepo(t)
	cci, client := newPullSSHClient(t)

	localWT := pullWorktreeRepo(t, "local-wt")
	hostWT := pullWorktreeRepo(t, "host-wt")
	assert.NilError(t, os.WriteFile(filepath.Join(hostWT, "edit.txt"), []byte("edited!\n"), 0o644))

	hostRec := factory.Record{RunID: "host-run", Worktree: hostWT}
	hostRecJSON, err := json.Marshal(hostRec)
	assert.NilError(t, err)

	// The host's build has not ended yet: GetCommand reports no EndedAt, so
	// pull must follow it - streaming its stderr - rather than treat it as
	// already finished.
	cci.CommandResponse = unfinishedCommand()
	cci.ExecResponse = &fakes.ExecResponse{CommandID: "cmd-record", Stdout: string(hostRecJSON), Stderr: "building on the host\n", ExitCode: 0}

	var stderr bytes.Buffer
	streams := iostream.Streams{Err: &stderr}
	rec := factory.Record{
		RunID:    "r-test",
		Worktree: localWT,
		Remote:   &factory.RemoteRun{SidecarID: "sidecar-host-1", CommandID: "cmd-build-1"},
	}

	err = pullRemoteRun(context.Background(), client, root, streams, func(iostream.Level, string) {}, &rec)

	assert.NilError(t, err, "stderr: %s", stderr.String())
	assert.Assert(t, strings.Contains(stderr.String(), "building on the host"),
		"the host's run was not followed before its work was pulled; stderr: %s", stderr.String())
	data, rerr := os.ReadFile(filepath.Join(localWT, "edit.txt"))
	assert.NilError(t, rerr)
	assert.Equal(t, string(data), "edited!\n")
	assert.Assert(t, rec.Remote == nil, "Remote should be cleared once the work is local")
}

// Ctrl-C on an unfinished run detaches exactly as --remote does: nothing is
// pulled and nothing is torn down.
func TestPullRemoteRunDetachesWithoutPullingWhenAnUnfinishedRunIsInterrupted(t *testing.T) {
	root, _ := factoryPullTestRepo(t)
	cci := fakes.NewFakeCircleCI()
	cci.CommandResponse = unfinishedCommand()
	client := newPullTestClient(t, cci)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancel as soon as following starts, which is after GetCommand reported
	// the run unfinished but before anything is streamed or pulled.
	statusFn := func(_ iostream.Level, msg string) {
		if strings.Contains(msg, "Following the run") {
			cancel()
		}
	}

	streams := iostream.Streams{Err: &bytes.Buffer{}}
	rec := factory.Record{
		RunID:    "r-test",
		Worktree: filepath.Join(t.TempDir(), "wt"),
		Remote:   &factory.RemoteRun{SidecarID: "sb-1", CommandID: "cmd-1"},
	}
	wantRemote := *rec.Remote

	err := pullRemoteRun(ctx, client, root, streams, statusFn, &rec)

	assert.Assert(t, errors.Is(err, errDetached), "got %v", err)
	assert.Assert(t, rec.Remote != nil, "Remote must not be cleared when detaching")
	assert.Equal(t, *rec.Remote, wantRemote)
	for _, r := range cci.Recorder.AllRequests() {
		assert.Assert(t, r.Method != "DELETE", "detaching must not delete anything: %v", r)
	}
}

// Once the host's run has failed outright (GetCommand reports a non-zero
// exit), pulling it is refused the same way, and just as safely: nothing is
// touched.
func TestPullRemoteRunRefusesAFailedHostRunWithoutTouchingAnything(t *testing.T) {
	root, _ := factoryPullTestRepo(t)
	cci := fakes.NewFakeCircleCI()
	cci.CommandResponse = endedCommand(7)
	client := newPullTestClient(t, cci)

	var out iostream.Streams
	rec := factory.Record{
		RunID:    "r-test",
		Worktree: filepath.Join(t.TempDir(), "wt"),
		Remote:   &factory.RemoteRun{SidecarID: "sb-1", CommandID: "cmd-1"},
	}

	err := pullRemoteRun(context.Background(), client, root, out, func(iostream.Level, string) {}, &rec)

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Assert(t, strings.Contains(ue.UserMessage(), "failed on sidecar sb-1"), "message: %s", ue.UserMessage())
	assert.Assert(t, rec.Remote != nil, "Remote must not be cleared on failure")

	for _, r := range cci.Recorder.AllRequests() {
		assert.Assert(t, r.Method != "DELETE", "a failed pull must not delete anything: %v", r)
	}
}
