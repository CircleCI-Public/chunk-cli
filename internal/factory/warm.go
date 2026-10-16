package factory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// WarmPool is the sidecars a run continuing a started run would use, made
// before that run starts. Starting sidecars takes the better part of a minute;
// made while the work in the started run is still being done, they are ready
// by the time it is handed to the factory.
//
// A continued run's sidecars start from the started run's baseline, not its
// work, which the run lays on the implementer's afterwards. The baseline is
// fixed when the run is started, so the sidecars can be made from it while the
// worktree is still changing: they are synced from a checkout of the baseline
// of their own.
type WarmPool struct {
	// RunID is the started run the sidecars are for.
	RunID string
	// Reviewers, OrgID and Image are what the sidecars were made with. A run
	// that wants other ones does not use them.
	Reviewers    int
	OrgID, Image string

	pool    *sidecar.Pool
	root    string
	dir     string
	claimed atomic.Bool
}

// warmRepoPath, when set, is where on a warm pool's sidecars the repository
// goes, instead of where its name puts it. Set in tests, whose sidecars are
// directories on this machine.
var warmRepoPath string

// NewWarmPool makes the sidecars a run continuing rec would use, for
// reviewers reviewers, from a checkout of rec's baseline beside its record.
func NewWarmPool(ctx context.Context, client *circleci.Client, root string, rec Record, reviewers int, orgID, image string, status iostream.StatusFunc) (*WarmPool, error) {
	dataDir, err := runsDir(root)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(dataDir, rec.RunID+"-baseline")
	git := gitexec.Runner{Dir: root}
	// A checkout left by an earlier attempt would make worktree add fail.
	_, _ = git.CombinedOutput(ctx, "worktree", "remove", "--force", dir)
	if out, err := git.CombinedOutput(ctx, "worktree", "add", "--detach", dir, rec.Baseline); err != nil {
		return nil, fmt.Errorf("check out the baseline of run %s: %w: %s", rec.RunID, err, strings.TrimSpace(string(out)))
	}
	w := &WarmPool{RunID: rec.RunID, Reviewers: reviewers, OrgID: orgID, Image: image, root: root, dir: dir}
	w.pool, err = sidecar.NewPool(ctx, client, sidecar.PoolOptions{
		Size:      PoolSize(reviewers),
		Name:      PoolName(rec.RunID),
		OrgID:     orgID,
		Image:     image,
		WorkDir:   dir,
		StatePath: PoolStatePath(dataDir, rec.RunID),
		RepoPath:  warmRepoPath,
	}, status)
	if err != nil {
		w.removeCheckout(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("make the sidecars ahead of run %s: %w", rec.RunID, err)
	}
	return w, nil
}

// Fits reports whether a run with these reviewers, organization and image can
// use the sidecars.
func (w *WarmPool) Fits(reviewers int, orgID, image string) bool {
	return w.Reviewers == reviewers && w.OrgID == orgID && w.Image == image
}

// claim hands the sidecars to a run working in workDir. From then on the
// pool is the run's, and any sidecar it replaces is synced from the run's
// worktree, as it would be for sidecars the run made itself.
func (w *WarmPool) claim(ctx context.Context, workDir string) *sidecar.Pool {
	w.claimed.Store(true)
	w.pool.SetWorkDir(workDir)
	w.removeCheckout(ctx)
	return w.pool
}

// Claimed reports whether a run took the sidecars, which are then the run's to
// delete.
func (w *WarmPool) Claimed() bool { return w.claimed.Load() }

// Discard deletes the sidecars of a warm pool no run used, and the checkout
// they were made from.
func (w *WarmPool) Discard(ctx context.Context) error {
	var err error
	if w.pool != nil {
		err = w.pool.Destroy(ctx)
	}
	if w.dir != "" {
		w.removeCheckout(ctx)
	}
	return err
}

func (w *WarmPool) removeCheckout(ctx context.Context) {
	git := gitexec.Runner{Dir: w.root}
	if _, err := git.CombinedOutput(ctx, "worktree", "remove", "--force", w.dir); err != nil {
		_ = os.RemoveAll(w.dir)
		_, _ = git.CombinedOutput(ctx, "worktree", "prune")
	}
}
