package sidecar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
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

// Sync synchronises local changes to a sidecar over SSH.
// It ensures the workspace base exists, clones the repo into workdir if absent,
// then resets to the remote base and applies a patch of local changes.
// workdir overrides the destination path; defaults to /home/user/<repo>.
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
		return &NoOriginRemoteError{Err: err}
	}

	repoPath, err := ResolveWorkspace(ctx, workdir, repo)
	if err != nil {
		return err
	}

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

	_, repo, repoErr := gitremote.DetectOrgAndRepo(cwd)
	if repoErr != nil && workdir == "" {
		return &NoOriginRemoteError{Err: repoErr}
	}

	repoPath, err := ResolveWorkspace(ctx, workdir, repo)
	if err != nil {
		return fmt.Errorf("bundle sync: %w", err)
	}
	if err := persistWorkspace(ctx, repoPath); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not save workspace: %v", err))
	}

	active, err := LoadActive(ctx)
	if err != nil {
		return fmt.Errorf("bundle sync: load active sidecar: %w", err)
	}
	if active == nil {
		active = &ActiveSidecar{SidecarIDs: []string{sidecarID}}
	}
	lastRef := active.LastSyncedRef
	if active.ID() != sidecarID {
		lastRef = "" // sidecar changed; force full bundle
		active.SidecarIDs = []string{sidecarID}
	}

	headRef, err := gitutil.HeadRef(cwd)
	if err != nil {
		return fmt.Errorf("bundle sync: %w", err)
	}

	// Ensure the destination exists and has a git repo; a fresh repo forces a
	// full bundle.
	freshRepo, err := ensureRemoteRepo(ctx, session, repoPath)
	if err != nil {
		return fmt.Errorf("bundle sync: %w", err)
	}
	if freshRepo {
		lastRef = ""
	}

	resetRef := "HEAD"
	if lastRef == headRef {
		status(iostream.LevelInfo, "No new commits since last sync.")
	} else {
		bundle, err := gitutil.CreateBundle(lastRef, cwd)
		if err != nil {
			return fmt.Errorf("bundle sync: %w", err)
		}
		label := "incremental bundle"
		if lastRef == "" {
			label = "full bundle"
		}
		status(iostream.LevelInfo, fmt.Sprintf("Sending %s (%d bytes)...", label, len(bundle)))
		if err := sendBundle(ctx, session, repo, repoPath, bundle); err != nil {
			return fmt.Errorf("bundle sync: %w", err)
		}
		resetRef = "FETCH_HEAD"
	}

	patch, err := gitutil.GeneratePatch(headRef)
	if err != nil {
		return fmt.Errorf("bundle sync: %w", err)
	}
	if patch != "" {
		status(iostream.LevelInfo, fmt.Sprintf("Applying working-tree changes (%d bytes)...", len(patch)))
	}
	if err := resetCleanApply(ctx, session, repoPath, resetRef, patch); err != nil {
		return fmt.Errorf("bundle sync: %w", err)
	}

	// Persist the synced ref.
	active.LastSyncedRef = headRef
	if err := SaveActive(ctx, *active); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not save last synced ref: %v", err))
	}

	status(iostream.LevelDone, "Synced")
	return nil
}

// BundleSyncFanOut synchronises a local working tree to multiple sidecars in
// parallel. It builds one git bundle and patch, then delivers them to all
// targets concurrently. Unlike BundleSync, it always sends a full bundle (no
// incremental optimisation) and does not update any active-sidecar state.
func BundleSyncFanOut(ctx context.Context, client *circleci.Client, sidecarIDs []string, identityFile, authSock, workdir, cwd string, status iostream.StatusFunc) error {
	_, repo, repoErr := gitremote.DetectOrgAndRepo(cwd)
	if repoErr != nil && workdir == "" {
		return &NoOriginRemoteError{Err: repoErr}
	}

	repoPath := workdir
	if repoPath == "" {
		repoPath = sidecarHome() + "/" + repo
	}

	headRef, err := gitutil.HeadRef(cwd)
	if err != nil {
		return fmt.Errorf("fan-out sync: %w", err)
	}

	bundle, err := gitutil.CreateBundle("", cwd)
	if err != nil {
		return fmt.Errorf("fan-out sync: %w", err)
	}
	status(iostream.LevelInfo, fmt.Sprintf("Bundle ready (%d bytes), syncing to %d sidecars...", len(bundle), len(sidecarIDs)))

	patch, err := gitutil.GeneratePatch(headRef)
	if err != nil {
		return fmt.Errorf("fan-out sync: %w", err)
	}

	errs := make([]error, len(sidecarIDs))
	var wg sync.WaitGroup
	for i, id := range sidecarIDs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sess, err := OpenSession(ctx, client, id, identityFile, authSock)
			if err != nil {
				errs[i] = fmt.Errorf("sidecar %s: open session: %w", id, err)
				return
			}
			if err := applyBundleToSidecar(ctx, sess, repo, repoPath, bundle, patch); err != nil {
				errs[i] = fmt.Errorf("sidecar %s: %w", id, err)
			}
		}(i, id)
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return err
	}
	status(iostream.LevelDone, fmt.Sprintf("Synced %d sidecars", len(sidecarIDs)))
	return nil
}

// applyBundleToSidecar delivers a pre-built full bundle and patch to a single
// sidecar: it ensures the repo exists, fetches the bundle, then resets, cleans,
// and applies the working-tree patch. It shares its per-step primitives with
// BundleSync so the single- and multi-sidecar paths behave identically.
func applyBundleToSidecar(ctx context.Context, session *Session, repo, repoPath string, bundle []byte, patch string) error {
	if _, err := ensureRemoteRepo(ctx, session, repoPath); err != nil {
		return err
	}
	if err := sendBundle(ctx, session, repo, repoPath, bundle); err != nil {
		return err
	}
	return resetCleanApply(ctx, session, repoPath, "FETCH_HEAD", patch)
}

// ensureRemoteRepo makes sure repoPath's parent exists and repoPath holds a git
// repo, running "git init" when absent. It returns true when the repo was
// freshly initialised (so callers know to send a full bundle).
func ensureRemoteRepo(ctx context.Context, session *Session, repoPath string) (bool, error) {
	parentDir := filepath.Dir(repoPath)
	if result, err := ExecOverSSH(ctx, session, "mkdir -p "+ShellEscape(parentDir), nil, nil); err != nil {
		return false, fmt.Errorf("mkdir: %w", err)
	} else if result.ExitCode != 0 {
		return false, fmt.Errorf("mkdir -p %s: %s", parentDir, result.Stderr)
	}

	testResult, err := ExecOverSSH(ctx, session, "test -d "+ShellEscape(repoPath+"/.git"), nil, nil)
	if err != nil {
		return false, fmt.Errorf("check repo: %w", err)
	}
	if testResult.ExitCode == 0 {
		return false, nil
	}
	if result, err := ExecOverSSH(ctx, session, "git init "+ShellEscape(repoPath), nil, nil); err != nil {
		return false, fmt.Errorf("git init: %w", err)
	} else if result.ExitCode != 0 {
		return false, fmt.Errorf("git init: %s", result.Stderr)
	}
	return true, nil
}

// sendBundle transfers a pre-built git bundle to the sidecar and fetches it into
// the remote repo (leaving the fetched tip as FETCH_HEAD).
func sendBundle(ctx context.Context, session *Session, repo, repoPath string, bundle []byte) error {
	bundleName := repo
	if bundleName == "" {
		bundleName = "chunk"
	}
	bundlePath := fmt.Sprintf("/tmp/chunk-sync-%s.bundle", bundleName)
	if result, err := ExecOverSSH(ctx, session, "tee "+ShellEscape(bundlePath), bytes.NewReader(bundle), nil); err != nil {
		return fmt.Errorf("write bundle: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("write bundle: %s", result.Stderr)
	}

	fetchCmd := fmt.Sprintf("git -C %s fetch %s HEAD", ShellEscape(repoPath), ShellEscape(bundlePath))
	if result, err := ExecOverSSH(ctx, session, fetchCmd, nil, nil); err != nil {
		return fmt.Errorf("fetch: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("fetch: %s", result.Stderr)
	}
	return nil
}

// resetCleanApply resets repoPath to resetRef, cleans untracked files, then
// applies patch (when non-empty) as working-tree changes on top.
func resetCleanApply(ctx context.Context, session *Session, repoPath, resetRef, patch string) error {
	resetCmd := fmt.Sprintf("git -C %s reset --hard %s", ShellEscape(repoPath), resetRef)
	if result, err := ExecOverSSH(ctx, session, resetCmd, nil, nil); err != nil {
		return fmt.Errorf("reset: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("reset: %s", result.Stderr)
	}

	cleanCmd := fmt.Sprintf("git -C %s clean -fd", ShellEscape(repoPath))
	if result, err := ExecOverSSH(ctx, session, cleanCmd, nil, nil); err != nil {
		return fmt.Errorf("clean: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("clean: %s", result.Stderr)
	}

	if patch != "" {
		applyCmd := fmt.Sprintf("git -C %s apply", ShellEscape(repoPath))
		if result, err := ExecOverSSH(ctx, session, applyCmd, strings.NewReader(patch), nil); err != nil {
			return fmt.Errorf("apply patch: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("apply patch: %s", result.Stderr)
		}
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
