package watchd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
)

const (
	// ConflictInterval is how often the daemon re-evaluates whether each
	// project's branch still merges cleanly. Far slower than PollInterval
	// because the answer only changes when a commit lands on either side, and
	// the check costs a merge in the object database rather than a stat.
	ConflictInterval = 60 * time.Second

	// firstConflictDelay is how long the daemon waits before its first check.
	//
	// Not zero, because every project needs a fetch on that first pass and the
	// instant the daemon starts is when the developer is most likely mid-sync.
	// Not a full ConflictInterval either: a fresh `chunk watch` followed
	// straight away by a commit would otherwise get no answer for a minute,
	// which reads as "no conflicts" to anyone not counting.
	firstConflictDelay = 5 * time.Second

	// FetchInterval is how often the merge target's remote-tracking ref is
	// refreshed. The check is worthless against a ref nobody has updated in a
	// week, and expensive if refreshed every time: this is the compromise.
	FetchInterval = 3 * time.Minute

	// fetchTimeout bounds one background fetch. Generous, because a cold
	// connection to a large remote is legitimately slow, but bounded, because
	// this runs unattended and a hung fetch would stall every other project's
	// check behind it.
	fetchTimeout = 30 * time.Second

	// mergeTimeout bounds one merge preview. A merge resolved entirely in the
	// object database is fast even on a large tree; anything near this is a
	// repository problem, not a slow merge.
	mergeTimeout = 20 * time.Second

	// gitQueryTimeout bounds the small rev-parse style queries around the check.
	gitQueryTimeout = 10 * time.Second

	// MaxConflictPaths caps the paths reported for one branch. A merge that
	// conflicts in hundreds of files is one fact — "this branch has diverged
	// badly" — and listing all of them buries it.
	MaxConflictPaths = 20
)

// checkConflictsLoop re-evaluates every known project on its own schedule.
//
// It is deliberately a separate goroutine from pollLoop rather than work folded
// into poll: a fetch reaches the network and a merge preview reads the object
// database, and neither belongs on the path that keeps the dashboard's sidecar
// rows current. A slow remote must cost the conflict notice its freshness, not
// the whole daemon its responsiveness.
func (d *daemon) checkConflictsLoop(ctx context.Context) {
	// The first pass is delayed rather than skipped or run immediately; see
	// firstConflictDelay.
	select {
	case <-time.After(firstConflictDelay):
		d.checkConflicts(ctx)
	case <-ctx.Done():
		return
	}

	ticker := time.NewTicker(ConflictInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			d.checkConflicts(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// checkConflicts refreshes the conflict state of every registered project.
//
// Projects are walked in sequence rather than concurrently. The work is mostly
// network waiting, so concurrency would help wall-clock time, but a developer
// with a dozen registered projects would be issuing a dozen simultaneous
// fetches against the same remote every few minutes — a background feature has
// no business doing that.
func (d *daemon) checkConflicts(ctx context.Context) {
	d.mu.RLock()
	work := make([]*projectState, 0, len(d.projects))
	for _, ps := range d.projects {
		work = append(work, ps)
	}
	d.mu.RUnlock()

	for _, ps := range work {
		if ctx.Err() != nil {
			return
		}
		d.checkProjectConflict(ctx, ps)
	}
}

// checkProjectConflict evaluates one project and stores the result.
//
// Every git call happens outside the lock; only the final assignment takes it.
// This matters more here than on the poll path: a fetch can sit on the network
// for the whole of fetchTimeout, and holding the daemon's lock across that
// would block every snapshot read — including the one a hook is waiting on.
func (d *daemon) checkProjectConflict(ctx context.Context, ps *projectState) {
	d.mu.RLock()
	prior := ps.conflict
	lastFetch := ps.lastFetch
	d.mu.RUnlock()

	state, fetchedAt := evaluateConflict(ctx, ps.root, prior, lastFetch)

	d.mu.Lock()
	ps.conflict = state
	ps.lastFetch = fetchedAt
	d.mu.Unlock()
}

// evaluateConflict produces the conflict state for one repository.
//
// Split out from checkProjectConflict so the decision logic is testable without
// a daemon: it takes the prior state and last fetch time explicitly rather than
// reading them off projectState, and returns the new state alongside the fetch
// time to remember — lastFetch carried forward when it did not fetch.
func evaluateConflict(ctx context.Context, root string, prior *ConflictState, lastFetch time.Time) (*ConflictState, time.Time) {
	branch := currentBranch(root)
	if branch == "" {
		return &ConflictState{Unavailable: "HEAD is detached, so there is no branch to compare"}, lastFetch
	}

	remote, targetBranch, err := gitutil.DefaultRemoteBranchIn(root)
	if err != nil {
		// A repo with no remote HEAD recorded has no merge target to speak of.
		// Common in a fresh `git init`, and not a malfunction.
		return &ConflictState{
			Branch:      branch,
			Unavailable: "no default branch is recorded for this repository",
		}, lastFetch
	}
	target := remote + "/" + targetBranch

	state := &ConflictState{Branch: branch, Target: target, CheckedAt: time.Now()}

	if branch == targetBranch {
		// Nothing to advise: the branch is the thing everything else merges
		// into. Reported rather than silently skipped so a manual run can say
		// why it found nothing.
		state.Unavailable = "this branch is the merge target"
		return state, lastFetch
	}

	// Carried forward so a tick that does not fetch keeps reporting when the
	// ref was last refreshed, rather than reading as never fetched.
	fetchedAt := lastFetch
	if time.Since(lastFetch) >= FetchInterval {
		fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
		err := gitutil.FetchRemoteBranch(fetchCtx, root, remote, targetBranch)
		cancel()
		// Advanced even when the fetch failed. Treating only a success as an
		// attempt would put a repo with no network back on the wire every tick.
		fetchedAt = time.Now()
		if err != nil {
			// Not fatal: a tracking ref from an hour ago still supports a
			// useful answer. TargetStale is what stops it being presented as
			// though it were current.
			state.TargetStale = true
			log.Printf("watchd: fetch %s for %s: %v", target, root, err)
		}
	}
	state.TargetFetchedAt = fetchedAt

	queryCtx, cancel := context.WithTimeout(ctx, gitQueryTimeout)
	targetSHA, targetErr := gitutil.RevParseCtx(queryCtx, root, target)
	headSHA, headErr := gitutil.HeadRefCtx(queryCtx, root)
	cancel()

	if targetErr != nil {
		state.Unavailable = fmt.Sprintf("%s has never been fetched", target)
		return state, fetchedAt
	}
	if headErr != nil {
		state.Unavailable = "this branch has no commits yet"
		return state, fetchedAt
	}
	state.HeadSHA = headSHA
	state.TargetSHA = targetSHA

	// Neither side has moved since the last answer, so the merge would resolve
	// identically. Reusing it is what keeps an idle daemon from re-merging the
	// same two commits every minute for as long as it runs.
	if prior != nil && prior.HeadSHA == headSHA && prior.TargetSHA == targetSHA && prior.Unavailable == "" {
		reused := *prior
		reused.CheckedAt = state.CheckedAt
		reused.TargetFetchedAt = state.TargetFetchedAt
		reused.TargetStale = state.TargetStale
		return &reused, fetchedAt
	}

	mergeCtx, cancel := context.WithTimeout(ctx, mergeTimeout)
	preview, err := gitutil.PreviewMerge(mergeCtx, root, headSHA, targetSHA)
	cancel()
	if err != nil {
		if errors.Is(err, gitutil.ErrMergeTreeUnsupported) {
			state.Unavailable = "git 2.38 or newer is required to preview a merge"
		} else {
			state.Unavailable = "the merge could not be previewed"
			log.Printf("watchd: preview merge %s..%s in %s: %v", branch, target, root, err)
		}
		return state, fetchedAt
	}

	state.Conflicted = !preview.Clean
	state.Paths = preview.Paths
	// TotalPaths is set whether or not the list was cut, so a reader never has
	// to infer the real count from whether it happens to equal the cap.
	state.TotalPaths = len(preview.Paths)
	if len(state.Paths) > MaxConflictPaths {
		state.Paths = state.Paths[:MaxConflictPaths]
	}
	return state, fetchedAt
}
