package sidecar

import (
	"context"
	"fmt"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// circleciBackend runs sidecars as CircleCI microVMs. It wraps a circleci
// client and an org, and delegates to the package's existing sidecar
// operations so the transport (rsync over the SSH-over-WebSocket tunnel) and
// lifecycle are unchanged. It lives in this package, rather than a subpackage,
// because it is a thin façade over sidecar's own internals — the poolSync test
// seam, Create, the rsync helpers, IsDefinitelyStale — which a separate package
// could not reach without exporting them and would deadlock NewPool's default
// construction in an import cycle.
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

func (b *circleciBackend) Create(ctx context.Context, name, image, _ string) (Instance, error) {
	// repoPath is ignored: a CircleCI sidecar's workspace is created by the
	// later rsync, not at provision time.
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

func (b *circleciBackend) Exec(ctx context.Context, id, script string, env map[string]string, onOutput iostream.OutputFn, onSubmitted func(string)) (int, error) {
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
