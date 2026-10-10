package docker

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// testImage is small and ships a POSIX sh, ls and cat, which is all these
// tests exec inside the container. The transport (rsync, git init) runs on the
// host, so the image needs no git or rsync of its own.
const testImage = "alpine:3.20"

const repoPath = "/home/user/repo"

func noStatus(iostream.Level, string) {}

// newLiveBackend connects to the local Docker daemon, skipping the test when
// one is not reachable so the suite stays green on machines without Docker. Its
// workspaces live under a temp dir and it runs the small test image.
func newLiveBackend(t *testing.T) *Provider {
	t.Helper()
	if _, err := exec.LookPath("rsync"); err != nil {
		t.Skip("rsync is not installed")
	}
	b, err := New()
	if err != nil {
		t.Skipf("no docker client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.cli.Ping(ctx); err != nil {
		t.Skipf("docker daemon not reachable: %v", err)
	}
	b.baseImage = testImage
	b.workRoot = t.TempDir()
	return b
}

func TestExecStreamsOutputAndExitCode(t *testing.T) {
	b := newLiveBackend(t)
	inst, err := b.Create(context.Background(), "chunk-test-exec", "", repoPath)
	assert.NilError(t, err)
	defer func() { _ = b.Delete(context.Background(), inst.ID) }()

	var stdout, stderr strings.Builder
	onOutput := func(stream string, data []byte) {
		switch stream {
		case iostream.StreamStdout:
			stdout.Write(data)
		case iostream.StreamStderr:
			stderr.Write(data)
		}
	}
	var submitted string
	code, err := b.Exec(context.Background(), inst.ID,
		"echo out-line; echo err-line 1>&2; exit 3",
		nil, onOutput, func(s string) { submitted = s })
	assert.NilError(t, err)
	assert.Equal(t, code, 3)
	assert.Equal(t, strings.TrimSpace(stdout.String()), "out-line")
	assert.Equal(t, strings.TrimSpace(stderr.String()), "err-line")
	assert.Assert(t, submitted != "")
}

func TestExecPassesEnv(t *testing.T) {
	b := newLiveBackend(t)
	inst, err := b.Create(context.Background(), "chunk-test-env", "", repoPath)
	assert.NilError(t, err)
	defer func() { _ = b.Delete(context.Background(), inst.ID) }()

	var stdout strings.Builder
	code, err := b.Exec(context.Background(), inst.ID, `printf '%s' "$CHUNK_TEST_VAR"`,
		map[string]string{"CHUNK_TEST_VAR": "hello-env"},
		func(stream string, data []byte) {
			if stream == iostream.StreamStdout {
				stdout.Write(data)
			}
		}, nil)
	assert.NilError(t, err)
	assert.Equal(t, code, 0)
	assert.Equal(t, stdout.String(), "hello-env")
}

// TestSyncSnapshotCloneAndPull exercises the whole provisioning + transport
// path the pool drives: sync a tree in, snapshot it, create a clone from the
// snapshot, confirm the clone has the files, then pull a container's workspace
// back out.
func TestSyncSnapshotCloneAndPull(t *testing.T) {
	b := newLiveBackend(t)
	ctx := context.Background()

	// A local source tree with a tracked file and a gitignored one.
	srcTree := t.TempDir()
	write(t, filepath.Join(srcTree, "main.go"), "package main\n")
	write(t, filepath.Join(srcTree, ".gitignore"), "ignored.txt\n")
	write(t, filepath.Join(srcTree, "ignored.txt"), "do not sync\n")

	seed, err := b.Create(ctx, "chunk-test-seed", "", repoPath)
	assert.NilError(t, err)
	defer func() { _ = b.Delete(ctx, seed.ID) }()

	assert.NilError(t, b.Sync(ctx, seed.ID, repoPath, srcTree, true, noStatus))

	// The tracked file landed in the bind-mounted host dir; the ignored one did
	// not; and Sync initialised a git repo.
	assert.Equal(t, readFile(t, filepath.Join(seed.HostDir, "main.go")), "package main\n")
	assert.Assert(t, !exists(filepath.Join(seed.HostDir, "ignored.txt")), "gitignored file should not sync")
	assert.Assert(t, exists(filepath.Join(seed.HostDir, ".git")), "git repo should be initialised")

	// The container sees the file at repoPath through the bind mount.
	var catOut strings.Builder
	code, err := b.Exec(ctx, seed.ID, "cat "+repoPath+"/main.go", nil,
		func(stream string, data []byte) {
			if stream == iostream.StreamStdout {
				catOut.Write(data)
			}
		}, nil)
	assert.NilError(t, err)
	assert.Equal(t, code, 0)
	assert.Equal(t, catOut.String(), "package main\n")

	// Snapshot the seed and clone from it: the clone's workspace has the files.
	snap, err := b.Snapshot(ctx, seed.ID, "chunk-test-snap")
	assert.NilError(t, err)
	assert.Assert(t, strings.HasPrefix(snap, snapshotScheme))

	clone, err := b.Create(ctx, "chunk-test-clone", snap, repoPath)
	assert.NilError(t, err)
	defer func() { _ = b.Delete(ctx, clone.ID) }()
	assert.Equal(t, readFile(t, filepath.Join(clone.HostDir, "main.go")), "package main\n")
	assert.Assert(t, exists(filepath.Join(clone.HostDir, ".git")), "clone should carry the snapshot's .git")

	// Pull the seed's workspace back out: the tracked file comes, .git does not.
	dest := t.TempDir()
	assert.NilError(t, b.Pull(ctx, seed.ID, repoPath, dest, noStatus))
	assert.Equal(t, readFile(t, filepath.Join(dest, "main.go")), "package main\n")
	assert.Assert(t, !exists(filepath.Join(dest, ".git")), "pull must not bring back .git")
}

func TestDeleteRemovesWorkspace(t *testing.T) {
	b := newLiveBackend(t)
	ctx := context.Background()
	inst, err := b.Create(ctx, "chunk-test-del", "", repoPath)
	assert.NilError(t, err)
	assert.Assert(t, exists(inst.HostDir))
	assert.NilError(t, b.Delete(ctx, inst.ID))
	assert.Assert(t, !exists(inst.HostDir), "Delete should remove the host workspace")
}

func write(t *testing.T, path, content string) {
	t.Helper()
	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o777))
	assert.NilError(t, os.WriteFile(path, []byte(content), 0o644))
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	return string(data)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
