// Package localprune finds and removes chunk's leftover local files: state
// written to paths chunk no longer reads as its primary source, now that
// state lives under a per-checkout project data directory and a
// per-repository repo data directory. It only deletes; it never migrates
// anything still live at an old path into the new one.
package localprune

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/validate"
)

// Item is one path Scan found to remove, with why.
type Item struct {
	Path   string
	Reason string
	// WorktreeRoot is the git repository Apply must run `git worktree remove`
	// in before deleting Path, for an Item that is a factory run's worktree.
	// Empty for every other kind of Item.
	WorktreeRoot string
}

// Plan is what Scan found. Nothing is removed until Apply runs.
type Plan struct {
	Items []Item
}

func (p *Plan) add(path, reason string) {
	p.Items = append(p.Items, Item{Path: path, Reason: reason})
}

func (p *Plan) addWorktree(root, path, reason string) {
	p.Items = append(p.Items, Item{Path: path, Reason: reason, WorktreeRoot: root})
}

// Apply deletes every path in the plan. It keeps going when one removal
// fails, so one bad path does not stop the rest of the clean-up.
func (p *Plan) Apply(ctx context.Context) error {
	var errs []error
	for _, it := range p.Items {
		if it.WorktreeRoot != "" {
			if err := removeWorktree(ctx, it.WorktreeRoot, it.Path); err != nil {
				errs = append(errs, fmt.Errorf("remove %s: %w", it.Path, err))
			}
			continue
		}
		if err := os.RemoveAll(it.Path); err != nil {
			errs = append(errs, fmt.Errorf("remove %s: %w", it.Path, err))
		}
	}
	return errors.Join(errs...)
}

// removeWorktree removes a factory run's worktree the way factory itself
// would, through `git worktree remove`, so the repository's
// .git/worktrees/<id> entry goes with it rather than surviving to make a
// later `git branch -d` fail with "used by worktree". If that fails, most
// likely because the directory was already removed by hand, path is deleted
// outright and `git worktree prune` clears any registration left pointing at
// it. The run's branch is never touched: a discarded run's branch is already
// gone, and a pulled run's branch is the developer's to keep or delete.
func removeWorktree(ctx context.Context, root, path string) error {
	git := gitexec.Runner{Dir: root}
	if err := git.Run(ctx, "worktree", "remove", "--force", path); err == nil {
		return nil
	}
	if err := os.RemoveAll(path); err != nil {
		return err
	}
	_ = git.Run(ctx, "worktree", "prune")
	return nil
}

// Scan finds chunk's leftover local files. It touches nothing; it only
// builds the Plan that Apply would carry out.
func Scan(ctx context.Context) (*Plan, error) {
	plan := &Plan{}
	if err := scanProjects(ctx, plan); err != nil {
		return nil, err
	}
	if err := scanHome(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

// scanProjects walks every per-checkout project data directory, skipping the
// repo-* data directories: those hold the current locations for factory
// records and the validate cache, and are never leftovers.
func scanProjects(ctx context.Context, plan *Plan) error {
	base, err := config.AppData()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), "repo-") {
			continue
		}
		dir := filepath.Join(base, e.Name())
		if root, ok := projectRoot(dir); ok {
			scanProject(ctx, root, dir, plan)
			continue
		}
		scanOrphan(dir, plan)
	}
	return nil
}

// projectRoot reads dir's project-root breadcrumb and reports the root it
// names, and whether that root still exists.
func projectRoot(dir string) (string, bool) {
	data, err := os.ReadFile(sidecar.ProjectRootPath(dir))
	if err != nil {
		return "", false
	}
	root := strings.TrimSpace(string(data))
	if root == "" {
		return "", false
	}
	if _, err := os.Stat(root); err != nil {
		return "", false
	}
	return root, true
}

// scanOrphan plans the removal of a project data directory whose project is
// gone: no breadcrumb, or a breadcrumb naming a root that no longer exists.
// A directory still holding factory state is left alone; its project may be
// unreachable only because the breadcrumb or root is temporarily wrong, and
// a worktree or record inside it may be the only copy of unfinished work.
func scanOrphan(dir string, plan *Plan) {
	entries, err := os.ReadDir(filepath.Join(dir, "factory"))
	if err == nil && len(entries) > 0 {
		return
	}
	plan.add(dir, "project data for a project that no longer exists")
}

// scanProject plans the removal of root's leftovers in its project data
// directory dir.
func scanProject(ctx context.Context, root, dir string, plan *Plan) {
	scanLegacyPools(root, dir, plan)
	scanLegacyReviewRun(root, dir, plan)
	scanLegacyHooksDisabled(root, plan)
	scanValidateCache(dir, plan)
	scanLegacySidecarFiles(dir, plan)
	scanLegacyFactory(ctx, root, dir, plan)
}

// scanLegacyPools plans the removal of pool state left in root's .chunk/
// directory, where chunk kept it before pools moved to the project data
// directory. A legacy file is only removed once its migrated copy exists at
// the new path; otherwise it is still the only copy of that pool's state.
func scanLegacyPools(root, dir string, plan *Plan) {
	matches, _ := filepath.Glob(filepath.Join(root, ".chunk", "*-pool.json"))
	for _, legacy := range matches {
		name := strings.TrimSuffix(filepath.Base(legacy), "-pool.json")
		current := filepath.Join(dir, "pools", name+".json")
		if fileExists(current) {
			plan.add(legacy, "pool state now kept at "+current)
		}
	}
}

// scanLegacyReviewRun plans the removal of a detached review's state at its
// old path inside root, once its migrated copy exists at the new one.
func scanLegacyReviewRun(root, dir string, plan *Plan) {
	legacy := filepath.Join(root, ".chunk", "review-run.json")
	current := filepath.Join(dir, "review-run.json")
	if fileExists(legacy) && fileExists(current) {
		plan.add(legacy, "detached review state now kept at "+current)
	}
}

// scanLegacyHooksDisabled plans the removal of the hooks-disabled sentinel at
// its old path inside root, once its migrated copy exists at the new one.
func scanLegacyHooksDisabled(root string, plan *Plan) {
	paths, err := validate.HooksDisabledPaths(root)
	if err != nil || len(paths) < 2 {
		return
	}
	current, legacy := paths[0], paths[1]
	if fileExists(legacy) && fileExists(current) {
		plan.add(legacy, "hooks-disabled sentinel now kept at "+current)
	}
}

// scanValidateCache plans the removal of the validate result cache at its old
// per-checkout location. It is a cache, so dropping it is always safe; the
// current cache lives in the repository data directory instead.
func scanValidateCache(dir string, plan *Plan) {
	old := filepath.Join(dir, "validate-cache")
	if dirExists(old) {
		plan.add(old, "validate cache now kept in the repository data directory")
	}
}

// scanLegacySidecarFiles plans the removal of sidecar sync state left by
// versions of chunk before per-session sidecars existed.
//
// sidecar.diff is from a patch-based sync chunk no longer performs, so
// wherever it survives it is dead weight; its sidecar.json sibling is removed
// with it, since together they are what that mechanism wrote, not today's
// no-session fallback. A lone sidecar.json without a sidecar.diff is that
// fallback still in use and is left alone.
//
// A per-session sidecar.<id>.diff is the same dead mechanism keyed by
// session; one with no matching sidecar.<id>.json has clearly outlived
// whatever it was paired with and is removed.
func scanLegacySidecarFiles(dir string, plan *Plan) {
	bareJSON := filepath.Join(dir, "sidecar.json")
	bareDiff := filepath.Join(dir, "sidecar.diff")
	if fileExists(bareDiff) {
		plan.add(bareDiff, "sync state from before per-session sidecars")
		if fileExists(bareJSON) {
			plan.add(bareJSON, "active-sidecar state from before per-session sidecars")
		}
	}

	diffs, _ := filepath.Glob(filepath.Join(dir, "sidecar.*.diff"))
	for _, diff := range diffs {
		counterpart := strings.TrimSuffix(diff, ".diff") + ".json"
		if !fileExists(counterpart) {
			plan.add(diff, "sync state with no matching active-sidecar record")
		}
	}
}

// scanLegacyFactory plans the removal of factory records and worktrees left
// in root's project data directory, where chunk kept them before runs moved
// to the repository data directory. A record is only removed once its run is
// pulled or discarded; an unfinished one is left alone, since LoadRecord
// falls back to it and a continuation may still need it.
func scanLegacyFactory(ctx context.Context, root, dir string, plan *Plan) {
	matches, _ := filepath.Glob(filepath.Join(dir, "factory", "*.json"))
	for _, path := range matches {
		if strings.HasSuffix(path, ".pool.json") {
			continue
		}
		rec, err := readRecord(path)
		if err != nil {
			continue
		}
		if !runDone(ctx, root, rec) {
			continue
		}
		plan.add(path, "factory run "+rec.RunID+" pulled or discarded")
		if pool := factory.PoolStatePath(dir, rec.RunID); fileExists(pool) {
			plan.add(pool, "factory run "+rec.RunID+"'s pool state")
		}
		if rec.Worktree != "" && dirExists(rec.Worktree) {
			plan.addWorktree(root, rec.Worktree, "factory run "+rec.RunID+"'s worktree")
		}
	}
}

func readRecord(path string) (factory.Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return factory.Record{}, err
	}
	var rec factory.Record
	return rec, json.Unmarshal(data, &rec)
}

// runDone reports whether a factory run's work has already been pulled into
// the repository's history or discarded: its branch is gone, or it has
// already been merged into the checkout's current commit. Neither is true for
// a run still in progress, whose branch holds work found nowhere else yet.
//
// It reports false, the safe default, whenever root cannot be read as a git
// repository at all: rev-parse failing to verify the branch then means
// nothing, since the same failure would come from root's .git having gone
// missing or git itself being unavailable, neither of which says the branch
// was deleted or merged.
func runDone(ctx context.Context, root string, rec factory.Record) bool {
	if rec.Branch == "" {
		return true
	}
	git := gitexec.Runner{Dir: root}
	if git.Run(ctx, "rev-parse", "--git-dir") != nil {
		return false
	}
	ref := "refs/heads/" + rec.Branch
	if err := git.Run(ctx, "rev-parse", "--verify", "--quiet", ref); err != nil {
		return true
	}
	return git.Run(ctx, "merge-base", "--is-ancestor", ref, "HEAD") == nil
}

// scanHome plans the removal of chunk's oldest leftovers, kept directly
// under the home directory before XDG data and state directories were used
// at all.
func scanHome(plan *Plan) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	factoryDir := filepath.Join(home, ".chunk", "factory")
	if dirExists(factoryDir) {
		plan.add(factoryDir, "factory records from before per-repository state")
	}

	daemonDir := filepath.Join(home, ".chunk", "daemon")
	if dirExists(daemonDir) && !daemonListening(filepath.Join(daemonDir, "daemon.sock")) {
		plan.add(daemonDir, "daemon state from before XDG state directories")
	}
	return nil
}

// daemonListening reports whether something is listening on the Unix socket
// at path, mirroring chunkd's own check for an old daemon still running there.
func daemonListening(path string) bool {
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func dirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}
