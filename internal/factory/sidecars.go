package factory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
// than one per project reused as `chunk review` does: the baseline commit
// moves the implementer's HEAD, and the reviewers get the implementer's .git,
// so a later sync would start from a commit the developer never had. Two runs
// in one project must not share sidecars either.
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

// Prepare commits the developer's tree, which the pool synced to every member,
// as the baseline the implementer's work is measured against.
func (s *Sidecars) Prepare(ctx context.Context) error {
	s.ws = workspace{exec: s.Exec, entry: s.Implementer.Entry}
	return s.ws.commitBaseline(ctx)
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
		s.stopStrayReviews(ctx)
		if err := s.Relay.Push(ctx, s.Reviewers); err != nil {
			return nil, err
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

// WritePatch collects the implementer's work and writes it, relative to the
// baseline, to path as a patch the developer can git apply to the tree they
// started from. It collects first because it also runs after a turn that
// failed, whose new files are not yet marked intent-to-add. Nothing is written
// when the change is empty.
//
// The diff runs on the implementer's sidecar, not on a local copy: the
// implementer controls its .git/config and .gitattributes, whose filters and
// diff drivers git would otherwise run on the developer's machine.
func (s *Sidecars) WritePatch(ctx context.Context, path string) (Change, error) {
	change, err := s.ws.collect(ctx)
	if err != nil {
		return Change{}, err
	}
	if change.Empty() {
		return change, nil
	}
	patch, err := s.ws.run(ctx, "cd "+sidecar.ShellEscape(s.Implementer.Entry.RepoPath)+" && "+patchDiff)
	if err != nil {
		return Change{}, fmt.Errorf("diff implementer's work: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Change{}, fmt.Errorf("create patch directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(patch), 0o644); err != nil {
		return Change{}, fmt.Errorf("write patch: %w", err)
	}
	return change, nil
}

// patchDiff produces a patch git apply accepts whatever the sidecar's git
// config says, such as diff.noprefix or color.diff=always.
const patchDiff = "git diff HEAD --binary --no-ext-diff --no-textconv --no-color --no-relative --src-prefix=a/ --dst-prefix=b/"

// stopStrayReviews kills claude processes left on reviewers by an earlier
// round: a review that timed out stops being streamed but keeps running on its
// sidecar, and would read the tree as it is replaced. Best-effort: a sidecar
// that cannot be reached fails the push that follows anyway. The bracket keeps
// the pattern from matching the shell that runs pkill.
func (s *Sidecars) stopStrayReviews(ctx context.Context) {
	var wg sync.WaitGroup
	for _, e := range s.Reviewers {
		wg.Add(1)
		go func(e *sidecar.PoolEntry) {
			defer wg.Done()
			_, _ = review.RunScript(ctx, s.Exec, e, "pkill -f '[c]laude -p' || true", maxScriptOutput)
		}(e)
	}
	wg.Wait()
}

func (s *Sidecars) scopedPrompts() []review.Prompt {
	out := make([]review.Prompt, len(s.Prompts))
	for i, p := range s.Prompts {
		out[i] = review.Prompt{Name: p.Name, Body: reviewScope + p.Body}
	}
	return out
}
