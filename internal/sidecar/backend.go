package sidecar

import (
	"context"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// Backend is the provider a pool runs its sidecars on: it creates, syncs,
// execs on, snapshots, and deletes them. CircleCI microVMs are one
// implementation (circleciBackend, in circleci_backend.go); a local Docker
// backend is another (package sidecar/docker). The pool and the factory loop
// depend only on this interface, so swapping where the work runs does not touch
// them.
type Backend interface {
	// Create provisions a sidecar named name from image (a snapshot/image ID,
	// or empty for the backend's default) and returns it ready to sync to.
	// repoPath is where the workspace will live on the sidecar; backends whose
	// workspace is a local bind mount (Docker) need it at creation time, while
	// remote backends (CircleCI) ignore it and sync to it later.
	Create(ctx context.Context, name, image, repoPath string) (Instance, error)
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
	Exec(ctx context.Context, id, script string, env map[string]string, onOutput iostream.OutputFn, onSubmitted func(commandID string)) (exitCode int, err error)
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
