package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func TestPooledValidateRegistersSubmittedCommand(t *testing.T) {
	regs := captureRegistrations(t)
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{CommandID: "cmd-1"}
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	// Deliberately different: a snapshot run executes in a copy and belongs to
	// the repository it was copied from, and the registration has to name the
	// one the developer is watching.
	snapshotDir := t.TempDir()
	projectRoot := t.TempDir()
	var recordedID string
	result := runPooledValidateCommand(
		context.Background(),
		&sidecar.PoolEntry{ID: "sb-1", RepoPath: "/workspace/repo", Client: client},
		config.Command{Name: "test", Run: "true"},
		"", snapshotDir, projectRoot, nil,
		func(id string) { recordedID = id },
		func(iostream.Level, string) {},
		iostream.Streams{Out: io.Discard, Err: io.Discard},
	)
	assert.NilError(t, result.Err)
	assert.Equal(t, recordedID, "cmd-1")

	select {
	case reg := <-regs:
		assert.Equal(t, reg.CommandID, "cmd-1")
		assert.Equal(t, reg.SidecarID, "sb-1")
		assert.Equal(t, reg.ProjectRoot, projectRoot, "a snapshot run was registered under the copy it ran in")
		assert.Equal(t, reg.Op, string(eventlog.OpValidate))
		assert.Equal(t, reg.Name, "test")
		assert.Assert(t, !reg.SubmittedAt.IsZero())
	case <-time.After(5 * time.Second):
		t.Fatal("pooled validation command was not registered")
	}
}

func TestPooledValidateClearsCommandIDWhenSubmissionFails(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecStatusCode = http.StatusInternalServerError
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	commandID := "stale-command"
	result := runPooledValidateCommand(
		context.Background(),
		&sidecar.PoolEntry{ID: "sb-1", RepoPath: "/workspace/repo", Client: client},
		config.Command{Name: "test", Run: "true"},
		"", t.TempDir(), t.TempDir(), nil,
		func(id string) { commandID = id },
		func(iostream.Level, string) {},
		iostream.Streams{Out: io.Discard, Err: io.Discard},
	)

	assert.Assert(t, result.Err != nil)
	assert.Equal(t, commandID, "")
	ue, ok := errors.AsType[*userError](result.Err)
	assert.Assert(t, ok)
	assert.Equal(t, ue.ErrorCode(), "sidecar.unreachable")
}

// RunRemoteStreamedResult is what reports a command's failure, and a command
// that could not be started never reaches it. The pool's per-command reporter
// closes a sidecar's run on a pass or failure, so a silent failure here would
// leave that sidecar showing as running.
func TestPooledValidateReportsFailureBeforeTheCommandRuns(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecStatusCode = http.StatusInternalServerError
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	var levels []iostream.Level
	var messages []string
	result := runPooledValidateCommand(
		context.Background(),
		&sidecar.PoolEntry{ID: "sb-1", RepoPath: "/workspace/repo", Client: client},
		config.Command{Name: "test", Run: "true"},
		"", t.TempDir(), t.TempDir(), nil, nil,
		func(level iostream.Level, msg string) {
			levels = append(levels, level)
			messages = append(messages, msg)
		},
		iostream.Streams{Out: io.Discard, Err: io.Discard},
	)

	assert.Assert(t, result.Err != nil)
	assert.DeepEqual(t, levels, []iostream.Level{iostream.LevelError})
	assert.Assert(t, strings.Contains(messages[0], "test"), "got %q", messages[0])
	assert.Assert(t, strings.Contains(messages[0], "sb-1"), "got %q", messages[0])
}

func TestPooledValidateReportsMissingWorkspace(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{CommandID: "probe-1", ExitCode: 1}
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	result := runPooledValidateCommand(
		context.Background(),
		&sidecar.PoolEntry{ID: "sb-1", RepoPath: "/workspace/repo", Client: client},
		config.Command{Name: "test", Run: "true"},
		"", t.TempDir(), t.TempDir(), nil, nil,
		func(iostream.Level, string) {},
		iostream.Streams{Out: io.Discard, Err: io.Discard},
	)

	assert.Assert(t, result.Err != nil)
	ue, ok := errors.AsType[*userError](result.Err)
	assert.Assert(t, ok)
	assert.Equal(t, ue.ErrorCode(), "sidecar.workspace_missing")
}

// A project whose .chunk sits below the git root registers its commands under
// the git top-level, because that is the root the daemon discovers from the
// project breadcrumb and buckets buffered output by. Registered under the cwd
// instead, the watch TUI never shows the output marker and the pane for that
// command cannot be opened.
func TestValidateRegistersCommandUnderTheGitRoot(t *testing.T) {
	regs := captureRegistrations(t)

	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvXDGConfigHome, filepath.Join(home, ".config"))
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	t.Setenv(config.EnvChunkSessionID, "")
	t.Setenv(session.EnvClaudeSessionID, "")
	t.Setenv(config.EnvAnthropicBaseURL, unroutableBaseURL)
	t.Setenv(config.EnvGitHubAPIURL, unroutableBaseURL)

	pubKey := fakes.GenerateSSHKeypairAt(t, filepath.Join(home, ".ssh", "chunk_ai"))
	sshSrv := fakes.NewSSHServer(t, pubKey)
	useLocalSidecar(t, sshSrv)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	cci.ExecResponse = &fakes.ExecResponse{CommandID: "cmd-1"}
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	t.Setenv(config.EnvCircleCIBaseURL, srv.URL)
	t.Setenv(config.EnvCircleToken, "fake-token")

	gitRoot := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	workDir := filepath.Join(gitRoot, "sub")
	assert.NilError(t, os.MkdirAll(workDir, 0o755))
	t.Chdir(workDir)
	assert.NilError(t, config.SaveProjectConfig(workDir, &config.ProjectConfig{
		OrgID:    "org-1",
		Commands: []config.Command{{Name: "test", Run: "true", Remote: true}},
	}))

	var outBuf, errBuf lockedBuf
	root := newTestRootCmd()
	root.SetOut(&outBuf)
	root.SetErr(&errBuf)
	root.SetArgs([]string{"validate", "--project", workDir})
	assert.NilError(t, root.Execute(), "stderr: %s", errBuf.String())

	canonical, err := filepath.EvalSymlinks(gitRoot)
	assert.NilError(t, err)

	select {
	case reg := <-regs:
		assert.Equal(t, config.CanonicalProjectRoot(reg.ProjectRoot), canonical,
			"the command was registered under a root the daemon does not track")
	case <-time.After(5 * time.Second):
		t.Fatal("the remote validation command was not registered")
	}
}

// lockedBuf stands in for the process stderr a run shares with the pool's
// background sync. Both write concurrently, which a file tolerates and a plain
// bytes.Buffer does not.
type lockedBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
