package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// WorktreesDir is where run worktrees are created, relative to the repo root.
const WorktreesDir = ".chunk/worktrees"

// DiffScript prints everything the worker changed since base, committed or
// not, as a binary patch. Staging first brings new files into the diff; the
// index it touches is the sidecar's own.
func DiffScript(repoPath, base string) string {
	git := "git -C " + sidecar.ShellEscape(repoPath)
	return fmt.Sprintf("%s add -A && %s diff --cached --binary %s", git, git, sidecar.ShellEscape(base))
}

// Worktree is the local checkout a run works in, on its own branch, so the
// developer's working tree is never touched.
type Worktree struct {
	Dir    string
	Branch string
	// Base is the commit the worktree started from.
	Base string
}

// CreateWorktree adds a worktree for a run at HEAD of repoRoot, under
// WorktreesDir, on a new branch named for the run.
func CreateWorktree(ctx context.Context, repoRoot string, now time.Time) (Worktree, error) {
	base, err := HeadCommit(ctx, repoRoot)
	if err != nil {
		return Worktree{}, err
	}
	stamp := now.UTC().Format("20060102-150405")
	wt := Worktree{
		Dir:    filepath.Join(repoRoot, WorktreesDir, stamp),
		Branch: "chunk/factory-" + stamp,
		Base:   base,
	}

	// Ignore the worktrees from inside their own directory, so no repository
	// needs a .gitignore entry for them.
	root := filepath.Join(repoRoot, WorktreesDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return Worktree{}, fmt.Errorf("create %s: %w", WorktreesDir, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*\n"), 0o644); err != nil {
		return Worktree{}, fmt.Errorf("write %s/.gitignore: %w", WorktreesDir, err)
	}

	if out, err := (gitexec.Runner{Dir: repoRoot}).CombinedOutput(ctx, "worktree", "add", "-b", wt.Branch, wt.Dir, wt.Base); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := excludePoolState(ctx, wt.Dir); err != nil {
		return Worktree{}, err
	}
	return wt, nil
}

// poolStateExclude matches the pool state files sidecar pools write into the
// worktree, which must stay out of every sync and every attempt's commit.
const poolStateExclude = ".chunk/*-pool.json"

// excludePoolState adds poolStateExclude to the repository's info/exclude,
// which every worktree shares, unless it is already there.
func excludePoolState(ctx context.Context, dir string) error {
	out, err := (gitexec.Runner{Dir: dir}).Output(ctx, "rev-parse", "--path-format=absolute", "--git-path", "info/exclude")
	if err != nil {
		return fmt.Errorf("find info/exclude: %w", err)
	}
	path := strings.TrimSpace(string(out))
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if slices.Contains(strings.Split(string(existing), "\n"), poolStateExclude) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	content := strings.TrimRight(string(existing), "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content+poolStateExclude+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// HeadCommit returns the commit dir has checked out.
func HeadCommit(ctx context.Context, dir string) (string, error) {
	out, err := (gitexec.Runner{Dir: dir}).Output(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ApplyPatch applies a patch produced by DiffScript to dir's working tree.
func ApplyPatch(ctx context.Context, dir, patch string) error {
	if strings.TrimSpace(patch) == "" {
		return nil
	}
	cmd := exec.CommandContext(ctx, "git", "apply", "--binary", "--whitespace=nowarn")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(patch)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("git apply: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ErrNothingToCommit is returned by CommitAll when dir has no changes.
var ErrNothingToCommit = errors.New("nothing to commit")

// CommitAll commits every change in dir, new files included. Hooks are
// skipped: the run's own checks happen on sidecars, not in a pre-commit hook.
func CommitAll(ctx context.Context, dir, message string) error {
	git := gitexec.Runner{Dir: dir}
	out, err := git.Output(ctx, "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return ErrNothingToCommit
	}
	if out, err := git.CombinedOutput(ctx, "add", "-A"); err != nil {
		return fmt.Errorf("git add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if out, err := git.CombinedOutput(ctx, "commit", "--no-verify", "-q", "-m", message); err != nil {
		return fmt.Errorf("git commit: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
