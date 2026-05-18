package sidecar

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

const workspaceDir = "./workspace"

// ResolveWorkspace determines the workspace path. Priority:
// 1. CLI --workdir flag  2. sidecar.json workspace  3. default ./workspace/<repo>.
func ResolveWorkspace(ctx context.Context, cliWorkdir, repo string) string {
	if cliWorkdir != "" {
		return cliWorkdir
	}
	if active, err := LoadActive(ctx); err == nil && active != nil && active.Workspace != "" {
		return active.Workspace
	}
	if repo == "" {
		return workspaceDir
	}
	return workspaceDir + "/" + repo
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

// inRepo builds a remote shell command that runs gitCmd inside repoPath.
// Uses double-quote wrapping so that ShellEscape (single-quoted) paths inside
// work correctly on the sidecar's SSH server.
func inRepo(repoPath, gitCmd string) string {
	return fmt.Sprintf(`sh -c "cd %s && %s"`, ShellEscape(repoPath), gitCmd)
}

// Bootstrap clones (or updates) the sidecar workspace from GitHub, checks out
// the best available local HEAD commit, applies a patch to reach exact local
// state, and persists BaselineRef + BaselineBranch.
//
// If the branch is pushed, the sidecar lands on the exact local HEAD and the
// patch covers only uncommitted working-tree changes. If the branch is not
// pushed, the sidecar lands on the merge-base and the patch carries all
// unpushed commits as file changes.
func Bootstrap(ctx context.Context,
	client *circleci.Client, sidecarID, identityFile, authSock, workdir, cwd string, status iostream.StatusFunc) error {

	session, err := OpenSession(ctx, client, sidecarID, identityFile, authSock)
	if err != nil {
		return err
	}

	org, repo, err := gitremote.DetectOrgAndRepo(cwd)
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	repoPath := ResolveWorkspace(ctx, workdir, repo)
	if err := persistWorkspace(ctx, repoPath); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not save workspace: %v", err))
	}

	// Ensure the parent directory exists on the sidecar.
	parentDir := filepath.Dir(repoPath)
	if result, err := ExecOverSSH(ctx, session, "mkdir -p "+ShellEscape(parentDir), nil, nil); err != nil {
		return fmt.Errorf("bootstrap: mkdir: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bootstrap: mkdir -p %s: %s", parentDir, result.Stderr)
	}

	// Clone if absent, otherwise fetch to bring the clone up to date.
	testResult, err := ExecOverSSH(ctx, session, "test -d "+ShellEscape(repoPath), nil, nil)
	if err != nil {
		return fmt.Errorf("bootstrap: check repo dir: %w", err)
	}
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", org, repo)
	if testResult.ExitCode != 0 {
		status(iostream.LevelInfo, fmt.Sprintf("Cloning %s/%s into %s...", org, repo, repoPath))
		cloneCmd := fmt.Sprintf("git clone %s %s", ShellEscape(repoURL), ShellEscape(repoPath))
		if result, err := ExecOverSSH(ctx, session, cloneCmd, nil, nil); err != nil {
			return fmt.Errorf("bootstrap: clone: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("bootstrap: clone failed: %s", result.Stderr)
		}
	} else {
		status(iostream.LevelInfo, fmt.Sprintf("Updating %s/%s at %s...", org, repo, repoPath))
		if result, err := ExecOverSSH(ctx, session, inRepo(repoPath, "git fetch origin"), nil, nil); err != nil {
			return fmt.Errorf("bootstrap: fetch: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("bootstrap: fetch failed: %s", result.Stderr)
		}
	}

	// Resolve the checkout target: exact HEAD when pushed, merge-base otherwise.
	branch, err := gitutil.CurrentBranch()
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	headSHA, err := gitutil.HeadRef(cwd)
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}

	checkoutSHA := headSHA
	if !gitutil.IsBranchPushed() {
		status(iostream.LevelInfo, "Branch not pushed; checking out merge-base instead.")
		checkoutSHA, err = gitutil.MergeBase()
		if err != nil {
			return &RemoteBaseError{Err: err}
		}
	}

	// Reset and clean before checkout so a dirty working tree never blocks it.
	if result, err := ExecOverSSH(ctx, session, inRepo(repoPath, "git reset --hard HEAD && git clean -fd"), nil, nil); err != nil {
		return fmt.Errorf("bootstrap: pre-checkout reset: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bootstrap: pre-checkout reset: %s", result.Stderr)
	}

	if result, err := ExecOverSSH(ctx, session, inRepo(repoPath, "git checkout "+checkoutSHA), nil, nil); err != nil {
		return fmt.Errorf("bootstrap: checkout: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bootstrap: checkout %s: %s", checkoutSHA, result.Stderr)
	}

	// Reset to the exact SHA and clean (handles any detached HEAD drift).
	if result, err := ExecOverSSH(ctx, session, inRepo(repoPath, "git reset --hard "+checkoutSHA+" && git clean -fd"), nil, nil); err != nil {
		return fmt.Errorf("bootstrap: reset: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bootstrap: reset: %s", result.Stderr)
	}

	patch, err := gitutil.GeneratePatch(checkoutSHA)
	if err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	if patch != "" {
		status(iostream.LevelInfo, fmt.Sprintf("Applying patch (%d bytes)...", len(patch)))
		if result, err := ExecOverSSH(ctx, session, inRepo(repoPath, "git apply"), strings.NewReader(patch), nil); err != nil {
			return fmt.Errorf("bootstrap: apply patch: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("bootstrap: apply patch: %s", result.Stderr)
		}
	}

	// Persist the baseline so incremental syncs know their anchor.
	active, err := LoadActive(ctx)
	if err != nil {
		return fmt.Errorf("bootstrap: load active: %w", err)
	}
	if active == nil {
		active = &ActiveSidecar{SidecarID: sidecarID}
	}
	active.BaselineRef = checkoutSHA
	active.BaselineBranch = branch
	if err := SaveActive(ctx, *active); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not save baseline: %v", err))
	}

	status(iostream.LevelDone, "Bootstrapped")
	return nil
}

// Sync sends an incremental patch from the stored BaselineRef to the sidecar.
// It triggers Bootstrap automatically when the baseline is stale: no baseline
// recorded, the local branch changed, or BaselineRef is no longer an ancestor
// of HEAD (e.g. after a rebase).
func Sync(ctx context.Context,
	client *circleci.Client, sidecarID, identityFile, authSock, workdir, cwd string, status iostream.StatusFunc) error {

	active, err := LoadActive(ctx)
	if err != nil {
		return fmt.Errorf("sync: load active sidecar: %w", err)
	}

	currentBranch, err := gitutil.CurrentBranch()
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	headSHA, err := gitutil.HeadRef(cwd)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	needsBootstrap := active == nil ||
		active.BaselineRef == "" ||
		active.BaselineBranch != currentBranch ||
		!gitutil.IsAncestor(active.BaselineRef, headSHA, cwd)

	if needsBootstrap {
		if active != nil && active.BaselineBranch != "" && active.BaselineBranch != currentBranch {
			status(iostream.LevelInfo, fmt.Sprintf("Branch changed from %q to %q; re-bootstrapping...", active.BaselineBranch, currentBranch))
		} else if active != nil && active.BaselineRef != "" {
			status(iostream.LevelInfo, "Baseline is stale; re-bootstrapping...")
		}
		return Bootstrap(ctx, client, sidecarID, identityFile, authSock, workdir, cwd, status)
	}

	session, err := OpenSession(ctx, client, sidecarID, identityFile, authSock)
	if err != nil {
		return err
	}

	repoPath := active.Workspace
	if repoPath == "" {
		_, repo, detErr := gitremote.DetectOrgAndRepo(cwd)
		if detErr != nil {
			return fmt.Errorf("sync: %w", detErr)
		}
		repoPath = ResolveWorkspace(ctx, workdir, repo)
	}

	status(iostream.LevelInfo, fmt.Sprintf("Syncing from baseline %s...", active.BaselineRef[:12]))

	patch, err := gitutil.GeneratePatch(active.BaselineRef)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	// Always reset+clean to guarantee a clean working tree, even with no patch.
	resetCmd := inRepo(repoPath, "git reset --hard "+active.BaselineRef+" && git clean -fd")
	if result, err := ExecOverSSH(ctx, session, resetCmd, nil, nil); err != nil {
		return fmt.Errorf("sync: reset: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("sync: reset: %s", result.Stderr)
	}

	if patch == "" {
		status(iostream.LevelDone, "Synced (no local changes)")
		return nil
	}

	status(iostream.LevelInfo, fmt.Sprintf("Applying patch (%d bytes)...", len(patch)))
	if result, err := ExecOverSSH(ctx, session, inRepo(repoPath, "git apply"), strings.NewReader(patch), nil); err != nil {
		return fmt.Errorf("sync: apply patch: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("sync: apply patch: %s", result.Stderr)
	}

	status(iostream.LevelDone, "Synced")
	return nil
}
