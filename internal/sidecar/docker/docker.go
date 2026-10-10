// Package docker implements sidecar.Backend on local Docker containers, so a
// factory run needs no CircleCI cloud sidecars.
//
// A member's workspace is a host directory bind-mounted into the container at
// its repoPath, so Sync and Pull are plain local rsync between host directories
// — no SSH tunnel. Because Docker's ContainerCommit does not capture bind-mount
// contents, Snapshot copies the seed's workspace directory aside and clones
// restore it, rather than committing an image.
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// defaultBaseImage is the image a member runs when none is configured. The real
// factory image also carries git, the agent CLI, and the project's toolchain;
// this default at least provides a shell and git on most tags.
const defaultBaseImage = "debian:bookworm-slim"

// snapshotScheme marks a Snapshot ref as a chunk host-directory snapshot rather
// than a Docker image, so Create knows to restore from a directory.
const snapshotScheme = "chunkfs:"

// label marks every container chunk creates, so they can be told apart from the
// user's own and reaped.
const label = "com.circleci.chunk.factory"

// Provider runs sidecars as local Docker containers with bind-mounted workspaces.
type Provider struct {
	cli       client.APIClient
	baseImage string
	// workRoot is where per-member workspace directories and snapshots live.
	workRoot string

	mu     sync.Mutex
	mounts map[string]string // container ID -> host workspace dir
}

var _ sidecar.Backend = (*Provider)(nil)

// New connects to the local Docker daemon using the standard environment
// (DOCKER_HOST and friends), negotiating the API version with the daemon.
func New() (*Provider, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker: connect to daemon: %w", err)
	}
	b := &Provider{cli: cli, baseImage: defaultBaseImage, mounts: map[string]string{}}
	if img := os.Getenv("CHUNK_DOCKER_IMAGE"); img != "" {
		b.baseImage = img
	}
	root := os.Getenv("CHUNK_DOCKER_WORKROOT")
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			cache = os.TempDir()
		}
		root = filepath.Join(cache, "chunk", "docker")
	}
	b.workRoot = root
	return b, nil
}

// NewWithClient builds a Provider around an existing Docker client, for tests.
func NewWithClient(cli client.APIClient, baseImage, workRoot string) *Provider {
	return &Provider{cli: cli, baseImage: baseImage, workRoot: workRoot, mounts: map[string]string{}}
}

// Close releases the Docker client's resources.
func (b *Provider) Close() error {
	if c, ok := b.cli.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

func (b *Provider) putMount(id, hostDir string) {
	b.mu.Lock()
	b.mounts[id] = hostDir
	b.mu.Unlock()
}

func (b *Provider) hostDir(id string) (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.mounts[id]
	return d, ok
}

// Create provisions a container whose workspace at repoPath is a bind-mounted
// host directory. image selects the source: empty or a Docker reference starts
// a fresh container from that image; a chunkfs snapshot ref restores the
// snapshot's workspace into the new host directory (and runs the base image).
func (b *Provider) Create(ctx context.Context, name, image, repoPath string) (sidecar.Instance, error) {
	if repoPath == "" {
		return sidecar.Instance{}, errors.New("docker: create needs a repoPath")
	}
	hostDir := filepath.Join(b.workRoot, name)
	if err := os.MkdirAll(hostDir, 0o777); err != nil {
		return sidecar.Instance{}, fmt.Errorf("docker: create workspace dir: %w", err)
	}

	containerImage := b.baseImage
	switch {
	case strings.HasPrefix(image, snapshotScheme):
		// Clone: restore the snapshot's workspace into this member's host dir.
		snapDir := strings.TrimPrefix(image, snapshotScheme)
		if err := copyTree(snapDir, hostDir); err != nil {
			return sidecar.Instance{}, fmt.Errorf("docker: restore snapshot: %w", err)
		}
	case image != "":
		containerImage = image
	}

	if err := b.ensureImage(ctx, containerImage); err != nil {
		_ = os.RemoveAll(hostDir)
		return sidecar.Instance{}, err
	}

	created, err := b.cli.ContainerCreate(ctx, &container.Config{
		Image:      containerImage,
		Cmd:        []string{"sleep", "infinity"},
		WorkingDir: repoPath,
		Labels:     map[string]string{label: "1"},
	}, &container.HostConfig{
		Binds: []string{hostDir + ":" + repoPath},
	}, nil, nil, name)
	if err != nil {
		_ = os.RemoveAll(hostDir)
		return sidecar.Instance{}, fmt.Errorf("docker: create container: %w", err)
	}
	if err := b.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		_ = b.cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
		_ = os.RemoveAll(hostDir)
		return sidecar.Instance{}, fmt.Errorf("docker: start container: %w", err)
	}

	b.putMount(created.ID, hostDir)
	return sidecar.Instance{ID: created.ID, HostDir: hostDir, RepoPath: repoPath}, nil
}

// ensureImage pulls ref if it is not already present locally.
func (b *Provider) ensureImage(ctx context.Context, ref string) error {
	if _, err := b.cli.ImageInspect(ctx, ref); err == nil {
		return nil
	}
	rc, err := b.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("docker: pull image %s: %w", ref, err)
	}
	defer func() { _ = rc.Close() }()
	// Draining the stream is what waits for the pull to finish.
	if _, err := io.Copy(io.Discard, rc); err != nil {
		return fmt.Errorf("docker: pull image %s: %w", ref, err)
	}
	return nil
}

// Delete removes the container and its host workspace directory. Removing one
// that is already gone is not an error.
func (b *Provider) Delete(ctx context.Context, id string) error {
	err := b.cli.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
	if err != nil && !client.IsErrNotFound(err) {
		return fmt.Errorf("docker: remove container: %w", err)
	}
	if dir, ok := b.hostDir(id); ok {
		_ = os.RemoveAll(dir)
		b.mu.Lock()
		delete(b.mounts, id)
		b.mu.Unlock()
	}
	return nil
}

// Snapshot copies the member's workspace aside and returns a chunkfs ref to it.
// It is independent of the seed container's lifecycle, so a later Delete of the
// seed does not disturb clones restoring from it.
func (b *Provider) Snapshot(ctx context.Context, id, name string) (string, error) {
	src, ok := b.hostDir(id)
	if !ok {
		return "", fmt.Errorf("docker: snapshot: unknown container %s", id)
	}
	snapDir := filepath.Join(b.workRoot, "snapshots", name)
	if err := os.MkdirAll(filepath.Dir(snapDir), 0o777); err != nil {
		return "", fmt.Errorf("docker: snapshot dir: %w", err)
	}
	// The snapshot keeps .git so clones carry the baseline commit and history.
	if err := copyTree(src, snapDir); err != nil {
		return "", fmt.Errorf("docker: snapshot copy: %w", err)
	}
	return snapshotScheme + snapDir, nil
}

// IsStale is always false: local containers do not go stale the way remote
// sidecars do.
func (b *Provider) IsStale(context.Context, string) bool { return false }

// Sync mirrors the local tree at cwd into the member's bind-mounted workspace
// with rsync (honoring .gitignore, deleting extraneous files), then ensures a
// git repo exists there so the baseline commit and `git diff HEAD` work. retry
// is unused: a local container is reachable as soon as it is created.
func (b *Provider) Sync(ctx context.Context, id, repoPath, cwd string, _ bool, status iostream.StatusFunc) error {
	dir, ok := b.hostDir(id)
	if !ok {
		return fmt.Errorf("docker: sync: unknown container %s", id)
	}
	status(iostream.LevelInfo, fmt.Sprintf("Syncing workspace %s...", repoPath))
	// Mirror cwd -> hostDir. The worktree's own .git is excluded; a fresh git
	// repo is initialised below, matching the CircleCI worktree sync.
	if err := rsyncMirror(ctx, cwd, dir, "--exclude=/.git", "--filter=:- .gitignore"); err != nil {
		return fmt.Errorf("docker: sync: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); errors.Is(err, os.ErrNotExist) {
		if out, err := exec.CommandContext(ctx, "git", "-C", dir, "init", "-q").CombinedOutput(); err != nil {
			return fmt.Errorf("docker: sync: git init: %w: %s", err, strings.TrimSpace(string(out)))
		}
	}
	status(iostream.LevelDone, "Synced")
	return nil
}

// Pull mirrors the member's workspace back into localDir with rsync, excluding
// every .git and files git ignores, matching the CircleCI pull's intent.
func (b *Provider) Pull(ctx context.Context, id, repoPath, localDir string, status iostream.StatusFunc) error {
	dir, ok := b.hostDir(id)
	if !ok {
		return fmt.Errorf("docker: pull: unknown container %s", id)
	}
	status(iostream.LevelInfo, fmt.Sprintf("Pulling workspace %s...", repoPath))
	if err := rsyncMirror(ctx, dir, localDir, "--exclude=.git", "--filter=:- .gitignore"); err != nil {
		return fmt.Errorf("docker: pull: %w", err)
	}
	status(iostream.LevelDone, "Pulled")
	return nil
}

// Exec runs script with `sh -c` inside container id and streams its stdout and
// stderr to onOutput, returning the command's exit code. onSubmitted, when
// non-nil, receives the Docker exec ID between creation and streaming.
func (b *Provider) Exec(ctx context.Context, id, script string, env map[string]string, onOutput iostream.OutputFn, onSubmitted func(string)) (int, error) {
	created, err := b.cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          []string{"sh", "-c", script},
		Env:          envSlice(env),
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return 0, fmt.Errorf("docker exec create: %w", err)
	}
	if onSubmitted != nil {
		onSubmitted(created.ID)
	}

	att, err := b.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return 0, fmt.Errorf("docker exec attach: %w", err)
	}
	defer att.Close()

	out := streamWriter{stream: iostream.StreamStdout, fn: onOutput}
	errW := streamWriter{stream: iostream.StreamStderr, fn: onOutput}
	if _, err := stdcopy.StdCopy(out, errW, att.Reader); err != nil && ctx.Err() == nil {
		return 0, fmt.Errorf("docker exec stream: %w", err)
	}

	insp, err := b.cli.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return 0, fmt.Errorf("docker exec inspect: %w", err)
	}
	return insp.ExitCode, nil
}

// rsyncMirror makes dst an exact copy of src (archive mode, deleting extraneous
// files) plus the given extra filters. Trailing slashes make rsync copy src's
// contents into dst rather than nesting a directory.
func rsyncMirror(ctx context.Context, src, dst string, extra ...string) error {
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}
	args := append([]string{"--archive", "--delete"}, extra...)
	args = append(args, strings.TrimRight(src, "/")+"/", strings.TrimRight(dst, "/")+"/")
	cmd := exec.CommandContext(ctx, "rsync", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rsync: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// copyTree copies src into dst exactly, including .git, with rsync.
func copyTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}
	cmd := exec.Command("rsync", "--archive", "--delete", strings.TrimRight(src, "/")+"/", strings.TrimRight(dst, "/")+"/")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("rsync copy: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// envSlice turns an environment map into the "KEY=VALUE" slice Docker wants.
func envSlice(env map[string]string) []string {
	if len(env) == 0 {
		return nil
	}
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

// streamWriter forwards each write to an OutputFn under a fixed stream label.
// It copies the buffer because stdcopy reuses its scratch space between writes.
type streamWriter struct {
	stream string
	fn     iostream.OutputFn
}

func (w streamWriter) Write(p []byte) (int, error) {
	if w.fn != nil {
		w.fn(w.stream, append([]byte(nil), p...))
	}
	return len(p), nil
}
