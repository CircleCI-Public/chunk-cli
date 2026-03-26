package sandbox

import (
	"context"
	"fmt"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// Sync synchronises local changes to a sandbox over SSH.
// If bootstrap is true it clones the repo on the sandbox first.
func Sync(ctx context.Context, client *circleci.Client, sandboxID, identityFile, authSock, dest string, bootstrap bool, io iostream.Streams) error {
	session, err := OpenSession(ctx, client, sandboxID, identityFile, authSock)
	if err != nil {
		return err
	}

	if bootstrap {
		if err := bootstrapSandbox(ctx, session, dest, io); err != nil {
			return err
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
		ShellEscape(dest), ShellEscape(base),
	)

	resetResult, err := ExecOverSSH(ctx, session, resetCmd, nil)

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

	applyCmd := fmt.Sprintf(
		`git -C %s apply`, ShellEscape(dest),
	)

	applyResult, err := ExecOverSSH(ctx, session, applyCmd, strings.NewReader(patch))
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

func bootstrapSandbox(ctx context.Context, session *Session, dest string, io iostream.Streams) error {
	org, repo, err := gitremote.DetectOrgAndRepo()
	if err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}
	repoURL := fmt.Sprintf("https://github.com/%s/%s.git", org, repo)

	branch, err := gitutil.CurrentBranch()
	if err != nil {
		return fmt.Errorf("bootstrap failed: %w", err)
	}

	var initCmd string
	if gitutil.IsBranchPushed() {
		initCmd = fmt.Sprintf("git clone --branch %s %s %s",
			ShellEscape(branch), ShellEscape(repoURL), ShellEscape(dest),
		)
	} else {
		io.ErrPrintf("%s\n", ui.Dim("Branch not pushed to remote; cloning default branch instead."))
		initCmd = fmt.Sprintf("git clone %s %s",
			ShellEscape(repoURL), ShellEscape(dest),
		)
	}

	io.ErrPrintf("%s\n", ui.Dim(fmt.Sprintf("Cloning %s/%s into %s...", org, repo, dest)))
	result, err := ExecOverSSH(ctx, session, initCmd, nil)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		detail := result.Stderr
		if detail == "" {
			detail = "git clone exited with a non-zero status"
		}
		return fmt.Errorf("bootstrap failed: %s", detail)
	}
	return nil
}
