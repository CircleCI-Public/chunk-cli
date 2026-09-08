package sidecar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// bundleSyncFanOutConcurrency caps concurrent sidecar uploads during pool sync.
// A full bundle can be tens of MB, so unbounded fan-out can exhaust local
// memory or saturate the provider when hundreds of sidecars are prepared at once.
const bundleSyncFanOutConcurrency = 8

// sidecarHome returns the base home directory on the sidecar. It reads
// CHUNK_SIDECAR_HOME so the default "/home/user" can be overridden when the
// image uses a different OS user.
func sidecarHome() string {
	if h := os.Getenv("CHUNK_SIDECAR_HOME"); h != "" {
		return h
	}
	return "/home/user"
}

// DefaultWorkspace returns the default remote workspace path for a repo.
// Use this when creating pool sidecars to avoid inheriting a stale saved path.
func DefaultWorkspace(repo string) string {
	return sidecarHome() + "/" + repo
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
	return DefaultWorkspace(repo), nil
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
	return syncTo(ctx, client, sidecarID, identityFile, authSock, workdir, true, status)
}

// SyncEphemeral synchronises like Sync but neither reads nor writes the active
// sidecar file. Callers that drive several sidecars concurrently would otherwise
// race on that shared file and leave it naming whichever worker finished last.
// workdir is required for the same reason: there is no shared state to fall
// back on, so each caller must name its own destination.
func SyncEphemeral(ctx context.Context,
	client *circleci.Client, sidecarID, identityFile, authSock, workdir string, status iostream.StatusFunc) error {
	if workdir == "" {
		return fmt.Errorf("sync: workdir is required for an ephemeral sync")
	}
	return syncTo(ctx, client, sidecarID, identityFile, authSock, workdir, false, status)
}

// syncTo backs Sync and SyncEphemeral. persist controls whether the resolved
// workspace is read from and written back to the active-sidecar file.
func syncTo(ctx context.Context, client *circleci.Client,
	sidecarID, identityFile, authSock, workdir string, persist bool, status iostream.StatusFunc) error {

	session, err := OpenSession(ctx, client, sidecarID, identityFile, authSock, false)
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

	repoPath := workdir
	if persist {
		repoPath, err = ResolveWorkspace(ctx, workdir, repo)
		if err != nil {
			return err
		}
		if err := persistWorkspace(ctx, repoPath); err != nil {
			status(iostream.LevelWarn, fmt.Sprintf("Could not save workspace: %v", err))
		}
	}

	err = syncWorkspace(ctx, status, org, repo, repoPath, session)
	if err == nil {
		status(iostream.LevelDone, "Synced")
		return nil
	}
	if !errors.Is(err, errApplyFailed) {
		return err
	}

	status(iostream.LevelWarn, fmt.Sprintf("Local %s/%s drifted from remote: %s (%s) - attempting clean",
		org, repo, repoPath, err))

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
// using a git bundle, without requiring the branch to be pushed.
func BundleSync(ctx context.Context,
	client *circleci.Client, sidecarID, identityFile, authSock, workdir, cwd string, retryOn404 bool, status iostream.StatusFunc) error {
	prepared, err := prepareBundleSync(workdir, cwd, "", 1, status)
	if err != nil {
		return err
	}
	if err := syncPreparedSidecar(ctx, client, sidecarID, identityFile, authSock, retryOn404, prepared); err != nil {
		return err
	}
	if err := persistWorkspace(ctx, prepared.repoPath); err != nil {
		status(iostream.LevelWarn, fmt.Sprintf("Could not save workspace: %v", err))
	}
	status(iostream.LevelDone, "Synced")
	return nil
}

// BundleSyncFanOut synchronises a local working tree to multiple sidecars in
// parallel using a full or incremental bundle.
func BundleSyncFanOut(ctx context.Context, client *circleci.Client, sidecarIDs []string, identityFile, authSock, workdir, cwd string, retryOn404 bool, status iostream.StatusFunc) error {
	_, err := bundleSyncFanOutSince(ctx, client, sidecarIDs, identityFile, authSock, workdir, cwd, "", retryOn404, status)
	return err
}

type preparedBundleSync struct {
	repo     string
	repoPath string
	headRef  string
	resetRef string
	bundle   []byte
	patch    string
}

// bundleSyncFanOutSince synchronises a local working tree to multiple sidecars
// in parallel. It returns the local HEAD ref that all successful targets were
// synced to.
func bundleSyncFanOutSince(ctx context.Context, client *circleci.Client, sidecarIDs []string, identityFile, authSock, workdir, cwd, baseRef string, retryOn404 bool, status iostream.StatusFunc) (string, error) {
	prepared, err := prepareBundleSync(workdir, cwd, baseRef, len(sidecarIDs), status)
	if err != nil {
		return "", err
	}

	parallelism := len(sidecarIDs)
	if parallelism > bundleSyncFanOutConcurrency {
		parallelism = bundleSyncFanOutConcurrency
	}

	errs := make([]error, len(sidecarIDs))
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for i, id := range sidecarIDs {
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if err := syncPreparedSidecar(ctx, client, id, identityFile, authSock, retryOn404, prepared); err != nil {
				errs[i] = fmt.Errorf("sidecar %s: %w", id, err)
			}
		}(i, id)
	}
	wg.Wait()

	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	status(iostream.LevelDone, fmt.Sprintf("Synced %d sidecars", len(sidecarIDs)))
	return prepared.headRef, nil
}

func prepareBundleSync(workdir, cwd, baseRef string, sidecarCount int, status iostream.StatusFunc) (*preparedBundleSync, error) {
	_, repo, repoErr := gitremote.DetectOrgAndRepo(cwd)
	if repoErr != nil && workdir == "" {
		return nil, &NoOriginRemoteError{Err: repoErr}
	}

	repoPath := workdir
	if repoPath == "" {
		repoPath = DefaultWorkspace(repo)
	}

	headRef, err := gitutil.HeadRef(cwd)
	if err != nil {
		return nil, fmt.Errorf("fan-out sync: %w", err)
	}

	resetRef := gitHeadRef
	var bundle []byte
	if baseRef == headRef {
		status(iostream.LevelInfo, "No new commits since last sync.")
	} else {
		bundle, err = createBundle(baseRef, cwd)
		if err != nil {
			return nil, fmt.Errorf("fan-out sync: %w", err)
		}
		if baseRef == "" {
			status(iostream.LevelInfo, fmt.Sprintf("Bundle ready (%d bytes), syncing to %d sidecars...", len(bundle), sidecarCount))
		} else {
			status(iostream.LevelInfo, fmt.Sprintf("Incremental bundle ready (%d bytes), syncing to %d sidecars...", len(bundle), sidecarCount))
		}
		resetRef = "FETCH_HEAD"
	}

	patch, err := generatePatch(headRef, cwd)
	if err != nil {
		return nil, fmt.Errorf("fan-out sync: %w", err)
	}

	return &preparedBundleSync{
		repo:     repo,
		repoPath: repoPath,
		headRef:  headRef,
		resetRef: resetRef,
		bundle:   bundle,
		patch:    patch,
	}, nil
}

func syncPreparedSidecar(ctx context.Context, client *circleci.Client, sidecarID, identityFile, authSock string, retryOn404 bool, prepared *preparedBundleSync) error {
	sess, err := OpenSession(ctx, client, sidecarID, identityFile, authSock, retryOn404)
	if err != nil {
		return fmt.Errorf("open session: %w", err)
	}
	return applyBundleToSidecar(ctx, sess, prepared.repo, prepared.repoPath, prepared.bundle, prepared.resetRef, prepared.patch)
}

func applyBundleToSidecar(ctx context.Context, sess *Session, repo, repoPath string, bundle []byte, resetRef, patch string) error {
	if _, err := ensureRemoteRepo(ctx, sess, repoPath); err != nil {
		return err
	}
	if len(bundle) > 0 {
		if err := sendPreparedBundle(ctx, sess, repo, repoPath, bundle); err != nil {
			return err
		}
	}
	return resetCleanApply(ctx, sess, repoPath, resetRef, patch)
}

// ensureRemoteRepo ensures repoPath's parent exists and repoPath holds a git
// repo. It returns true when the repo was freshly initialised.
func ensureRemoteRepo(ctx context.Context, sess *Session, repoPath string) (bool, error) {
	parentDir := filepath.Dir(repoPath)
	if result, err := ExecOverSSH(ctx, sess, "mkdir -p "+ShellEscape(parentDir), nil, nil); err != nil {
		return false, fmt.Errorf("mkdir: %w", err)
	} else if result.ExitCode != 0 {
		return false, fmt.Errorf("mkdir -p %s: %s", parentDir, result.Stderr)
	}

	testResult, err := ExecOverSSH(ctx, sess, "test -d "+ShellEscape(repoPath+"/.git"), nil, nil)
	if err != nil {
		return false, fmt.Errorf("check repo: %w", err)
	}
	if testResult.ExitCode == 0 {
		return false, nil
	}
	if result, err := ExecOverSSH(ctx, sess, "git init "+ShellEscape(repoPath), nil, nil); err != nil {
		return false, fmt.Errorf("git init: %w", err)
	} else if result.ExitCode != 0 {
		return false, fmt.Errorf("git init: %s", result.Stderr)
	}
	return true, nil
}

func sendPreparedBundle(ctx context.Context, sess *Session, repo, repoPath string, bundle []byte) error {
	bundleName := repo
	if bundleName == "" {
		bundleName = "chunk"
	}
	bundlePath := fmt.Sprintf("/tmp/chunk-sync-%s.bundle", bundleName)
	if result, err := ExecOverSSH(ctx, sess, "tee "+ShellEscape(bundlePath), bytes.NewReader(bundle), nil); err != nil {
		return fmt.Errorf("write bundle: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("write bundle: %s", result.Stderr)
	}

	fetchCmd := fmt.Sprintf("git -C %s fetch %s HEAD", ShellEscape(repoPath), ShellEscape(bundlePath))
	if result, err := ExecOverSSH(ctx, sess, fetchCmd, nil, nil); err != nil {
		return fmt.Errorf("fetch: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("fetch: %s", result.Stderr)
	}
	return nil
}

func resetCleanApply(ctx context.Context, sess *Session, repoPath, resetRef, patch string) error {
	resetCmd := fmt.Sprintf("git -C %s reset --hard %s", ShellEscape(repoPath), resetRef)
	if result, err := ExecOverSSH(ctx, sess, resetCmd, nil, nil); err != nil {
		return fmt.Errorf("reset: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("reset: %s", result.Stderr)
	}

	cleanCmd := fmt.Sprintf("git -C %s clean -fd", ShellEscape(repoPath))
	if result, err := ExecOverSSH(ctx, sess, cleanCmd, nil, nil); err != nil {
		return fmt.Errorf("clean: %w", err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("clean: %s", result.Stderr)
	}

	if patch != "" {
		applyCmd := fmt.Sprintf("git -C %s apply --whitespace=nowarn", ShellEscape(repoPath))
		if result, err := ExecOverSSH(ctx, sess, applyCmd, strings.NewReader(patch), nil); err != nil {
			return fmt.Errorf("apply patch: %w", err)
		} else if result.ExitCode != 0 {
			return fmt.Errorf("apply patch: %s", result.Stderr)
		}
	}
	return nil
}

var errApplyFailed = errors.New("apply failed")

type RemoteBaseError struct {
	Err error
}

func (e *RemoteBaseError) Error() string {
	return fmt.Sprintf("resolve remote base: %v", e.Err)
}

func (e *RemoteBaseError) Unwrap() error {
	return e.Err
}

func syncWorkspace(ctx context.Context, status iostream.StatusFunc, org, repo, repoPath string, sess *Session) error {
	status(iostream.LevelInfo, fmt.Sprintf("Assessing %s/%s on remote: %s...", org, repo, repoPath))

	parentDir := filepath.Dir(repoPath)
	if result, err := ExecOverSSH(ctx, sess, "mkdir -p "+ShellEscape(parentDir), nil, nil); err != nil {
		return fmt.Errorf("sync: mkdir %s: %w", parentDir, err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("sync: mkdir -p %s: %s", parentDir, result.Stderr)
	}

	testResult, err := ExecOverSSH(ctx, sess, "test -d "+ShellEscape(repoPath), nil, nil)
	if err != nil {
		return fmt.Errorf("sync: check repo dir: %w", err)
	}
	if testResult.ExitCode != 0 {
		repoURL := fmt.Sprintf("https://github.com/%s/%s.git", org, repo)
		var cloneCmd string
		cwd := cwdOrDot()
		if branchPushed(cwd) {
			branch, err := gitutil.CurrentBranchIn(cwd)
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
		cloneResult, err := ExecOverSSH(ctx, sess, cloneCmd, nil, nil)
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
	fetchResult, err := ExecOverSSH(ctx, sess, fetchCmd, nil, nil)
	if err != nil {
		return fmt.Errorf("sync: fetch: %w", err)
	}
	if fetchResult.ExitCode != 0 {
		return fmt.Errorf("sync: fetch failed (exit code: %d): %s", fetchResult.ExitCode, fetchResult.Stderr)
	}

	base, err := mergeBase(cwdOrDot())
	if err != nil {
		return &RemoteBaseError{Err: err}
	}

	patch, err := generatePatch(base, cwdOrDot())
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
	resetResult, err := ExecOverSSH(ctx, sess, resetCmd, nil, nil)
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
	applyResult, err := ExecOverSSH(ctx, sess, applyCmd, strings.NewReader(patch), nil)
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

func createBundle(base, cwd string) ([]byte, error) {
	var args []string
	if base == "" {
		args = []string{"bundle", "create", "-", gitHeadRef}
	} else {
		args = []string{"bundle", "create", "-", base + "..HEAD"}
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("create bundle: %w", err)
	}
	return out, nil
}

func generatePatch(base, cwd string) (string, error) {
	lsCmd := exec.Command("git", "ls-files", "--others", "--exclude-standard")
	lsCmd.Dir = cwd
	lsOut, err := lsCmd.Output()
	if err != nil {
		return "", fmt.Errorf("list untracked files: %w", err)
	}

	untracked := splitNonEmpty(strings.TrimSpace(string(lsOut)))
	if len(untracked) > 0 {
		args := append([]string{"add", "-N", "--"}, untracked...)
		addCmd := exec.Command("git", args...)
		addCmd.Dir = cwd
		if err := addCmd.Run(); err != nil {
			return "", fmt.Errorf("stage untracked files: %w", err)
		}
		defer func() {
			resetArgs := append([]string{"reset", gitHeadRef, "--"}, untracked...)
			resetCmd := exec.Command("git", resetArgs...)
			resetCmd.Dir = cwd
			_ = resetCmd.Run()
		}()
	}

	diffCmd := exec.Command("git", "diff", base, "--binary")
	diffCmd.Dir = cwd
	out, err := diffCmd.Output()
	if err != nil {
		return "", fmt.Errorf("generate diff: %w", err)
	}
	return string(out), nil
}

func branchPushed(cwd string) bool {
	branch, err := gitutil.CurrentBranchIn(cwd)
	if err != nil {
		return false
	}
	ref := "refs/remotes/origin/" + branch
	cmd := exec.Command("git", "rev-parse", "--verify", ref)
	cmd.Dir = cwd
	return cmd.Run() == nil
}

func mergeBase(cwd string) (string, error) {
	cmd := exec.Command("git", "merge-base", "@{upstream}", "origin/HEAD")
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err == nil {
		sha := strings.TrimSpace(string(out))
		if sha != "" {
			return sha, nil
		}
	}

	cmd = exec.Command("git", "rev-parse", "origin/HEAD")
	cmd.Dir = cwd
	out, err = cmd.Output()
	if err != nil {
		verifyCmd := exec.Command("git", "rev-parse", "--verify", "@{upstream}")
		verifyCmd.Dir = cwd
		if verifyCmd.Run() == nil {
			return "", fmt.Errorf("origin/HEAD is not set")
		}
		return "", fmt.Errorf("no upstream tracking branch or origin/HEAD found")
	}
	sha := strings.TrimSpace(string(out))
	if sha == "" {
		return "", fmt.Errorf("origin/HEAD is empty")
	}
	return sha, nil
}

func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, "\n")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func cwdOrDot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}
