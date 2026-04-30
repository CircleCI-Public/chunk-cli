package sidecar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

const workspaceDir = "./workspace"

// resolveWorkspace determines the workspace path. Priority:
// 1. CLI --workdir flag  2. sidecar.json workspace  3. default.
func resolveWorkspace(cliWorkdir, repo string) string {
	if cliWorkdir != "" {
		return cliWorkdir
	}
	if active, err := LoadActive(); err == nil && active != nil && active.Workspace != "" {
		return active.Workspace
	}
	return workspaceDir + "/" + repo
}

// persistWorkspace saves the resolved workspace back to the sidecar file if it
// differs from the current value.
func persistWorkspace(workspace string) error {
	active, err := LoadActive()
	if err != nil {
		return err
	}
	if active == nil || active.Workspace == workspace {
		return nil
	}
	active.Workspace = workspace
	return SaveActive(*active)
}

// Sync synchronises local changes to a sidecar over SSH.
// It ensures the workspace base exists, clones the repo into workdir if absent,
// then resets to the remote base and applies a patch of local changes.
// workdir overrides the destination path; defaults to /workspace/<repo>.
func Sync(ctx context.Context,
	client *circleci.Client, sidecarID, identityFile, authSock, workdir string, status iostream.StatusFunc) error {

	session, err := OpenSession(ctx, client, sidecarID, identityFile, authSock)
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	org, repo, err := gitremote.DetectOrgAndRepo(cwd)
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	repoPath := resolveWorkspace(workdir, repo)

	if err := persistWorkspace(repoPath); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not save workspace: %v", err))
	}

	// Try once and exit here if it worked
	err = syncWorkspace(ctx, status, org, repo, repoPath, session)
	if err == nil {
		status(iostream.LevelDone, "Synced")
		return nil
	}
	// We should only try again if the failure was in the apply phase.
	if !errors.Is(err, errApplyFailed) {
		return err
	}

	// Second attempt - after deleting the remote folder
	status(iostream.LevelWarn, fmt.Sprintf("Local %s/%s drifted from remote: %s (%s) - attempting clean",
		org, repo, repoPath, err))

	// Delete the remote working directory
	if result, err := ExecOverSSH(ctx, session, "rm -rf "+ShellEscape(repoPath), nil, nil); err != nil {
		return fmt.Errorf("sync: rm %s: %w", repoPath, err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("sync: rm %s: %s", repoPath, result.Stderr)
	}

	if err := syncWorkspace(ctx, status, org, repo, repoPath, session); err != nil {
		return fmt.Errorf("sync retry: %w", err)
	}

	status(iostream.LevelDone, "Synced")
	return nil
}

// BundleSync synchronises local commits and working-tree changes to a sidecar
// using git bundle, without requiring the branch to be pushed to GitHub.
//
// On first sync (no LastSyncedRef) a full bundle of HEAD is sent. On subsequent
// syncs only commits since the last synced ref are bundled (incremental). In
// both cases any uncommitted working-tree changes are applied on top as a patch.
func BundleSync(ctx context.Context,
	client *circleci.Client, sidecarID, identityFile, authSock, workdir, cwd string, status iostream.StatusFunc) error {

	session, err := OpenSession(ctx, client, sidecarID, identityFile, authSock)
	if err != nil {
		return err
	}

	_, repo, err := gitremote.DetectOrgAndRepo(cwd)
	if err != nil {
		return fmt.Errorf("bundle sync: %w", err)
	}

	repoPath := resolveWorkspace(workdir, repo)
	if err := persistWorkspace(repoPath); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not save workspace: %v", err))
	}

	active, err := LoadActive()
	if err != nil {
		return fmt.Errorf("bundle sync: load active sidecar: %w", err)
	}
	if active == nil {
		active = &ActiveSidecar{SidecarID: sidecarID}
	}
	lastRef := active.LastSyncedRef

	headRef, err := gitutil.HeadRef(cwd)
	if err != nil {
		return fmt.Errorf("bundle sync: %w", err)
	}

	// Ensure the workspace directory exists on the sidecar.
	parentDir := filepath.Dir(repoPath)
	if result, err := ExecOverSSH(ctx, session, "mkdir -p "+ShellEscape(parentDir), nil, nil); err != nil {
		return fmt.Errorf("bundle sync: mkdir: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bundle sync: mkdir -p %s: %s", parentDir, result.Stderr)
	}

	// Init the repo on the sidecar if it's not already there.
	testResult, err := ExecOverSSH(ctx, session, "test -d "+ShellEscape(repoPath), nil, nil)
	if err != nil {
		return fmt.Errorf("bundle sync: check repo dir: %w", err)
	}
	if testResult.ExitCode != 0 {
		initCmd := fmt.Sprintf("git init %s && git -C %s commit --allow-empty -m init",
			ShellEscape(repoPath), ShellEscape(repoPath))
		if result, err := ExecOverSSH(ctx, session, initCmd, nil, nil); err != nil {
			return fmt.Errorf("bundle sync: git init: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("bundle sync: git init: %s", result.Stderr)
		}
		lastRef = "" // force full bundle for a fresh repo
	}

	resetCmd := fmt.Sprintf("git -C %s reset --hard HEAD", ShellEscape(repoPath))
	cleanCmd := fmt.Sprintf("git -C %s clean -fd", ShellEscape(repoPath))

	// Sync commits: skip the bundle when already up-to-date, otherwise send and fetch.
	if lastRef == headRef {
		status(iostream.LevelInfo, "No new commits since last sync.")
	} else {
		if err := sendBundle(ctx, session, lastRef, cwd, repo, repoPath, status); err != nil {
			return err
		}
	}

	// Reset and clean the remote working tree after the bundle fetch (or no-op sync).
	if result, err := ExecOverSSH(ctx, session, resetCmd, nil, nil); err != nil {
		return fmt.Errorf("bundle sync: reset: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bundle sync: reset: %s", result.Stderr)
	}
	if result, err := ExecOverSSH(ctx, session, cleanCmd, nil, nil); err != nil {
		return fmt.Errorf("bundle sync: clean: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bundle sync: clean: %s", result.Stderr)
	}

	// Apply any uncommitted working-tree changes as a patch on top.
	patch, err := gitutil.GeneratePatch(headRef)
	if err != nil {
		return fmt.Errorf("bundle sync: %w", err)
	}
	if patch != "" {
		status(iostream.LevelInfo, fmt.Sprintf("Applying working-tree changes (%d bytes)...", len(patch)))
		applyCmd := fmt.Sprintf("git -C %s apply", ShellEscape(repoPath))
		if result, err := ExecOverSSH(ctx, session, applyCmd, strings.NewReader(patch), nil); err != nil {
			return fmt.Errorf("bundle sync: apply patch: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("bundle sync: apply patch: %s", result.Stderr)
		}
	}

	// Persist the synced ref.
	active.LastSyncedRef = headRef
	if err := SaveActive(*active); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not save last synced ref: %v", err))
	}

	status(iostream.LevelDone, "Synced")
	return nil
}

// sendBundle creates and transfers a git bundle (full or incremental) to the
// sidecar, then fetches it into the remote repo.
func sendBundle(ctx context.Context, session *Session, lastRef, cwd, repo, repoPath string, status iostream.StatusFunc) error {
	label := "incremental bundle"
	if lastRef == "" {
		label = "full bundle"
	}

	bundle, err := gitutil.CreateBundle(lastRef, cwd)
	if err != nil {
		return fmt.Errorf("bundle sync: %w", err)
	}
	status(iostream.LevelInfo, fmt.Sprintf("Sending %s (%d bytes)...", label, len(bundle)))

	bundlePath := fmt.Sprintf("/tmp/chunk-sync-%s.bundle", repo)
	writeCmd := "cat > " + ShellEscape(bundlePath)
	if result, err := ExecOverSSH(ctx, session, writeCmd, bytes.NewReader(bundle), nil); err != nil {
		return fmt.Errorf("bundle sync: write bundle: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bundle sync: write bundle: %s", result.Stderr)
	}

	fetchCmd := fmt.Sprintf("git -C %s fetch %s HEAD:HEAD --update-head-ok", ShellEscape(repoPath), ShellEscape(bundlePath))
	if result, err := ExecOverSSH(ctx, session, fetchCmd, nil, nil); err != nil {
		return fmt.Errorf("bundle sync: fetch: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bundle sync: fetch: %s", result.Stderr)
	}
	return nil
}

var errApplyFailed = errors.New("apply failed")

func syncWorkspace(ctx context.Context, status iostream.StatusFunc, org, repo, repoPath string, session *Session) error {
	status(iostream.LevelInfo, fmt.Sprintf("Assessing %s/%s on remote: %s...", org, repo, repoPath))

	// Ensure the parent directory exists on the sidecar.
	parentDir := filepath.Dir(repoPath)
	if result, err := ExecOverSSH(ctx, session, "mkdir -p "+ShellEscape(parentDir), nil, nil); err != nil {
		return fmt.Errorf("sync: mkdir %s: %w", parentDir, err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("sync: mkdir -p %s: %s", parentDir, result.Stderr)
	}

	// Clone into remote workspace if not already present.
	testResult, err := ExecOverSSH(ctx, session, "test -d "+ShellEscape(repoPath), nil, nil)
	if err != nil {
		return fmt.Errorf("sync: check repo dir: %w", err)
	}
	if testResult.ExitCode != 0 {
		repoURL := fmt.Sprintf("https://github.com/%s/%s.git", org, repo)
		var cloneCmd string
		if gitutil.IsBranchPushed() {
			branch, err := gitutil.CurrentBranch()
			if err != nil {
				return fmt.Errorf("sync: %w", err)
			}
			cloneCmd = fmt.Sprintf("git clone --branch %s %s %s",
				ShellEscape(branch), ShellEscape(repoURL), ShellEscape(repoPath),
			)
		} else {
			status(iostream.LevelInfo, "Branch not pushed to remote; cloning default branch instead.")
			cloneCmd = fmt.Sprintf("git clone %s %s",
				ShellEscape(repoURL), ShellEscape(repoPath),
			)
		}

		status(iostream.LevelInfo, fmt.Sprintf("Cloning %s/%s into %s...", org, repo, repoPath))
		cloneResult, err := ExecOverSSH(ctx, session, cloneCmd, nil, nil)
		if err != nil {
			return fmt.Errorf("sync: clone: %w", err)
		}
		if cloneResult.ExitCode != 0 {
			detail := cloneResult.Stderr
			if detail == "" {
				detail = "git clone exited with a non-zero status"
			}
			return fmt.Errorf("sync: clone failed: %s", detail)
		}
	}

	status(iostream.LevelInfo, fmt.Sprintf("Synchronising local %s/%s to remote: %s...", org, repo, repoPath))

	base, err := gitutil.MergeBase()
	if err != nil {
		return &RemoteBaseError{Err: err}
	}

	patch, err := gitutil.GeneratePatch(base)
	if err != nil {
		return err
	}
	if patch == "" {
		status(iostream.LevelInfo, "No local changes relative to remote base.")
		return nil
	}

	status(iostream.LevelInfo, fmt.Sprintf("Synchronising %d bytes.", len(patch)))

	resetCmd := fmt.Sprintf(
		`sh -c "cd %s && git reset --hard %s && git clean -fd"`,
		ShellEscape(repoPath), ShellEscape(base),
	)
	resetResult, err := ExecOverSSH(ctx, session, resetCmd, nil, nil)
	if err != nil {
		return err
	}
	if resetResult.ExitCode != 0 {
		detail := resetResult.Stderr
		if detail == "" {
			detail = "git reset exited with a non-zero status"
		}
		return fmt.Errorf("git reset failed (exit code: %d): %s", resetResult.ExitCode, detail)
	}

	applyCmd := fmt.Sprintf("git -C %s apply", ShellEscape(repoPath))
	applyResult, err := ExecOverSSH(ctx, session, applyCmd, strings.NewReader(patch), nil)
	if err != nil {
		return err
	}
	if applyResult.ExitCode != 0 {
		detail := applyResult.Stderr
		if detail == "" {
			detail = "git apply exited with a non-zero status"
		}
		return fmt.Errorf("%w (exit code: %d): %s", errApplyFailed, applyResult.ExitCode, detail)
	}
	return nil
}
