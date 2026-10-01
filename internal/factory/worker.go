package factory

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// ExitClaudeMissing is the worker script's exit code for claude not being on
// PATH, matching the review scripts'.
const ExitClaudeMissing = 97

// WorktreesDir is where run worktrees are created, relative to the repo root.
const WorktreesDir = ".chunk/worktrees"

// WorkScript runs the worker agent on a sidecar. Unlike a reviewer, the worker
// edits the tree, so it runs with permission checks off: the sidecar is an
// ephemeral machine holding nothing but a copy of the repository. The prompt is
// piped base64-encoded so no quoting in it reaches the shell.
func WorkScript(repoPath, prompt, model string) string {
	args := []string{"claude", "-p", "--output-format", "text", "--dangerously-skip-permissions"}
	if model != "" {
		args = append(args, "--model", model)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(prompt))
	return fmt.Sprintf(`export PATH="$HOME/.local/bin:$PATH"
command -v claude >/dev/null 2>&1 || exit %d
cd %s && echo %s | base64 -d | %s`,
		ExitClaudeMissing, sidecar.ShellEscape(repoPath), encoded, sidecar.ShellJoin(args))
}

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
	git := gitexec.Runner{Dir: repoRoot}
	out, err := git.Output(ctx, "rev-parse", "HEAD")
	if err != nil {
		return Worktree{}, fmt.Errorf("resolve HEAD: %w", err)
	}
	stamp := now.UTC().Format("20060102-150405")
	wt := Worktree{
		Dir:    filepath.Join(repoRoot, WorktreesDir, stamp),
		Branch: "chunk/factory-" + stamp,
		Base:   strings.TrimSpace(string(out)),
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

	if out, err := git.CombinedOutput(ctx, "worktree", "add", "-b", wt.Branch, wt.Dir, wt.Base); err != nil {
		return Worktree{}, fmt.Errorf("git worktree add: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := excludePoolState(ctx, wt.Dir); err != nil {
		return Worktree{}, err
	}
	return wt, nil
}

// poolStateExclude matches the pool state files sidecar pools write into the
// worktree, which must stay out of every attempt's commit.
const poolStateExclude = ".chunk/*-pool.json"

// excludePoolState adds poolStateExclude to the repository's info/exclude,
// which every worktree shares, unless it is already there.
func excludePoolState(ctx context.Context, dir string) error {
	out, err := (gitexec.Runner{Dir: dir}).Output(ctx, "rev-parse", "--git-common-dir")
	if err != nil {
		return fmt.Errorf("find git dir: %w", err)
	}
	commonDir := strings.TrimSpace(string(out))
	if !filepath.IsAbs(commonDir) {
		commonDir = filepath.Join(dir, commonDir)
	}
	path := filepath.Join(commonDir, "info", "exclude")
	existing, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read %s: %w", path, err)
	}
	for line := range strings.SplitSeq(string(existing), "\n") {
		if strings.TrimSpace(line) == poolStateExclude {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	prefix := ""
	if len(existing) > 0 && !strings.HasSuffix(string(existing), "\n") {
		prefix = "\n"
	}
	if _, err := fmt.Fprintf(f, "%s%s\n", prefix, poolStateExclude); err != nil {
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
