package sidecar

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// Sync synchronises local changes to a sidecar over SSH.
// It ensures the workspace base exists, clones the repo into workdir if absent,
// then resets to the remote base and applies a patch of local changes.
// workdir overrides the destination path; defaults to /workspace/<repo>.
func Sync(ctx context.Context,
	client *circleci.Client, sidecarID, identityFile, authSock, workdir string, status iostream.StatusFunc) error {

	session, err := openSessionWithRetry(ctx, client, sidecarID, identityFile, authSock)
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

	repoPath := ResolveWorkspace(ctx, workdir, repo)

	if err := persistWorkspace(ctx, repoPath); err != nil {
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

	status(iostream.LevelInfo, "Fetching remote refs on sidecar...")
	fetchCmd := fmt.Sprintf("git -C %s fetch origin", ShellEscape(repoPath))
	fetchResult, err := ExecOverSSH(ctx, session, fetchCmd, nil, nil)
	if err != nil {
		return fmt.Errorf("sync: fetch: %w", err)
	}
	if fetchResult.ExitCode != 0 {
		return fmt.Errorf("sync: fetch failed (exit code: %d): %s", fetchResult.ExitCode, fetchResult.Stderr)
	}

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

// openSessionWithRetry calls OpenSession, retrying on transient errors to give
// a newly-created sidecar time to finish booting before its SSH service is ready.
func openSessionWithRetry(ctx context.Context, client *circleci.Client, sidecarID, identityFile, authSock string) (*Session, error) {
	const retryDelay = 5 * time.Second
	const maxAttempts = 12 // up to ~1 minute

	var err error
	for i := range maxAttempts {
		var session *Session
		session, err = OpenSession(ctx, client, sidecarID, identityFile, authSock)
		if err == nil {
			return session, nil
		}
		if !isTransientSSHError(err) || i == maxAttempts-1 {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(retryDelay):
		}
	}
	return nil, err
}

// isTransientSSHError returns true for errors that are worth retrying when
// opening a session — timeouts and connection failures that indicate the
// sidecar's SSH service is not yet ready. Auth failures, missing keys, and
// definitive API errors are not transient.
func isTransientSSHError(err error) bool {
	if errors.Is(err, circleci.ErrNotAuthorized) {
		return false
	}
	var statusErr *circleci.StatusError
	if errors.As(err, &statusErr) {
		return false
	}
	var keyErr *KeyNotFoundError
	if errors.As(err, &keyErr) {
		return false
	}
	var pubKeyErr *PublicKeyNotFoundError
	return !errors.As(err, &pubKeyErr)
}
