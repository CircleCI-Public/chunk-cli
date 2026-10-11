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
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"

	"github.com/smeltdev/smelt/internal/brand"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/sidecar"
)

// defaultBaseImage is the image a member runs when none is configured. The real
// factory image also carries git, the agent CLI, and the project's toolchain;
// this default at least provides a shell and git on most tags.
const defaultBaseImage = "debian:bookworm-slim"

// snapshotScheme marks a Snapshot ref as a smelt host-directory snapshot rather
// than a Docker image, so Create knows to restore from a directory.
const snapshotScheme = brand.Command + "fs:"

// EnvImage names the environment variable that overrides the provider's default
// image. `smelt factory build` reads it too, to know an image is configured.
const EnvImage = brand.EnvPrefix + "DOCKER_IMAGE"

// label marks every container smelt creates, so they can be told apart from the
// user's own and reaped.
const label = brand.LabelPrefix + "factory"

// labelName carries the name the pool gave a member, since the container's
// own name has a uniqueness suffix appended.
const labelName = brand.LabelPrefix + "name"

// inspectAttempts bounds image lookups that fail for a reason other than the
// image being absent. notFoundAttempts bounds lookups that answer "no such
// image": Docker Desktop's API proxy answers that while the engine is still
// starting after a Resource Saver shutdown, before its image store is loaded,
// so one "not found" is not trusted. The engine can take half a minute to
// come back, so the lookups are spread over about 45s: the delay between them
// grows by inspectBackoff up to maxInspectBackoff. inspectBackoff is a
// variable so tests need not wait.
const (
	inspectAttempts   = 12
	notFoundAttempts  = 12
	maxInspectBackoff = 5
)

var inspectBackoff = time.Second

// Provider runs sidecars as local Docker containers with bind-mounted workspaces.
type Provider struct {
	cli       client.APIClient
	baseImage string
	// workRoot is where per-member workspace directories and snapshots live.
	workRoot string

	mu      sync.Mutex
	members map[string]member // by container ID
}

// member is a container the provider created: its bind-mounted workspace on
// the host, and the image it runs. A snapshot of a member records that image so
// clones restored from it run the same image, not the provider's default.
type member struct {
	hostDir string
	image   string
}

// Layout of a snapshot directory: the workspace copy, and the image it ran on.
const (
	snapshotWorkspaceDir = "workspace"
	snapshotImageFile    = "image"
)

var _ sidecar.Backend = (*Provider)(nil)

// NewClient connects to the local Docker daemon using the standard environment
// (DOCKER_HOST and friends), negotiating the API version with the daemon. It is
// the one place a Docker client is constructed, shared with image builds.
func NewClient() (*client.Client, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("docker: connect to daemon: %w", err)
	}
	return cli, nil
}

// New connects to the local Docker daemon using the standard environment
// (DOCKER_HOST and friends), negotiating the API version with the daemon.
func New() (*Provider, error) {
	cli, err := NewClient()
	if err != nil {
		return nil, err
	}
	b := &Provider{cli: cli, baseImage: defaultBaseImage, members: map[string]member{}}
	if img := os.Getenv(EnvImage); img != "" {
		b.baseImage = img
	}
	root := os.Getenv(brand.Env("DOCKER_WORKROOT"))
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			cache = os.TempDir()
		}
		root = filepath.Join(cache, brand.AppDir, "docker")
	}
	b.workRoot = root
	return b, nil
}

// NewWithClient builds a Provider around an existing Docker client, for tests.
func NewWithClient(cli client.APIClient, baseImage, workRoot string) *Provider {
	return &Provider{cli: cli, baseImage: baseImage, workRoot: workRoot, members: map[string]member{}}
}

// Close releases the Docker client's resources.
func (b *Provider) Close() error {
	if c, ok := b.cli.(interface{ Close() error }); ok {
		return c.Close()
	}
	return nil
}

func (b *Provider) remember(id string, m member) {
	b.mu.Lock()
	b.members[id] = m
	b.mu.Unlock()
}

func (b *Provider) member(id string) (member, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, ok := b.members[id]
	return m, ok
}

func (b *Provider) hostDir(id string) (string, bool) {
	m, ok := b.member(id)
	return m.hostDir, ok
}

// Create provisions a container whose workspace at repoPath is a bind-mounted
// host directory. image selects the source: empty or a Docker reference starts
// a fresh container from that image; a smeltfs snapshot ref restores the
// snapshot's workspace into the new host directory (and runs the base image).
func (b *Provider) Create(ctx context.Context, name, image, repoPath string) (sidecar.Instance, error) {
	if repoPath == "" {
		return sidecar.Instance{}, errors.New("docker: create needs a repoPath")
	}
	// The pool may reuse a name: it names the seed and the first clone made from
	// its snapshot alike, which CircleCI allows and Docker does not. A suffix
	// keeps container names and workspace directories unique per Create; the
	// pool's own name is kept as a label.
	suffix, err := randomSuffix()
	if err != nil {
		return sidecar.Instance{}, fmt.Errorf("docker: name container: %w", err)
	}
	containerName := name + "-" + suffix
	hostDir := filepath.Join(b.workRoot, containerName)
	if err := os.MkdirAll(hostDir, 0o777); err != nil {
		return sidecar.Instance{}, fmt.Errorf("docker: create workspace dir: %w", err)
	}

	containerImage := b.baseImage
	switch {
	case strings.HasPrefix(image, snapshotScheme):
		// Clone: run the image the snapshot's seed ran, and restore the
		// snapshot's workspace into this member's host dir.
		snapDir := strings.TrimPrefix(image, snapshotScheme)
		recorded, err := os.ReadFile(filepath.Join(snapDir, snapshotImageFile))
		if err != nil {
			_ = os.RemoveAll(hostDir)
			return sidecar.Instance{}, fmt.Errorf("docker: read snapshot image: %w", err)
		}
		containerImage = strings.TrimSpace(string(recorded))
		if err := copyTree(ctx, filepath.Join(snapDir, snapshotWorkspaceDir), hostDir); err != nil {
			_ = os.RemoveAll(hostDir)
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
		Labels:     map[string]string{label: "1", labelName: name},
	}, &container.HostConfig{
		Binds: []string{hostDir + ":" + repoPath},
	}, nil, nil, containerName)
	if err != nil {
		_ = os.RemoveAll(hostDir)
		return sidecar.Instance{}, fmt.Errorf("docker: create container: %w", err)
	}
	if err := b.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		_ = b.cli.ContainerRemove(context.WithoutCancel(ctx), created.ID, container.RemoveOptions{Force: true})
		_ = os.RemoveAll(hostDir)
		return sidecar.Instance{}, fmt.Errorf("docker: start container: %w", err)
	}

	b.remember(created.ID, member{hostDir: hostDir, image: containerImage})
	return sidecar.Instance{ID: created.ID, HostDir: hostDir, RepoPath: repoPath}, nil
}

// ensureImage makes sure ref is on the Docker host, pulling it only when the
// host keeps saying it has no such image. Lookups are retried briefly first:
// Docker Desktop can answer the first request after a Resource Saver shutdown
// with an error, or with "no such image" while its engine is still starting.
// Any other error is reported as itself, rather than turned into a pull that
// would bury it under a misleading registry error for a locally built image.
func (b *Provider) ensureImage(ctx context.Context, ref string) error {
	var err error
	for attempt := 1; ; attempt++ {
		_, err = b.cli.ImageInspect(ctx, ref)
		if err == nil {
			return nil
		}
		limit := inspectAttempts
		if cerrdefs.IsNotFound(err) {
			limit = notFoundAttempts
		}
		if attempt >= limit {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(min(attempt, maxInspectBackoff)) * inspectBackoff):
		}
	}
	if !cerrdefs.IsNotFound(err) {
		// Not "no such image": pulling would only bury the real error under a
		// registry failure for an image that may exist only locally.
		return fmt.Errorf("docker: look up image %s: %w", ref, err)
	}
	rc, err := b.cli.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("docker: image %s is not on this Docker host and could not be pulled; "+
			"if it is a local image, build it first (docker build -t %s -f Dockerfile.local .): %w", ref, ref, err)
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
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("docker: remove container: %w", err)
	}
	if dir, ok := b.hostDir(id); ok {
		_ = os.RemoveAll(dir)
		b.mu.Lock()
		delete(b.members, id)
		b.mu.Unlock()
	}
	return nil
}

// Snapshot copies the member's workspace aside, records the image it runs, and
// returns a smeltfs ref to the result. It is independent of the seed
// container's lifecycle, so a later Delete of the seed does not disturb clones
// restoring from it.
func (b *Provider) Snapshot(ctx context.Context, id, name string) (string, error) {
	m, ok := b.member(id)
	if !ok {
		return "", fmt.Errorf("docker: snapshot: unknown container %s", id)
	}
	snapDir := filepath.Join(b.workRoot, "snapshots", name)
	if err := os.MkdirAll(snapDir, 0o777); err != nil {
		return "", fmt.Errorf("docker: snapshot dir: %w", err)
	}
	// The snapshot keeps .git so clones carry the baseline commit and history.
	if err := copyTree(ctx, m.hostDir, filepath.Join(snapDir, snapshotWorkspaceDir)); err != nil {
		return "", fmt.Errorf("docker: snapshot copy: %w", err)
	}
	if err := os.WriteFile(filepath.Join(snapDir, snapshotImageFile), []byte(m.image+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("docker: snapshot image: %w", err)
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
func copyTree(ctx context.Context, src, dst string) error {
	if err := os.MkdirAll(dst, 0o777); err != nil {
		return fmt.Errorf("mkdir %s: %w", dst, err)
	}
	cmd := exec.CommandContext(ctx, "rsync", "--archive", "--delete", strings.TrimRight(src, "/")+"/", strings.TrimRight(dst, "/")+"/")
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

// randomSuffix returns a short random hex string for unique container names.
func randomSuffix() (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
