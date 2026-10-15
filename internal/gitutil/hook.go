package gitutil

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
)

// ErrNotGitRepo is returned when a directory is not in a git repository.
var ErrNotGitRepo = errors.New("not a git repository")

// CommonDirCtx returns the shared git directory of the repository dir is in,
// as an absolute path. It is where hooks live, even from a worktree.
func CommonDirCtx(ctx context.Context, dir string) (string, error) {
	git := gitexec.Runner{Dir: dir}
	if err := git.Run(ctx, "rev-parse", "--git-dir"); err != nil {
		return "", fmt.Errorf("%w: %w", ErrNotGitRepo, err)
	}

	out, err := git.Output(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("git rev-parse --git-common-dir: %w", err)
	}
	common := trimNewline(out)
	if !filepath.IsAbs(common) {
		// From a subdirectory git answers relative to its own working
		// directory, which is dir with its symlinks resolved. Joining to dir
		// as given lands in the wrong place when a symlink stands for a
		// directory deeper than itself.
		base := dir
		if real, err := filepath.EvalSymlinks(dir); err == nil {
			base = real
		}
		common = filepath.Join(base, common)
	}
	return common, nil
}

// HookOutcome is what EnsurePreCommit did to the pre-commit hook.
type HookOutcome int

const (
	// HookCreated means there was no pre-commit hook and one was written.
	HookCreated HookOutcome = iota
	// HookAppended means the command was added to the end of the hook.
	HookAppended
	// HookUpToDate means the hook already ran the command.
	HookUpToDate
	// HookSymlink means the hook is a symlink, so it was left alone.
	HookSymlink
)

// EnsurePreCommit makes the repository's pre-commit hook run command.
// commonDir must be the path CommonDirCtx returns, so the hook lands in the
// shared hooks directory even from a worktree. A hook that already mentions
// command is left as it is; any other hook has command appended, keeping its
// permissions. A hook that is a symlink belongs to a hook manager (husky,
// lefthook), and is left alone too: writing through the path would replace the
// symlink with a regular file and break the manager's setup.
func EnsurePreCommit(commonDir, command string) (HookOutcome, error) {
	hooksDir := filepath.Join(commonDir, "hooks")
	if err := os.MkdirAll(hooksDir, 0o755); err != nil {
		return 0, fmt.Errorf("create git hooks directory: %w", err)
	}

	path := filepath.Join(hooksDir, "pre-commit")
	existing, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"+command+"\n"), 0o755); err != nil {
			return 0, fmt.Errorf("write pre-commit hook: %w", err)
		}
		return HookCreated, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read pre-commit hook: %w", err)
	}

	if strings.Contains(string(existing), command) {
		return HookUpToDate, nil
	}

	info, err := os.Lstat(path)
	if err != nil {
		return 0, fmt.Errorf("stat pre-commit hook: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return HookSymlink, nil
	}

	content := string(existing)
	if len(content) > 0 && content[len(content)-1] != '\n' {
		content += "\n"
	}
	content += command + "\n"
	if err := os.WriteFile(path, []byte(content), info.Mode()); err != nil {
		return 0, fmt.Errorf("write pre-commit hook: %w", err)
	}
	return HookAppended, nil
}
