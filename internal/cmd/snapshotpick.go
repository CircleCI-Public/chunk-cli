package cmd

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/CircleCI-Public/chunk-cli/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// snapshotCriteria describes workDir's repository for snapshot selection.
//
// Both signals prefer recorded facts over inference: the repo name comes from
// the git remote (what the snapshot's author would have named it after) before
// the directory name, and the stack comes from the environment `sidecar setup`
// already saved before re-deriving it by walking the tree.
func snapshotCriteria(workDir string, cfg *config.ProjectConfig) sidecar.SnapshotCriteria {
	c := sidecar.SnapshotCriteria{}

	if _, repo, err := gitremote.DetectOrgAndRepo(workDir); err == nil && repo != "" {
		c.Repo = repo
	} else if cfg != nil && cfg.VCS != nil && cfg.VCS.Repo != "" {
		c.Repo = cfg.VCS.Repo
	} else if abs, err := filepath.Abs(workDir); err == nil {
		c.Repo = filepath.Base(abs)
	}

	if cfg != nil && cfg.Environment != nil && cfg.Environment.Stack != "" {
		c.Stack = cfg.Environment.Stack
	} else if stack, err := envbuilder.DetectStack(workDir); err == nil {
		c.Stack = stack
	}
	if c.Stack == envbuilder.StackUnknown {
		c.Stack = ""
	}
	return c
}

// autoSelectSnapshotImage returns the ID of the org snapshot that best fits the
// repo in workDir, for projects that have no validation.sidecarImage recorded.
//
// Creating the sidecar from a matching snapshot is what makes an unconfigured
// repo usable: the plain default image has none of the repo's dependencies, so
// every command run against it fails on a missing toolchain rather than on the
// code. Returns "" when the org has no snapshot related to this repo, which
// leaves the caller on that default image — the previous behaviour, and still
// the right one when guessing would be worse than not guessing.
//
// Selection is never fatal: any failure to reach the API is reported and then
// treated as "no match", because losing a snapshot is a worse outcome for the
// user than losing the whole validate run.
func autoSelectSnapshotImage(
	ctx context.Context,
	client *circleci.Client,
	orgID, workDir string,
	statusFn iostream.StatusFunc,
	streams iostream.Streams,
) string {
	if client == nil || orgID == "" {
		return ""
	}
	cfg, err := config.LoadProjectConfig(workDir)
	if err != nil {
		cfg = nil
	}
	criteria := snapshotCriteria(workDir, cfg)

	match, ok, err := sidecar.ResolveSnapshot(ctx, client, orgID, criteria)
	if err != nil {
		streams.ErrPrintf("warning: could not list snapshots to pick a sidecar image: %v\n", err)
		return ""
	}
	if !ok {
		statusFn(iostream.LevelInfo, snapshotMissHint(criteria))
		return ""
	}
	statusFn(iostream.LevelInfo, fmt.Sprintf(
		"using snapshot %s (%s) — %s", match.Snapshot.Name, match.Snapshot.ID, match.Reason))
	return match.Snapshot.ID
}

// snapshotMissHint explains a no-match in terms of what was searched for, so
// the user can tell a missing snapshot apart from a mis-detected stack.
func snapshotMissHint(c sidecar.SnapshotCriteria) string {
	switch {
	case c.Repo != "" && c.Stack != "":
		return fmt.Sprintf("no snapshot matches %s or %s; using the default image", c.Repo, c.Stack)
	case c.Repo != "":
		return fmt.Sprintf("no snapshot matches %s; using the default image", c.Repo)
	case c.Stack != "":
		return fmt.Sprintf("no snapshot matches %s; using the default image", c.Stack)
	default:
		return "no snapshot to match against; using the default image"
	}
}
