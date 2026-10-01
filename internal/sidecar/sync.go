package sidecar

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// syncFanOutConcurrency caps concurrent sidecar syncs during pool sync, so a
// large pool does not open an SSH proxy and walk the tree for every sidecar at
// once.
const syncFanOutConcurrency = 8

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
	client *circleci.Client, sidecarID, workdir string, status iostream.StatusFunc) error {
	return syncTo(ctx, client, sidecarID, workdir, true, status)
}

// SyncEphemeral synchronises like Sync but neither reads nor writes the active
// sidecar file. Callers that drive several sidecars concurrently would otherwise
// race on that shared file and leave it naming whichever worker finished last.
// workdir is required for the same reason: there is no shared state to fall
// back on, so each caller must name its own destination.
func SyncEphemeral(ctx context.Context,
	client *circleci.Client, sidecarID, workdir string, status iostream.StatusFunc) error {
	if workdir == "" {
		return fmt.Errorf("sync: workdir is required for an ephemeral sync")
	}
	return syncTo(ctx, client, sidecarID, workdir, false, status)
}

// syncTo backs Sync and SyncEphemeral. persist controls whether the resolved
// workspace is read from and written back to active-pool state.
func syncTo(ctx context.Context, client *circleci.Client,
	sidecarID, workdir string, persist bool, status iostream.StatusFunc) error {

	session, err := OpenSession(ctx, client, sidecarID, false)
	if err != nil {
		return err
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	org, repo, err := gitremote.DetectOrgAndRepoCtx(ctx, cwd)
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
		pushed, err := branchPushed(ctx, cwd)
		if err != nil {
			return fmt.Errorf("sync: check pushed branch: %w", err)
		}
		if pushed {
			branch, err := gitutil.CurrentBranchInCtx(ctx, cwd)
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

	base, err := mergeBase(ctx, cwdOrDot())
	if err != nil {
		return &RemoteBaseError{Err: err}
	}

	patch, err := generatePatch(ctx, base, cwdOrDot())
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

func generatePatch(ctx context.Context, base, cwd string) (string, error) {
	git := gitexec.Runner{Dir: cwd}
	lsOut, err := git.Output(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return "", fmt.Errorf("list untracked files: %w", err)
	}

	untracked := splitNonEmpty(strings.TrimSpace(string(lsOut)))
	if len(untracked) > 0 {
		args := append([]string{"add", "-N", "--"}, untracked...)
		if err := git.Run(ctx, args...); err != nil {
			return "", fmt.Errorf("stage untracked files: %w", err)
		}
		defer func() {
			resetArgs := append([]string{"reset", gitHeadRef, "--"}, untracked...)
			_ = git.Run(context.WithoutCancel(ctx), resetArgs...)
		}()
	}

	out, err := git.Output(ctx, "diff", base, "--binary")
	if err != nil {
		return "", fmt.Errorf("generate diff: %w", err)
	}
	return string(out), nil
}

func branchPushed(ctx context.Context, cwd string) (bool, error) {
	branch, err := gitutil.CurrentBranchInCtx(ctx, cwd)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return false, ctxErr
		}
		return false, nil
	}
	ref := "refs/remotes/origin/" + branch
	err = (gitexec.Runner{Dir: cwd}).Run(ctx, "rev-parse", "--verify", ref)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return false, ctxErr
	}
	return err == nil, nil
}

func mergeBase(ctx context.Context, cwd string) (string, error) {
	git := gitexec.Runner{Dir: cwd}
	out, err := git.Output(ctx, "merge-base", "@{upstream}", "origin/HEAD")
	if err == nil {
		sha := strings.TrimSpace(string(out))
		if sha != "" {
			return sha, nil
		}
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return "", ctxErr
	}

	out, err = git.Output(ctx, "rev-parse", "origin/HEAD")
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
		}
		if git.Run(ctx, "rev-parse", "--verify", "@{upstream}") == nil {
			return "", fmt.Errorf("origin/HEAD is not set")
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return "", ctxErr
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
