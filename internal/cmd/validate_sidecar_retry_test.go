package cmd

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// syncErrEnv gives a project rooted in a temp dir with its own state directory,
// so pruning is observable without touching the developer's real state.
func syncErrEnv(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	stateDir, err := sidecar.StateDir()
	assert.NilError(t, err)
	assert.NilError(t, os.MkdirAll(stateDir, 0o755))
	return stateDir
}

func seedState(t *testing.T, id string) string {
	t.Helper()
	ctx := context.Background()
	assert.NilError(t, sidecar.SaveActive(ctx, sidecar.ActiveSidecar{SidecarID: id}))
	stateDir, err := sidecar.StateDir()
	assert.NilError(t, err)
	return filepath.Join(stateDir, "sidecar.json")
}

func statePresent(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// TestSidecarSyncErrorSignalsRetryWhenGone covers a sidecar deleted between the
// reap's listing and the sync: state is dropped and the caller is told a fresh
// sidecar will fix it, rather than being asked to run the command again.
func TestSidecarSyncErrorSignalsRetryWhenGone(t *testing.T) {
	syncErrEnv(t)
	path := seedState(t, "sb-gone")

	syncErr := &circleci.StatusError{Op: "add ssh key", StatusCode: http.StatusNotFound}
	err := sidecarSyncError(context.Background(), nil, "sb-gone", syncErr, iostream.Streams{Err: os.Stderr})

	assert.Assert(t, errors.Is(err, errSidecarUnusable), "a gone sidecar must be retryable, got: %v", err)
	assert.Assert(t, !statePresent(t, path), "state for a gone sidecar must be dropped")
}

// TestSidecarSyncErrorSignalsRetryWhenOutOfDate covers the 410 no listing can
// reveal. The error stays unwrappable to the StatusError so GoneError can still
// phrase it if the retry also fails.
func TestSidecarSyncErrorSignalsRetryWhenOutOfDate(t *testing.T) {
	syncErrEnv(t)
	path := seedState(t, "sb-old")

	syncErr := &circleci.StatusError{
		Op:            "exec",
		StatusCode:    http.StatusGone,
		ServerMessage: "This sidecar is out of date, recreate it",
	}
	err := sidecarSyncError(context.Background(), nil, "sb-old", syncErr, iostream.Streams{Err: os.Stderr})

	assert.Assert(t, errors.Is(err, errSidecarUnusable), "an out-of-date sidecar must be retryable")
	assert.Assert(t, !statePresent(t, path), "state for an out-of-date sidecar must be dropped")
	assert.Assert(t, circleci.SidecarOutOfDate(err), "the 410 must stay reachable through the wrapping")
	assert.Assert(t, GoneError(err) != nil, "GoneError must still phrase the wrapped 410")
}

// TestSidecarSyncErrorKeepsStateOnOrdinaryFailure covers a sync that failed for
// reasons that say nothing about the sidecar, such as the network. Replacing it
// would destroy a working sidecar over a transient fault.
func TestSidecarSyncErrorKeepsStateOnOrdinaryFailure(t *testing.T) {
	syncErrEnv(t)
	path := seedState(t, "sb-fine")

	err := sidecarSyncError(context.Background(), nil, "sb-fine",
		errors.New("dial tcp: connection refused"), iostream.Streams{Err: os.Stderr})

	assert.Assert(t, !errors.Is(err, errSidecarUnusable), "a transient fault must not replace the sidecar")
	assert.Assert(t, statePresent(t, path), "state must survive a fault unrelated to the sidecar")
}
