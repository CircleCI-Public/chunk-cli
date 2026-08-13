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
	"github.com/CircleCI-Public/chunk-cli/internal/hashutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
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
//
// When neither the HEAD commit nor the working-tree patch has changed since the
// previous sync, all remote operations are skipped entirely so golangci-lint
// caches remain untouched. When only the working-tree changed (no new commits),
// a reverse+apply delta is used instead of a full reset+clean+apply, which
// likewise leaves committed files untouched.
func BundleSync(ctx context.Context,
	client *circleci.Client, sidecarID, identityFile, authSock, workdir, cwd string, status iostream.StatusFunc) error {

	sess, err := OpenSession(ctx, client, sidecarID, identityFile, authSock)
	if err != nil {
		return err
	}

	_, repo, err := gitremote.DetectOrgAndRepo(cwd)
	if err != nil {
		return &NoOriginRemoteError{Err: err}
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
		active = &ActiveSidecar{SidecarID: sidecarID}
	}
	lastRef := active.LastSyncedRef
	if active.SidecarID != sidecarID {
		// Sidecar changed: force a full sync so the new sidecar gets a clean state.
		lastRef = ""
		active.SidecarID = sidecarID
		active.LastSyncedPatchHash = ""
	}

	// Resolve the repo root from cwd so patch file helpers use the correct
	// project key regardless of the process working directory.
	repoRoot, err := gitutil.RepoRoot(cwd)
	if err != nil {
		return fmt.Errorf("bundle sync: resolve repo root: %w", err)
	}

	headRef, err := gitutil.HeadRef(cwd)
	if err != nil {
		return fmt.Errorf("bundle sync: %w", err)
	}

	// Generate the patch before any network calls so we can check whether
	// anything changed since the last sync without opening a session first.
	patch, err := gitutil.GeneratePatch(headRef)
	if err != nil {
		return fmt.Errorf("bundle sync: %w", err)
	}
	patchHash := hashutil.SumParts(patch)

	status(iostream.LevelInfo, fmt.Sprintf("Syncing workspace %s...", repoPath))

	isNew, err := initRemoteWorkspace(ctx, sess, repoPath)
	if err != nil {
		return err
	}
	if isNew {
		lastRef = "" // force full bundle for a fresh repo
		active.LastSyncedPatchHash = ""
	}

	commitsChanged := lastRef != headRef
	patchChanged := patchHash != active.LastSyncedPatchHash

	if !commitsChanged && !patchChanged {
		// Nothing has changed since the last sync. Skip all remote operations so
		// golangci-lint caches remain valid.
		status(iostream.LevelDone, "Synced (up to date)")
		return nil
	}

	resetRef := "HEAD"
	if commitsChanged {
		if err := sendBundle(ctx, sess, lastRef, cwd, repo, repoPath, status); err != nil {
			return err
		}
		resetRef = "FETCH_HEAD"
	} else {
		status(iostream.LevelInfo, "No new commits since last sync.")
	}

	stateDir, stateDirErr := StateDir()
	if stateDirErr != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not resolve state directory; delta sync disabled: %v", stateDirErr))
		stateDir = ""
	}

	// When only the working-tree changed (no new commits), try a delta sync:
	// reverse the previously applied patch and apply the new one. This avoids
	// reset+clean which would touch every committed file and force golangci-lint
	// to re-analyse the entire codebase.
	if !commitsChanged && patchChanged && stateDir != "" {
		if oldPatch, hasPatch := loadSyncedPatch(ctx, stateDir, repoRoot); hasPatch {
			if err := applyDeltaPatch(ctx, sess, repoPath, oldPatch, patch, status); err == nil {
				return saveBundleSyncState(ctx, active, headRef, patchHash, stateDir, repoRoot, patch, status)
			}
			status(iostream.LevelWarn, "Delta sync failed; falling back to full sync")
		}
	}

	if err := applyFullSync(ctx, sess, repoPath, resetRef, patch, status); err != nil {
		return err
	}
	return saveBundleSyncState(ctx, active, headRef, patchHash, stateDir, repoRoot, patch, status)
}

// initRemoteWorkspace ensures the parent directory and a git repo exist at
// repoPath on the sidecar. Returns true when a fresh git init was performed
// (caller should force a full bundle sync).
func initRemoteWorkspace(ctx context.Context, sess *Session, repoPath string) (bool, error) {
	parentDir := filepath.Dir(repoPath)
	if result, err := ExecOverSSH(ctx, sess, "mkdir -p "+ShellEscape(parentDir), nil, nil); err != nil {
		return false, fmt.Errorf("bundle sync: mkdir: %w", err)
	} else if result.ExitCode != 0 {
		return false, fmt.Errorf("bundle sync: mkdir -p %s: %s", parentDir, result.Stderr)
	}

	testResult, err := ExecOverSSH(ctx, sess, "test -d "+ShellEscape(repoPath+"/.git"), nil, nil)
	if err != nil {
		return false, fmt.Errorf("bundle sync: check repo: %w", err)
	}
	if testResult.ExitCode == 0 {
		return false, nil // repo already exists
	}

	if result, err := ExecOverSSH(ctx, sess, "git init "+ShellEscape(repoPath), nil, nil); err != nil {
		return false, fmt.Errorf("bundle sync: git init: %w", err)
	} else if result.ExitCode != 0 {
		return false, fmt.Errorf("bundle sync: git init: %s", result.Stderr)
	}
	return true, nil
}

// applyFullSync resets the remote repo to resetRef, cleans untracked files,
// and applies patch on top. It is the fallback when the delta path is
// unavailable or fails.
func applyFullSync(ctx context.Context, sess *Session, repoPath, resetRef, patch string, status iostream.StatusFunc) error {
	resetCmd := fmt.Sprintf("git -C %s reset --hard %s", ShellEscape(repoPath), resetRef)
	if result, err := ExecOverSSH(ctx, sess, resetCmd, nil, nil); err != nil {
		return fmt.Errorf("bundle sync: reset: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bundle sync: reset: %s", result.Stderr)
	}

	cleanCmd := fmt.Sprintf("git -C %s clean -fd", ShellEscape(repoPath))
	if result, err := ExecOverSSH(ctx, sess, cleanCmd, nil, nil); err != nil {
		return fmt.Errorf("bundle sync: clean: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bundle sync: clean: %s", result.Stderr)
	}

	if patch == "" {
		return nil
	}
	status(iostream.LevelInfo, fmt.Sprintf("Applying working-tree changes (%d bytes)...", len(patch)))
	applyCmd := fmt.Sprintf("git -C %s apply --whitespace=nowarn", ShellEscape(repoPath))
	if result, err := ExecOverSSH(ctx, sess, applyCmd, strings.NewReader(patch), nil); err != nil {
		return fmt.Errorf("bundle sync: apply patch: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bundle sync: apply patch: %s", result.Stderr)
	}
	return nil
}

// saveBundleSyncState persists sync metadata (ref, patch hash, patch content)
// and emits the final "Synced" status line. stateDir may be empty, in which
// case the patch file write is skipped (a warning is still emitted if SaveActive fails).
func saveBundleSyncState(ctx context.Context, active *ActiveSidecar, headRef, patchHash, stateDir, repoRoot, patch string, status iostream.StatusFunc) error {
	active.LastSyncedRef = headRef
	active.LastSyncedPatchHash = patchHash
	if err := SaveActive(ctx, *active); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not save last synced ref: %v", err))
	}
	if stateDir != "" {
		if err := saveSyncedPatch(ctx, stateDir, repoRoot, patch); err != nil {
			status(iostream.LevelWarn, fmt.Sprintf("Could not save synced patch: %v", err))
		}
	}
	status(iostream.LevelDone, "Synced")
	return nil
}

// patchFileName mirrors sidecarFileName but uses a .diff extension, giving
// each session+branch combination its own patch file alongside its sidecar.json.
func patchFileName(sessionID, branch string) string {
	name := sidecarFileName(sessionID, branch)
	if after, ok := strings.CutSuffix(name, ".json"); ok {
		return after + ".diff"
	}
	return name + ".diff"
}

// loadSyncedPatch reads the patch that was last applied for this session+branch.
// repoRoot must be the git root of the caller's repository so the branch lookup
// targets the correct repo regardless of the process working directory.
// Returns ("", false) when the file does not exist (unknown prior state).
// Returns ("", true) when the file exists but is empty (prior sync had no changes).
func loadSyncedPatch(ctx context.Context, dir, repoRoot string) (string, bool) {
	branch := CurrentBranch(repoRoot)
	path := filepath.Join(dir, patchFileName(session.IDFromCtx(ctx), branch))
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// saveSyncedPatch writes patch to the per-session+branch patch file in dir.
// repoRoot must be the git root of the caller's repository.
// Permissions are 0o600 because patch content may contain sensitive code.
func saveSyncedPatch(ctx context.Context, dir, repoRoot, patch string) error {
	branch := CurrentBranch(repoRoot)
	path := filepath.Join(dir, patchFileName(session.IDFromCtx(ctx), branch))
	return os.WriteFile(path, []byte(patch), 0o600)
}

// applyDeltaPatch reverses the previously applied patch then applies the new
// one. This avoids touching committed files so golangci-lint caches stay valid.
// oldPatch may be empty (previous sync had a clean working tree); in that case
// only the forward apply is performed. Returns an error if either git command
// fails; the caller should fall back to a full reset+clean+apply.
func applyDeltaPatch(ctx context.Context, sess *Session, repoPath, oldPatch, newPatch string, status iostream.StatusFunc) error {
	if oldPatch != "" {
		reverseCmd := fmt.Sprintf("git -C %s apply --reverse --whitespace=nowarn", ShellEscape(repoPath))
		if result, err := ExecOverSSH(ctx, sess, reverseCmd, strings.NewReader(oldPatch), nil); err != nil {
			return fmt.Errorf("reverse patch: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("reverse patch: %s", result.Stderr)
		}
	}
	if newPatch != "" {
		status(iostream.LevelInfo, fmt.Sprintf("Applying working-tree changes (%d bytes)...", len(newPatch)))
		applyCmd := fmt.Sprintf("git -C %s apply --whitespace=nowarn", ShellEscape(repoPath))
		if result, err := ExecOverSSH(ctx, sess, applyCmd, strings.NewReader(newPatch), nil); err != nil {
			return fmt.Errorf("apply patch: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("apply patch: %s", result.Stderr)
		}
	}
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
	if result, err := ExecOverSSH(ctx, session, "tee "+ShellEscape(bundlePath), bytes.NewReader(bundle), nil); err != nil {
		return fmt.Errorf("bundle sync: write bundle: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("bundle sync: write bundle: %s", result.Stderr)
	}

	fetchCmd := fmt.Sprintf("git -C %s fetch %s HEAD", ShellEscape(repoPath), ShellEscape(bundlePath))
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

	applyCmd := fmt.Sprintf("git -C %s apply --whitespace=nowarn", ShellEscape(repoPath))
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
