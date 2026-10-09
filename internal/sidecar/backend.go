package sidecar

import (
	"context"
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// Backend is the provider a pool runs its sidecars on: it creates, syncs,
// execs on, snapshots, and deletes them. CircleCI microVMs are one
// implementation (circleciBackend); a local Docker backend is another. The
// pool and the factory loop depend only on this interface, so swapping where
// the work runs does not touch them.
type Backend interface {
	// Create provisions a sidecar named name from image (a snapshot/image ID,
	// or empty for the backend's default) and returns it ready to sync to.
	Create(ctx context.Context, name, image string) (Instance, error)
	// Delete removes a sidecar. Deleting one that is already gone is not an
	// error.
	Delete(ctx context.Context, id string) error
	// Snapshot captures id as a reusable image and returns the image's ID.
	Snapshot(ctx context.Context, id, name string) (image string, err error)
	// IsStale reports whether id is known to be unusable (e.g. reaped
	// server-side). A backend whose instances never go stale returns false.
	IsStale(ctx context.Context, id string) bool
	// Sync mirrors the local tree at cwd to the sidecar's workspace at
	// repoPath. retry tolerates a sidecar that was only just created and is not
	// yet reachable.
	Sync(ctx context.Context, id, repoPath, cwd string, retry bool, status iostream.StatusFunc) error
	// Pull mirrors the sidecar's workspace at repoPath into localDir.
	Pull(ctx context.Context, id, repoPath, localDir string, status iostream.StatusFunc) error
	// Exec runs a shell script on the sidecar and streams its output.
	// onSubmitted, when non-nil, is called with the command's ID between
	// submission and streaming.
	Exec(ctx context.Context, id, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(commandID string)) (exitCode int, err error)
}

// Instance is a sidecar a Backend provisioned. ID identifies it to the
// backend. HostDir and RepoPath are set by backends whose workspace is a local
// directory (Docker); they are empty for remote backends (CircleCI), where the
// pool supplies the workspace path.
type Instance struct {
	ID       string
	HostDir  string
	RepoPath string
}

// circleciBackend runs sidecars as CircleCI microVMs. It wraps a circleci
// client and an org, and delegates to the package's existing sidecar
// operations so the transport (rsync over the SSH-over-WebSocket tunnel) and
// lifecycle are unchanged.
type circleciBackend struct {
	client *circleci.Client
	orgID  string
}

// NewCircleCIBackend returns a Backend backed by client and orgID.
func NewCircleCIBackend(client *circleci.Client, orgID string) Backend {
	return &circleciBackend{client: client, orgID: orgID}
}

// CircleCIClientOf returns the CircleCI client a Backend wraps when it is a
// CircleCI backend. It is an escape hatch for the few code paths that still
// drop to the CircleCI API directly — notably the Target-based `validate
// --remote` runner — and returns (nil, false) for any other backend.
func CircleCIClientOf(b Backend) (*circleci.Client, bool) {
	cb, ok := b.(*circleciBackend)
	if !ok {
		return nil, false
	}
	return cb.client, true
}

func (b *circleciBackend) Create(ctx context.Context, name, image string) (Instance, error) {
	sc, err := Create(ctx, b.client, b.orgID, name, image)
	if err != nil {
		return Instance{}, err
	}
	return Instance{ID: sc.ID}, nil
}

func (b *circleciBackend) Delete(ctx context.Context, id string) error {
	return b.client.DeleteSidecar(ctx, id)
}

func (b *circleciBackend) Snapshot(ctx context.Context, id, name string) (string, error) {
	snap, err := b.client.CreateSnapshot(ctx, id, name)
	if err != nil {
		return "", err
	}
	return snap.ID, nil
}

func (b *circleciBackend) IsStale(ctx context.Context, id string) bool {
	return IsDefinitelyStale(ctx, b.client, id)
}

func (b *circleciBackend) Sync(ctx context.Context, id, repoPath, cwd string, retry bool, status iostream.StatusFunc) error {
	// poolSync is a package var so pool tests can stand in for rsync; route
	// through it to preserve that seam.
	return poolSync(ctx, b.client, id, retry, repoPath, cwd, status)
}

func (b *circleciBackend) Pull(ctx context.Context, id, repoPath, localDir string, status iostream.StatusFunc) error {
	return RsyncPull(ctx, b.client, id, repoPath, localDir, status)
}

func (b *circleciBackend) Exec(ctx context.Context, id, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(string)) (int, error) {
	// Submit and stream are kept apart so onSubmitted sees the command ID: the
	// caller needs it before the command ends, not after.
	commandID, err := b.client.SubmitExec(ctx, id, "sh", []string{"-c", script}, env)
	if err != nil {
		return 0, fmt.Errorf("submit: %w", err)
	}
	if onSubmitted != nil {
		onSubmitted(commandID)
	}
	res, err := b.client.StreamOutput(ctx, commandID, "", onOutput)
	if err != nil {
		return 0, fmt.Errorf("stream output: %w", err)
	}
	return res.ExitCode, nil
}
