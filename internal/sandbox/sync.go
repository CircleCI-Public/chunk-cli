package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

const workspaceDir = "/workspace"

// Sync synchronises local changes to a sandbox over SSH.
// It ensures the workspace base exists, clones the repo into workdir if absent,
// then resets to the remote base and applies a patch of local changes.
// workdir overrides the destination path; defaults to /workspace/<repo>.
func Sync(ctx context.Context, client *circleci.Client, sandboxID, identityFile, authSock, workdir string, io iostream.Streams) error {
	session, err := OpenSession(ctx, client, sandboxID, identityFile, authSock)
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

	repoPath := workdir
	if repoPath == "" {
		repoPath = workspaceDir + "/" + repo
	}

	// Ensure the parent directory exists on the sandbox.
	parentDir := filepath.Dir(repoPath)
	if result, err := ExecOverSSH(ctx, session, "mkdir -p "+ShellEscape(parentDir), nil, nil); err != nil {
		return fmt.Errorf("sync: mkdir %s: %w", parentDir, err)
	} else if result.ExitCode != 0 {
		return fmt.Errorf("sync: mkdir -p %s: %s", parentDir, result.Stderr)
	}

	// Clone into /workspace/<repo> if not already present.
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
			io.ErrPrintf("%s\n", ui.Dim("Branch not pushed to remote; cloning default branch instead."))
			cloneCmd = fmt.Sprintf("git clone %s %s",
				ShellEscape(repoURL), ShellEscape(repoPath),
			)
		}

		io.ErrPrintf("%s\n", ui.Dim(fmt.Sprintf("Cloning %s/%s into %s...", org, repo, repoPath)))
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

	base, err := gitutil.MergeBase()
	if err != nil {
		return fmt.Errorf("could not resolve remote base: %w\nPush your branch or ensure the repository has a remote configured", err)
	}

	patch, err := gitutil.GeneratePatch(base)
	if err != nil {
		return err
	}
	if patch == "" {
		io.ErrPrintln(ui.Dim("No local changes relative to remote base."))
		return nil
	}

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
		return fmt.Errorf("sync failed: %s", detail)
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
		return fmt.Errorf("sync failed: %s", detail)
	}

	io.ErrPrintln(ui.Success("Synced."))
	return nil
}
