package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// reviewScope tells each reviewer what the change under review is. The prompt
// files are written for a developer's branch; in the loop the change is the
// implementer's uncommitted work on top of a baseline commit.
const reviewScope = `The change under review is the uncommitted work in this repository: run ` +
	"`git diff HEAD`" + ` to see it, including new files. Committed history is the baseline and is not under review.

`

// Provision creates the implementer's sidecar and one per reviewer, in
// parallel, and waits until each accepts connections. On error, any sidecar it
// created is deleted.
func Provision(ctx context.Context, client *circleci.Client, orgID, image, repoPath string, reviewers int, status iostream.StatusFunc) (impl *sidecar.PoolEntry, revs []*sidecar.PoolEntry, err error) {
	names := make([]string, 0, 1+reviewers)
	names = append(names, "factory-implementer")
	for i := range reviewers {
		names = append(names, fmt.Sprintf("factory-reviewer-%d", i+1))
	}
	entries := make([]*sidecar.PoolEntry, len(names))
	errs := make([]error, len(names))
	var wg sync.WaitGroup
	for i, name := range names {
		wg.Add(1)
		go func(i int, name string) {
			defer wg.Done()
			sc, err := sidecar.Create(ctx, client, orgID, name, image)
			if err != nil {
				errs[i] = fmt.Errorf("create %s: %w", name, err)
				return
			}
			entries[i] = &sidecar.PoolEntry{ID: sc.ID, RepoPath: repoPath, Client: client}
			status(iostream.LevelInfo, fmt.Sprintf("created %s (%s)", name, sc.ID))
			// A new sidecar can briefly 404 before it accepts a key.
			if _, err := sidecar.OpenSession(ctx, client, sc.ID, true); err != nil {
				errs[i] = fmt.Errorf("connect to %s: %w", name, err)
			}
		}(i, name)
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		if tdErr := Teardown(client, entries); tdErr != nil {
			status(iostream.LevelWarn, fmt.Sprintf("could not delete sidecars: %v", tdErr))
		}
		return nil, nil, err
	}
	return entries[0], entries[1:], nil
}

// teardownTimeout bounds Teardown: it runs on the way out, when a stuck delete
// should not keep the command from exiting.
const teardownTimeout = time.Minute

// Teardown deletes sidecars, ignoring nil entries and ones already gone. It
// returns the deletes that failed, so the caller can name the sidecars left
// running.
func Teardown(client *circleci.Client, entries []*sidecar.PoolEntry) error {
	ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
	defer cancel()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, e := range entries {
		if e == nil {
			continue
		}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if err := client.DeleteSidecar(ctx, id); err != nil && !circleci.SidecarGone(err) {
				mu.Lock()
				errs = append(errs, fmt.Errorf("delete sidecar %s: %w", id, err))
				mu.Unlock()
			}
		}(e.ID)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Sidecars runs the loop's steps on real sidecars: the implementer writes code
// on its own sidecar, which also runs validation, and each review runs on a
// reviewer sidecar the implementer's tree is relayed to.
type Sidecars struct {
	Client      *circleci.Client
	Exec        review.Execer
	Implementer *Implementer
	Reviewers   []*sidecar.PoolEntry
	Relay       *Relay
	Prompts     []review.Prompt
	Review      review.Options
	Commands    []config.Command
	Status      iostream.StatusFunc
	// OnCheck is called as each validation command finishes. Reviews report
	// their progress through Review.ProgressFn.
	OnCheck func(Check)

	ws workspace
}

// Prepare syncs the developer's tree at workDir to the implementer and commits
// it as the baseline the implementer's work is measured against.
func (s *Sidecars) Prepare(ctx context.Context, workDir string) error {
	impl := s.Implementer.Entry
	if err := sidecar.RsyncSyncEphemeral(ctx, s.Client, impl.ID, impl.RepoPath, workDir, s.status()); err != nil {
		return fmt.Errorf("sync to implementer: %w", err)
	}
	s.ws = workspace{exec: s.Exec, entry: impl}
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
			acquire, release := s.reviewerPool()
			results, reviewErr = review.RunPass(ctx, acquire, release, s.Exec, s.scopedPrompts(), s.Review)
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
			_, _ = runScript(ctx, s.Exec, e, "pkill -f '[c]laude -p' || true")
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

// reviewerPool hands out reviewer sidecars to a review pass, blocking while
// all are in use.
func (s *Sidecars) reviewerPool() (acquire func(context.Context) (*sidecar.PoolEntry, error), release func(*sidecar.PoolEntry)) {
	free := make(chan *sidecar.PoolEntry, len(s.Reviewers))
	for _, e := range s.Reviewers {
		free <- e
	}
	acquire = func(ctx context.Context) (*sidecar.PoolEntry, error) {
		select {
		case e := <-free:
			return e, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return acquire, func(e *sidecar.PoolEntry) { free <- e }
}

func (s *Sidecars) status() iostream.StatusFunc {
	if s.Status == nil {
		return func(iostream.Level, string) {}
	}
	return s.Status
}
