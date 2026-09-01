package sidecar

import (
	"context"
	"fmt"
	"os"
)

// sidecarHome returns the base home directory on the sidecar. It reads
// CHUNK_SIDECAR_HOME so the default "/home/user" can be overridden when the
// image uses a different OS user.
func sidecarHome() string {
	if h := os.Getenv("CHUNK_SIDECAR_HOME"); h != "" {
		return h
	}
	return "/home/user"
}

// ResolveWorkspace determines the workspace path. Priority:
// 1. CLI --workdir flag  2. sidecar.json workspace  3. default <sidecarHome>/<repo>.
// Returns an error if no repo-specific path can be determined (repo empty and no
// saved workspace), because the bare home dir is not safe to pass to rm -rf.
func ResolveWorkspace(ctx context.Context, cliWorkdir, repo string) (string, error) {
	if cliWorkdir != "" {
		return cliWorkdir, nil
	}
	if active, err := LoadActive(ctx); err == nil && active != nil && active.Workspace != "" {
		return active.Workspace, nil
	}
	if repo == "" {
		return "", fmt.Errorf("sync: cannot determine workspace: repo name is empty and no workspace is saved")
	}
	return sidecarHome() + "/" + repo, nil
}

// persistWorkspace saves the resolved workspace back to the sidecar file if it
// differs from the current value.
func persistWorkspace(ctx context.Context, workspace string) error {
	active, err := LoadActive(ctx)
	if err != nil {
		return err
	}
	if active == nil || active.Workspace == workspace {
		return nil
	}
	active.Workspace = workspace
	return SaveActive(ctx, *active)
}
