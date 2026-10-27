package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

// planWorktreesDir is where a repository's plans keep their worktrees, under
// chunk's data directory for it.
func planWorktreesDir(root string) (string, error) {
	dataDir, err := runsDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dataDir, "plans"), nil
}

// planEnds are the states of a factory plan that is over, merged or dropped.
var planEnds = []string{"Done", "Canceled", "Won't Do", "Duplicate"}

// PlanEnded is whether a factory plan in state is over, so nothing more
// happens to it: its worktree can go, and a session building it can stop.
func PlanEnded(state string) bool { return slices.Contains(planEnds, state) }

// PlanWorktreePath is where the worktree of factory plan planID goes, for the
// repository at root. It is the same for every session planning it, so a
// session resumed there finds its conversation.
func PlanWorktreePath(root, planID string) (string, error) {
	if planID == "" || planID != filepath.Base(planID) || strings.HasPrefix(planID, ".") {
		return "", fmt.Errorf("plan ID %q cannot name a directory", planID)
	}
	dir, err := planWorktreesDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, planID), nil
}

// OpenPlanWorktree returns the worktree of factory plan planID, adding it from
// a fresh fetch of the remote's default branch when there is none. One whose
// directory exists is used as it is, but for copying in a chunk config it is
// missing. A new worktree starts detached, so it holds no branch.
func OpenPlanWorktree(ctx context.Context, root, planID string) (string, error) {
	path, err := PlanWorktreePath(root, planID)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err == nil {
		if err := copyProjectConfig(root, path); err != nil {
			return "", err
		}
		return path, nil
	}
	git := gitexec.Runner{Dir: root}
	// git still lists a worktree whose directory was deleted, and refuses to
	// add one at its path until it forgets it.
	_ = git.Run(ctx, "worktree", "prune")
	start, err := planStart(ctx, root)
	if err != nil {
		return "", err
	}
	if out, err := git.CombinedOutput(ctx, "worktree", "add", "--quiet", "--detach", path, start); err != nil {
		return "", fmt.Errorf("add the plan's worktree: %w: %s", err, strings.TrimSpace(string(out)))
	}
	if err := copyProjectConfig(root, path); err != nil {
		return "", err
	}
	return path, nil
}

// planFetchTimeout bounds asking origin for its default branch and fetching
// it, so a network that stalls falls back to the tip last fetched.
const planFetchTimeout = 30 * time.Second

// planStart is the commit a new plan's worktree starts from: the tip of the
// remote's default branch, fetched first. A failed fetch, offline say, falls
// back to the tip last fetched.
func planStart(ctx context.Context, root string) (string, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, planFetchTimeout)
	defer cancel()
	remote, branch, err := gitutil.DefaultRemoteBranchIn(root)
	if err != nil {
		// A repository given its origin with git remote add has no remote
		// HEAD, so ask origin which branch is its default.
		remote, branch = "origin", remoteHead(fetchCtx, root, "origin")
	}
	fetchErr := gitutil.FetchRemoteBranch(fetchCtx, root, remote, branch)
	sha, err := gitutil.RevParseCtx(ctx, root, remote+"/"+branch)
	if err != nil {
		if fetchErr != nil {
			return "", fmt.Errorf("find %s/%s to plan from: %w", remote, branch, fetchErr)
		}
		return "", fmt.Errorf("find %s/%s to plan from: %w", remote, branch, err)
	}
	return sha, nil
}

// remoteHead is the default branch remote reports for itself, or main when it
// cannot be asked.
func remoteHead(ctx context.Context, root, remote string) string {
	git := gitexec.Runner{Dir: root, Env: append(os.Environ(), "GIT_TERMINAL_PROMPT=0")}
	out, err := git.Output(ctx, "ls-remote", "--symref", remote, "HEAD")
	if err != nil {
		return "main"
	}
	// The symref comes first: "ref: refs/heads/<branch>\tHEAD".
	for line := range strings.Lines(string(out)) {
		ref, ok := strings.CutPrefix(line, "ref: refs/heads/")
		if !ok {
			continue
		}
		if branch, _, ok := strings.Cut(ref, "\t"); ok && branch != "" {
			return branch
		}
	}
	return "main"
}

var projectConfig = filepath.Join(".chunk", "config.json")

// copyProjectConfig copies the developer's .chunk/config.json into the
// worktree at path when it has none, as when the file is not committed.
func copyProjectConfig(root, path string) error {
	rel := projectConfig
	if _, err := os.Stat(filepath.Join(path, rel)); err == nil {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // G304: the project's own config
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read the project's chunk config: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(path, ".chunk"), 0o755); err != nil {
		return fmt.Errorf("copy the project's chunk config to the plan's worktree: %w", err)
	}
	if err := os.WriteFile(filepath.Join(path, rel), b, 0o600); err != nil {
		return fmt.Errorf("copy the project's chunk config to the plan's worktree: %w", err)
	}
	return nil
}

// PrunePlanWorktrees removes the worktree of each plan of the repository at
// root that ended says has ended. A worktree with changes, or whose plan's
// state could not be read, is left for a later prune. Its branch, if factory
// made one, is left too.
func PrunePlanWorktrees(ctx context.Context, root string, ended func(ctx context.Context, planID string) (bool, error)) error {
	dir, err := planWorktreesDir(root)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list the plans' worktrees: %w", err)
	}
	var errs []error
	git := gitexec.Runner{Dir: root}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		over, err := ended(ctx, e.Name())
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !over {
			continue
		}
		path := filepath.Join(dir, e.Name())
		dropPlanSession(root, e.Name())
		dropCopiedConfig(ctx, path)
		if out, err := git.CombinedOutput(ctx, "worktree", "remove", path); err != nil {
			errs = append(errs, fmt.Errorf("remove the worktree of plan %s: %w: %s", e.Name(), err, strings.TrimSpace(string(out))))
		}
	}
	return errors.Join(errs...)
}

// dropCopiedConfig deletes the chunk config copyProjectConfig put in the
// worktree at path, when it is not committed there: git refuses to remove a
// worktree with an untracked file in it.
func dropCopiedConfig(ctx context.Context, path string) {
	if err := (gitexec.Runner{Dir: path}).Run(ctx, "ls-files", "--error-unmatch", projectConfig); err == nil {
		return
	}
	_ = os.Remove(filepath.Join(path, projectConfig))
	_ = os.Remove(filepath.Join(path, ".chunk")) // only when copying made it
}
