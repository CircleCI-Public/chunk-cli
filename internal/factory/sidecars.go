package factory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/review"
	"github.com/smeltdev/smelt/internal/sidecar"
)

// reviewHead tells each reviewer how the run's request and its reusable review
// prompt relate. The original request remains the specification in every round;
// later implementer prompts contain only feedback from the preceding checks.
const reviewHead = `Review the implementation against the original requested change below. Treat the requested change as the specification and the review instructions as the lens for evaluating it. Do not implement changes yourself.

## Requested change

%s

## Change under review

The change under review is the uncommitted work in this repository: run ` +
	"`git diff HEAD`" + ` to see it, including new files. Committed history is the baseline and is not under review.`

const reviewTail = "## Review instructions\n\n%s"

// PoolName names a run's sidecar pool. Each run has a pool of its own, rather
// than one per project reused as `smelt review` does: every member is synced
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
			out = append(out, &sidecar.PoolEntry{ID: id, RepoPath: impl.RepoPath, Backend: impl.Backend})
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
	// Request is the original change the implementer was asked to make. It is
	// included in every review, including rounds where the implementer is sent
	// only feedback from the preceding checks.
	Request  string
	Prompts  []review.Prompt
	Review   review.Options
	Commands []config.Command
	// GatesFirst runs Commands before the reviews, and skips the reviews of a
	// round whose commands failed.
	GatesFirst bool
	// Install, when set, installs the project's dependencies once on every
	// sidecar before the baseline commit. A prebuilt image holds only the
	// package manager's store, since the workspace is mounted over the repo.
	Install string
	// InstallTimeout bounds Install on each sidecar; zero means
	// config.DefaultInstallTimeout.
	InstallTimeout time.Duration
	// OnInstallOutput receives each sidecar's install output as it arrives.
	OnInstallOutput func(sidecarID string, line string)
	// OnCheck is called as each validation command finishes. Reviews report
	// their progress through Review.ProgressFn.
	OnCheck func(Check)
	// Status, when set, receives notices about a round, such as a summary whose
	// gates were not run.
	Status iostream.StatusFunc
	// OnReviewerTree, when set, is called each round for every reviewer, in
	// Reviewers order, with whether the change it is about to review is the
	// implementer's. It is a canary for the relay; a mismatch does not stop the
	// round.
	OnReviewerTree func(ReviewerTree)

	ws workspace
	// history is what each review found in earlier rounds, by prompt name.
	history map[string][]priorRound
	// fingerprint is the implementer's change as last collected, which is
	// what each round's reviews are of.
	fingerprint string
}

// Prepare commits the tree the pool synced to every member, the worktree's, as
// the baseline the implementer's work is measured against. Reviewers get one
// too: they are sent the implementer's files but never its .git, so `git diff
// HEAD` on a reviewer needs a commit of the same tree to diff against.
func (s *Sidecars) Prepare(ctx context.Context) error {
	if err := s.installDependencies(ctx); err != nil {
		return err
	}
	if err := s.prepareHarness(ctx); err != nil {
		return err
	}
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
	turn, err := s.Implementer.Run(ctx, prompt)
	if err != nil {
		return turn, err
	}
	if turn, err = s.prove(ctx, turn); err != nil {
		return turn, err
	}
	s.answer(turn.Summary)
	return turn, nil
}

// prove checks a summary that claims passing gates against what the turn ran.
// One that claims them without running them after its last edit is asked once
// to run them, in a turn that belongs to the same round; a second miss is
// reported on the turn, and the round's own gates tell the truth.
func (s *Sidecars) prove(ctx context.Context, turn Turn) (Turn, error) {
	gates := gateCommands(s.Commands)
	missing := unproven(turn.Activity, gates)
	if len(missing) == 0 || !claimsGatesPass(turn.Summary) {
		return turn, nil
	}
	s.notify(iostream.LevelWarn, "the summary says the gates pass but "+strings.Join(missing, ", ")+" were not run after the last edit; asking the implementer to run them")
	next, err := s.Implementer.Run(ctx, nudgePrompt(missing))
	if err != nil {
		return mergeTurns(turn, next), err
	}
	turn = mergeTurns(turn, next)
	turn.Nudged = true
	if missing = unproven(turn.Activity, gates); len(missing) > 0 && claimsGatesPass(turn.Summary) {
		turn.Unproven = missing
		s.notify(iostream.LevelWarn, "the implementer still claims passing gates it did not run after its last edit: "+strings.Join(missing, ", "))
	}
	return turn, nil
}

// notify reports a notice, if anyone listens.
func (s *Sidecars) notify(level iostream.Level, msg string) {
	if s.Status != nil {
		s.Status(level, msg)
	}
}

// answer records the implementer's summary as the reply to every review's latest
// findings, since one turn answers all the feedback of the round just checked.
func (s *Sidecars) answer(reply string) {
	for name, rounds := range s.history {
		if n := len(rounds); n > 0 && rounds[n-1].Reply == "" {
			s.history[name][n-1].Reply = strings.TrimSpace(reply)
		}
	}
}

// Collect describes the implementer's work so far.
func (s *Sidecars) Collect(ctx context.Context) (Change, error) {
	c, err := s.ws.collect(ctx)
	if err == nil {
		s.fingerprint = c.Fingerprint
	}
	return c, err
}

// Check brings the implementer's work into the worktree, then validates it and
// has it reviewed. With GatesFirst the validation commands run first and the
// reviews only when none failed; otherwise both run at the same time.
func (s *Sidecars) Check(ctx context.Context, round int) ([]Check, error) {
	if err := s.Relay.Pull(ctx, s.Implementer.Entry.ID, s.Implementer.Entry.RepoPath); err != nil {
		return nil, err
	}
	if s.GatesFirst && len(s.Prompts) > 0 {
		return s.checkGatesFirst(ctx, round)
	}
	return s.checkTogether(ctx, round)
}

// checkGatesFirst runs the validation commands, and the reviews if they all
// passed. A round whose gates failed is going back to the implementer anyway,
// so its reviews are reported as skipped rather than spent on code that is
// about to change.
func (s *Sidecars) checkGatesFirst(ctx context.Context, round int) ([]Check, error) {
	gates := runValidation(ctx, s.Exec, s.Implementer.Entry, s.Commands, s.OnCheck)
	if gatesFailed(gates) {
		skipped := make([]Check, len(s.Prompts))
		for i, p := range s.Prompts {
			skipped[i] = skippedReview(p.Name)
		}
		return slices.Concat(skipped, gates), nil
	}
	if err := s.prepareReviewers(ctx, round); err != nil {
		return nil, err
	}
	reviews, err := s.runReviews(ctx, round)
	if err != nil {
		return nil, err
	}
	return slices.Concat(reviews, gates), nil
}

// checkTogether runs the reviews on the reviewers and validation on the
// implementer at the same time.
func (s *Sidecars) checkTogether(ctx context.Context, round int) ([]Check, error) {
	if len(s.Prompts) > 0 {
		if err := s.prepareReviewers(ctx, round); err != nil {
			return nil, err
		}
	}
	var (
		wg         sync.WaitGroup
		reviews    []Check
		reviewErr  error
		validation []Check
	)
	if len(s.Prompts) > 0 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reviews, reviewErr = s.runReviews(ctx, round)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		validation = runValidation(ctx, s.Exec, s.Implementer.Entry, s.Commands, s.OnCheck)
	}()
	wg.Wait()
	if reviewErr != nil {
		return nil, reviewErr
	}
	return slices.Concat(reviews, validation), nil
}

// prepareReviewers relays the implementer's tree, as pulled into the
// worktree, to the reviewers.
func (s *Sidecars) prepareReviewers(ctx context.Context, round int) error {
	// A review that timed out stops being streamed but keeps running on
	// its sidecar, and would read the tree as it is replaced. Best-effort:
	// a sidecar that cannot be reached fails the push that follows anyway.
	// The bracket keeps the pattern from matching the shell running pkill.
	_ = s.onReviewers(s.script(ctx, "pkill -f '[c]laude -p' || true"))
	if err := s.Relay.Push(ctx, s.Reviewers); err != nil {
		return err
	}
	// New files are marked intent-to-add so `git diff HEAD` shows them; the
	// reset first drops marks for files a later round deleted.
	if err := s.onReviewers(s.script(ctx, "git reset -q && git add -A -N")); err != nil {
		return fmt.Errorf("prepare reviewers: %w", err)
	}
	if s.OnReviewerTree != nil {
		s.reviewerTrees(ctx, round)
	}
	return nil
}

// runReviews runs every review prompt on the reviewers and remembers what each
// found for the next round.
func (s *Sidecars) runReviews(ctx context.Context, round int) ([]Check, error) {
	results, err := review.RunPass(ctx, s.Acquire, s.Release, s.Exec, scopePrompts(s.Request, s.Prompts, s.history), s.Review)
	if err != nil {
		return nil, fmt.Errorf("reviews: %w", err)
	}
	checks := make([]Check, 0, len(results))
	for _, r := range results {
		c := FromReview(r)
		checks = append(checks, c)
		s.remember(round, c)
	}
	return checks, nil
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
		if _, err := review.RunScript(ctx, s.Exec, e, inRepo(e, cmd), maxScriptOutput); err != nil {
			return fmt.Errorf("%s: %w", e.ID, err)
		}
		return nil
	}
}

// inRepo is the script that runs cmd in e's workspace.
func inRepo(e *sidecar.PoolEntry, cmd string) string {
	return "cd " + sidecar.ShellEscape(e.RepoPath) + " && " + cmd
}

// ReviewerTree compares the change one reviewer is about to review with the
// implementer's, by fingerprint.
type ReviewerTree struct {
	Round     int
	SidecarID string
	// Fingerprint is the reviewer's change; Want is the implementer's.
	Fingerprint string
	Want        string
	// Err is why the reviewer's fingerprint could not be read.
	Err error
}

// Matches reports whether the reviewer has the implementer's change.
func (t ReviewerTree) Matches() bool {
	return t.Err == nil && t.Fingerprint != "" && t.Fingerprint == t.Want
}

// reviewerTrees fingerprints every reviewer's change at once and reports them
// in order.
func (s *Sidecars) reviewerTrees(ctx context.Context, round int) {
	trees := make([]ReviewerTree, len(s.Reviewers))
	var wg sync.WaitGroup
	for i, e := range s.Reviewers {
		wg.Add(1)
		go func(i int, e *sidecar.PoolEntry) {
			defer wg.Done()
			out, err := review.RunScript(ctx, s.Exec, e, inRepo(e, fingerprintCmd), maxScriptOutput)
			trees[i] = ReviewerTree{Round: round, SidecarID: e.ID, Fingerprint: strings.TrimSpace(out), Want: s.fingerprint, Err: err}
		}(i, e)
	}
	wg.Wait()
	for _, t := range trees {
		s.OnReviewerTree(t)
	}
}

// remember keeps what a review found this round for the reviews of later ones.
// A review that could not run, or was not started, has nothing to remember.
func (s *Sidecars) remember(round int, c Check) {
	if c.Kind != KindReview || c.Status == StatusErrored || c.Status == StatusSkipped {
		return
	}
	if s.history == nil {
		s.history = map[string][]priorRound{}
	}
	var worth []review.Finding
	for _, f := range c.Findings {
		if f.WorthChanging() {
			worth = append(worth, f)
		}
	}
	s.history[c.Name] = append(s.history[c.Name], priorRound{Round: round, Findings: worth})
}

// scopePrompts is prompts as each reviewer is sent them, with the stable
// request being checked, the location of the change under review, the severity
// rubric, and what that reviewer reported in earlier rounds.
func scopePrompts(request string, prompts []review.Prompt, history map[string][]priorRound) []review.Prompt {
	out := make([]review.Prompt, len(prompts))
	for i, p := range prompts {
		var b strings.Builder
		fmt.Fprintf(&b, reviewHead, request)
		b.WriteString("\n\n")
		b.WriteString(severityScope + "\n\n")
		if h := historyScope(history[p.Name]); h != "" {
			b.WriteString(h + "\n")
		}
		fmt.Fprintf(&b, reviewTail, p.Body)
		out[i] = review.Prompt{Name: p.Name, Body: b.String()}
	}
	return out
}

// installDependencies runs Install in every sidecar's workspace at once. What
// it creates is gitignored, so later syncs leave it alone.
func (s *Sidecars) installDependencies(ctx context.Context) error {
	if s.Install == "" {
		return nil
	}
	entries := append([]*sidecar.PoolEntry{s.Implementer.Entry}, s.Reviewers...)
	errs := make([]error, len(entries))
	var wg sync.WaitGroup
	for i, e := range entries {
		wg.Add(1)
		go func(i int, e *sidecar.PoolEntry) {
			defer wg.Done()
			errs[i] = s.installOn(ctx, e)
		}(i, e)
	}
	wg.Wait()
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("install dependencies: %w", err)
	}
	return nil
}

func (s *Sidecars) installOn(ctx context.Context, e *sidecar.PoolEntry) error {
	timeout := s.InstallTimeout
	if timeout <= 0 {
		timeout = config.DefaultInstallTimeout * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var tail strings.Builder
	code, err := s.Exec(ctx, e, inRepo(e, s.Install), nil, func(_ string, data []byte) {
		if s.OnInstallOutput != nil {
			for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
				s.OnInstallOutput(e.ID, line)
			}
		}
		tail.Write(data)
		if tail.Len() > 8192 {
			rest := tail.String()[tail.Len()-4096:]
			tail.Reset()
			tail.WriteString(rest)
		}
	}, nil)
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("%s: %q timed out after %s: %s", e.ID, s.Install, timeout, review.Tail(strings.TrimSpace(tail.String()), 2000))
	case err != nil:
		return fmt.Errorf("%s: %w", e.ID, err)
	case code != 0:
		return fmt.Errorf("%s: %q exited %d: %s", e.ID, s.Install, code, review.Tail(strings.TrimSpace(tail.String()), 2000))
	}
	return nil
}

// prepareHarness runs the agent's own setup once on every sidecar, after the
// dependencies are installed. Its secrets are in the setup's environment only,
// so later turns never get them.
func (s *Sidecars) prepareHarness(ctx context.Context) error {
	script, env := s.Implementer.agent().Prepare(s.Implementer.Credential)
	if script == "" {
		return nil
	}
	prepare := func(e *sidecar.PoolEntry) error {
		if _, err := review.RunScriptEnv(ctx, s.Exec, e, script, env, 64<<10); err != nil {
			return fmt.Errorf("%s: set up %s: %w", e.ID, s.Implementer.agent().Name(), err)
		}
		return nil
	}
	if err := prepare(s.Implementer.Entry); err != nil {
		return err
	}
	return s.onReviewers(prepare)
}
