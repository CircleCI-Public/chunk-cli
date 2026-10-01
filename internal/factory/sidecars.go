package factory

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// reviewScope tells each reviewer what the change under review is. The prompt
// files are written for a developer's branch; in the loop the change is the
// implementer's uncommitted work on top of a baseline commit.
const reviewScope = `The change under review is the uncommitted work in this repository: run ` +
	"`git diff HEAD`" + ` to see it, including new files. Committed history is the baseline and is not under review.

`

// PoolName names a run's sidecar pool. Each run has a pool of its own, rather
// than one per project reused as `chunk review` does: every member is synced
// from the run's worktree and gets a baseline commit on top of it, which a
// later run's sync does not expect. Two runs in one project must not share
// sidecars either.
func PoolName(runID string) string {
	return "factory-" + runID
}

// PoolSize is how many sidecars a run needs: the implementer, which runs the
// validation commands too, and one per reviewer.
func PoolSize(reviewers int) int {
	return 1 + reviewers
}

// Members describes every sidecar in a pool other than the implementer, the
// ones its tree is relayed to. ids are the pool's, once it is synced.
func Members(impl *sidecar.PoolEntry, ids []string) []*sidecar.PoolEntry {
	var out []*sidecar.PoolEntry
	for _, id := range ids {
		if id != impl.ID {
			out = append(out, &sidecar.PoolEntry{ID: id, RepoPath: impl.RepoPath, Client: impl.Client})
		}
	}
	return out
}

// Sidecars runs the loop's steps on a pool of sidecars: the implementer writes
// code on the member it holds for the whole run, which also runs validation,
// and each review runs on another member the implementer's tree is relayed to.
type Sidecars struct {
	Exec        review.Execer
	Implementer *Implementer
	// Acquire and Release hand out reviewer sidecars: the pool's own, with the
	// implementer's member already checked out.
	Acquire   func(context.Context) (*sidecar.PoolEntry, error)
	Release   func(*sidecar.PoolEntry)
	Reviewers []*sidecar.PoolEntry
	Relay     *Relay
	Prompts   []review.Prompt
	Review    review.Options
	Commands  []config.Command
	// OnCheck is called as each validation command finishes. Reviews report
	// their progress through Review.ProgressFn.
	OnCheck func(Check)

	ws workspace
}

// Prepare commits the tree the pool synced to every member, the worktree's, as
// the baseline the implementer's work is measured against. Reviewers get one
// too: they are sent the implementer's files but never its .git, so `git diff
// HEAD` on a reviewer needs a commit of the same tree to diff against.
func (s *Sidecars) Prepare(ctx context.Context) error {
	s.ws = workspace{exec: s.Exec, entry: s.Implementer.Entry}
	if err := s.ws.commitBaseline(ctx); err != nil {
		return err
	}
	return s.onReviewers(func(e *sidecar.PoolEntry) error {
		rw := workspace{exec: s.Exec, entry: e}
		if err := rw.commitBaseline(ctx); err != nil {
			return fmt.Errorf("%s: %w", e.ID, err)
		}
		return nil
	})
}

// Implement runs one implementer turn.
func (s *Sidecars) Implement(ctx context.Context, prompt string) (Turn, error) {
	return s.Implementer.Run(ctx, prompt)
}

// Collect describes the implementer's work so far.
func (s *Sidecars) Collect(ctx context.Context) (Change, error) {
	return s.ws.collect(ctx)
}

// Check relays the implementer's tree to the reviewers, then runs the reviews
// there and validation on the implementer at the same time.
func (s *Sidecars) Check(ctx context.Context, _ int) ([]Check, error) {
	if err := s.Relay.Pull(ctx, s.Implementer.Entry.ID, s.Implementer.Entry.RepoPath); err != nil {
		return nil, err
	}
	if len(s.Prompts) > 0 {
		// A review that timed out stops being streamed but keeps running on
		// its sidecar, and would read the tree as it is replaced. Best-effort:
		// a sidecar that cannot be reached fails the push that follows anyway.
		// The bracket keeps the pattern from matching the shell running pkill.
		_ = s.onReviewers(s.script(ctx, "pkill -f '[c]laude -p' || true"))
		if err := s.Relay.Push(ctx, s.Reviewers); err != nil {
			return nil, err
		}
		// New files are marked intent-to-add so `git diff HEAD` shows them; the
		// reset first drops marks for files a later round deleted.
		if err := s.onReviewers(s.script(ctx, "git reset -q && git add -A -N")); err != nil {
			return nil, fmt.Errorf("prepare reviewers: %w", err)
		}
	}

	var (
		wg         sync.WaitGroup
		results    []review.Result
		reviewErr  error
		validation []Check
	)
	if len(s.Prompts) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results, reviewErr = review.RunPass(ctx, s.Acquire, s.Release, s.Exec, s.scopedPrompts(), s.Review)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		validation = runValidation(ctx, s.Exec, s.Implementer.Entry, s.Commands, s.OnCheck)
	}()
	wg.Wait()
	if reviewErr != nil {
		return nil, fmt.Errorf("reviews: %w", reviewErr)
	}

	checks := make([]Check, 0, len(results)+len(validation))
	for _, r := range results {
		checks = append(checks, FromReview(r))
	}
	return append(checks, validation...), nil
}

// Pull brings the implementer's work so far into the worktree. Check does it
// every round; this is for the way out, after a turn that failed or a round
// that stopped short of checking.
func (s *Sidecars) Pull(ctx context.Context) error {
	return s.Relay.Pull(ctx, s.Implementer.Entry.ID, s.Implementer.Entry.RepoPath)
}

// onReviewers runs fn on every reviewer at once and reports all that failed.
func (s *Sidecars) onReviewers(fn func(*sidecar.PoolEntry) error) error {
	errs := make([]error, len(s.Reviewers))
	var wg sync.WaitGroup
	for i, e := range s.Reviewers {
		wg.Add(1)
		go func(i int, e *sidecar.PoolEntry) {
			defer wg.Done()
			errs[i] = fn(e)
		}(i, e)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// script runs a command in a reviewer's workspace.
func (s *Sidecars) script(ctx context.Context, cmd string) func(*sidecar.PoolEntry) error {
	return func(e *sidecar.PoolEntry) error {
		if _, err := review.RunScript(ctx, s.Exec, e, "cd "+sidecar.ShellEscape(e.RepoPath)+" && "+cmd, maxScriptOutput); err != nil {
			return fmt.Errorf("%s: %w", e.ID, err)
		}
		return nil
	}
}

func (s *Sidecars) scopedPrompts() []review.Prompt {
	out := make([]review.Prompt, len(s.Prompts))
	for i, p := range s.Prompts {
		out[i] = review.Prompt{Name: p.Name, Body: reviewScope + p.Body}
	}
	return out
}
