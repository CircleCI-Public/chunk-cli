package cmd

import (
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/ui/watch"
)

// Paths given to a remote watch are paths on the daemon's host, so they are
// cleaned as text and never resolved against this machine's filesystem.
func TestRemoteProjectsKeepsDaemonHostPathsVerbatim(t *testing.T) {
	entries := remoteProjects([]string{"/srv/work/repo/", "/srv/work/../work/other"})

	assert.DeepEqual(t, entries, []watch.ProjectEntry{
		{ProjectRoot: "/srv/work/repo"},
		{ProjectRoot: "/srv/work/other"},
	})
}

func TestRunRemoteWatchRequiresAToken(t *testing.T) {
	t.Setenv("CHUNK_DAEMON_REMOTE_ADDR", "127.0.0.1:1")
	t.Setenv("CHUNK_DAEMON_TCP_TOKEN", "")

	err := runRemoteWatch(newWatchCmd(), false, nil)
	assert.ErrorContains(t, err, "CHUNK_DAEMON_TCP_TOKEN")
}

func TestRunRemoteWatchFocusNeedsARemotePath(t *testing.T) {
	t.Setenv("CHUNK_DAEMON_REMOTE_ADDR", "127.0.0.1:1")
	t.Setenv("CHUNK_DAEMON_TCP_TOKEN", "tok")

	err := runRemoteWatch(newWatchCmd(), true, nil)
	assert.ErrorContains(t, err, "--focus needs at least one path")
}
