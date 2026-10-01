package watchd

import (
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

const remoteToken = "remote-secret"

// startRemoteDaemon runs a real daemon on a TCP listener with one registered
// project and points this process's client at it, exactly as a viewer on another
// machine would be configured. It returns the project's canonical root.
func startRemoteDaemon(t *testing.T) (addr, root string) {
	t.Helper()
	projectDir := initRepo(t)
	canonical, err := filepath.EvalSymlinks(projectDir)
	assert.NilError(t, err)

	addr = startTestDaemonTCPWith(t, remoteToken, func() {
		dataDir, err := config.ProjectDataDir(projectDir)
		assert.NilError(t, err)
		assert.NilError(t, sidecar.RegisterProjectRoot(dataDir, projectDir))
	})
	t.Setenv("CHUNK_WATCHD_REMOTE_ADDR", addr)
	t.Setenv("CHUNK_WATCHD_TCP_TOKEN", remoteToken)
	return addr, canonical
}

func TestRemoteSnapshotReturnsTheDaemonsProjects(t *testing.T) {
	_, root := startRemoteDaemon(t)

	snap, err := FetchSnapshot(nil)
	assert.NilError(t, err)
	assert.Equal(t, len(snap.Projects), 1)
	assert.Equal(t, snap.Projects[0].Root, root)
	assert.Equal(t, snap.Projects[0].Branch, "main")
}

// A remote viewer can only spell a project as it appears on the daemon's host,
// which need not be the spelling the daemon keys it by.
func TestRemoteSnapshotFilterMatchesASymlinkedSpelling(t *testing.T) {
	_, root := startRemoteDaemon(t)
	link := filepath.Join(t.TempDir(), "link")
	assert.NilError(t, os.Symlink(root, link))

	snap, err := FetchSnapshot([]string{link})
	assert.NilError(t, err)
	assert.Equal(t, len(snap.Projects), 1, "the symlinked spelling should match the tracked project")
	assert.Equal(t, snap.Projects[0].Root, root)
}

func TestRemoteSnapshotWithWrongTokenIsUnauthorized(t *testing.T) {
	startRemoteDaemon(t)
	t.Setenv("CHUNK_WATCHD_TCP_TOKEN", "not-the-token")

	_, err := FetchSnapshot(nil)
	assert.Assert(t, errors.Is(err, ErrUnauthorized), "got %v", err)
	assert.ErrorContains(t, err, "CHUNK_WATCHD_TCP_TOKEN")
}

func TestRemoteSnapshotUnreachableNamesTheAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	assert.NilError(t, err)
	addr := ln.Addr().String()
	assert.NilError(t, ln.Close())
	t.Setenv("CHUNK_WATCHD_REMOTE_ADDR", addr)
	t.Setenv("CHUNK_WATCHD_TCP_TOKEN", remoteToken)

	_, err = FetchSnapshot(nil)
	assert.ErrorContains(t, err, addr)
	assert.ErrorContains(t, err, "unreachable")
}
