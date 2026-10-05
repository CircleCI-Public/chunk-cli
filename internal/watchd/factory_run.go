package watchd

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// DefaultAttempts is how many rounds a factory session checks at most when the
// request does not say.
const DefaultAttempts = 3

// factoryAuthError explains why factory sessions cannot run, or "" when they
// can.
func (d *daemon) factoryAuthError() string {
	if d.rcfg.Credential.Value == "" {
		if d.rcfg.AuthError != "" {
			return d.rcfg.AuthError
		}
		return "no Claude credential configured for the watch daemon — factory runs unavailable"
	}
	if d.rcfg.RunFactory == nil && d.client == nil {
		return "not authenticated to CircleCI — factory runs unavailable (run: chunk auth login)"
	}
	return ""
}

// startFactory validates a request, records a factory session and starts it in
// the background. It returns as soon as the session is accepted.
func (d *daemon) startFactory(req FactoryRequest) (Session, error) {
	if msg := d.factoryAuthError(); msg != "" {
		return Session{}, apiErr(http.StatusServiceUnavailable, "%s", msg)
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return Session{}, apiErr(http.StatusBadRequest, "prompt required")
	}
	ps := d.lookupProject(req.ProjectRoot)
	if ps == nil {
		return Session{}, apiErr(http.StatusNotFound, "the watch daemon is not tracking %q", req.ProjectRoot)
	}
	cfg, err := loadProjectConfig(ps.root)
	if err != nil {
		return Session{}, apiErr(http.StatusBadRequest, "%v", err)
	}
	dir, err := sessionPromptsDir(ps.root, req.ReviewsDir)
	if err != nil {
		return Session{}, apiErr(http.StatusBadRequest, "%v", err)
	}
	prompts, commands, err := factory.LoadChecks(dir, req.ReviewsDir == "", cfg, req.NoValidate)
	if err != nil {
		return Session{}, apiErr(http.StatusBadRequest, "%v", err)
	}
	orgID, image := req.OrgID, req.Image
	if orgID == "" || image == "" {
		cfgOrg, cfgImage, err := poolTarget(ps.root, cfg)
		if err != nil && orgID == "" {
			return Session{}, apiErr(http.StatusBadRequest, "%v", err)
		}
		if orgID == "" {
			orgID = cfgOrg
		}
		if image == "" {
			image = cfgImage
		}
	}
	// The daemon's working directory is not the caller's, so a relative path
	// would put the log somewhere the caller did not mean.
	if req.Log != "" && req.Log != factory.LogDefault && !filepath.IsAbs(req.Log) {
		return Session{}, apiErr(http.StatusBadRequest, "log must be an absolute path, got %q", req.Log)
	}
	attempts := req.Attempts
	if attempts <= 0 {
		attempts = DefaultAttempts
	}

	entry, ctx, busy := d.sessions.add(Session{
		Kind:        KindFactory,
		ProjectRoot: ps.root,
		Branch:      currentBranch(ps.root),
		HeadSHA:     headRef(ps.root),
		Factory:     &FactoryRun{Prompt: prompt, Attempts: attempts},
	})
	if busy != "" {
		return Session{}, apiErr(http.StatusConflict, "%s", busy)
	}
	opts := factory.RunOptions{
		Root:                    ps.root,
		Prompt:                  prompt,
		Attempts:                attempts,
		Reviewers:               factory.ReviewerCount(req.Reviewers, prompts),
		Prompts:                 prompts,
		Commands:                commands,
		Client:                  d.client,
		OrgID:                   orgID,
		Image:                   image,
		KeepSidecars:            req.KeepSidecars,
		Credential:              d.rcfg.Credential,
		BaseURL:                 d.rcfg.BaseURL,
		Model:                   req.Model,
		ImplementerInstructions: req.ImplementerInstructions,
		ImplementTimeout:        time.Duration(req.ImplementTimeoutSeconds) * time.Second,
		ReviewTimeout:           time.Duration(req.ReviewTimeoutSeconds) * time.Second,
		Log:                     req.Log,
		Verbose:                 req.Verbose,
	}
	go d.executeFactory(ctx, entry, opts)
	return entry.snapshot(), nil
}

// executeFactory runs a factory session and settles it. It always closes
// entry.done.
func (d *daemon) executeFactory(ctx context.Context, entry *sessionEntry, opts factory.RunOptions) {
	defer close(entry.done)
	defer entry.cancel()

	rec := &factoryRecorder{entry: entry, prompts: opts.Prompts, rounds: map[int]int{}}
	root := opts.Root
	opts.Status = func(level iostream.Level, msg string) {
		log.Printf("watchd: factory (%s): %s", root, msg)
		rec.progress(level, msg)
	}
	opts.Exec = d.execerFor(root, rec.attribute)
	opts.OnStart = rec.started
	opts.OnEvent = rec.event
	opts.OnReviewProgress = rec.reviewProgress
	opts.OnCheck = rec.checked

	run := d.rcfg.RunFactory
	if run == nil {
		run = factory.Run
	}
	rep, err := run(ctx, opts)
	rec.finish(rep, err)
	d.settleSession(entry, err)
}

// factoryResultNote says how a factory loop ended, for its stage.
func factoryResultNote(o factory.Outcome) string {
	switch o.Result {
	case factory.ResultPassed:
		return fmt.Sprintf("all checks passed after %d round(s)", o.Rounds)
	case factory.ResultNoChange:
		return "the implementer made no changes"
	case factory.ResultStuck:
		return fmt.Sprintf("the implementer stopped changing the code after round %d, with checks still failing", o.Rounds)
	case factory.ResultExhausted:
		return fmt.Sprintf("checks still failed after %d round(s)", o.Rounds)
	}
	return string(o.Result)
}

// factoryRecorder keeps a factory session's record as its run goes. Each
// factory round is a round of the record: its implementer turn, then its
// reviews and validation commands.
type factoryRecorder struct {
	entry   *sessionEntry
	prompts []review.Prompt

	// rounds maps a factory round to the record's round index, and cur is the
	// round in progress. Both are only touched from the loop's goroutine;
	// attribute reads cur through the entry's lock.
	rounds map[int]int
	cur    int
}

func (r *factoryRecorder) started(runID string, wt factory.Worktree) {
	e := r.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	f := e.s.Factory
	f.RunID, f.Worktree, f.Branch, f.Baseline, f.Head = runID, wt.Path, wt.Branch, wt.Baseline, wt.Head
	// The work is on the run's branch, not the one checked out.
	e.s.Branch = wt.Branch
}

// progress records something the run said. It is called from the pool's and
// the relay's goroutines as well as the loop's.
func (r *factoryRecorder) progress(level iostream.Level, msg string) {
	e := r.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	e.s.Factory.Progress.add(level, msg)
}

// round returns the record's index for a factory round, beginning it if it
// has not begun: a round that only checks again has no implementer turn.
func (r *factoryRecorder) round(n int) int {
	if ridx, ok := r.rounds[n]; ok {
		return ridx
	}
	ridx := r.entry.beginRound(r.prompts)
	r.rounds[n] = ridx
	r.entry.mu.Lock()
	r.cur = ridx
	r.entry.mu.Unlock()
	return ridx
}

func (r *factoryRecorder) event(ev factory.Event) {
	ridx := r.round(ev.Round)
	e := r.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	round := &e.s.Rounds[ridx]
	switch ev.Kind {
	case factory.EventImplementing:
		round.State = RoundImplementing
		round.Implement = &RoundImplement{State: FixRunning}
	case factory.EventImplemented:
		if impl := round.Implement; impl != nil {
			impl.State = FixApplied
			impl.DurationMS = ev.Turn.Duration.Milliseconds()
			impl.CostUSD = ev.Turn.CostUSD
			impl.Summary = ev.Turn.Summary
		}
	case factory.EventCollected:
		if impl := round.Implement; impl != nil {
			impl.Stat = ev.Change.Stat
			if ev.Change.Empty() {
				impl.State = FixEmpty
			}
		}
	case factory.EventChecking:
		round.State = RoundChecking
	case factory.EventChecked:
		r.recordReviewsLocked(ridx, ev.Checks)
		passed := 0
		for _, c := range ev.Checks {
			if c.Status == factory.StatusPassed {
				passed++
			}
		}
		now := time.Now()
		round.State, round.EndedAt = RoundDone, &now
		round.Note = fmt.Sprintf("%d of %d checks passed", passed, len(ev.Checks))
	}
}

// recordReviewsLocked stores the findings of a round's reviews.
func (r *factoryRecorder) recordReviewsLocked(ridx int, checks []factory.Check) {
	e := r.entry
	rd := &e.details[ridx]
	rd.Results = rd.Results[:0]
	var all []review.Finding
	for _, c := range checks {
		if c.Kind != factory.KindReview {
			continue
		}
		res := ReviewResult{Prompt: c.Name, SidecarID: c.SidecarID, Error: c.Error, DurationMS: c.Duration.Milliseconds(), Status: string(c.Status)}
		for i, f := range c.Findings {
			f.Prompt = c.Name
			f.ID = fmt.Sprintf("%s-%d", c.Name, i+1)
			res.Findings = append(res.Findings, f)
		}
		if i := e.reviewIndexLocked(ridx, c.Name); i >= 0 {
			e.s.Rounds[ridx].Reviews[i].Findings = len(res.Findings)
		}
		all = append(all, res.Findings...)
		rd.Results = append(rd.Results, res)
	}
	unique := review.DedupeFindings(all)
	e.s.Rounds[ridx].Findings = len(unique)
	e.s.Rounds[ridx].Worth = len(worthChanging(unique))
}

func (r *factoryRecorder) reviewProgress(ev review.ProgressEvent) {
	r.entry.mu.Lock()
	ridx := r.cur
	r.entry.mu.Unlock()
	r.entry.applyProgress(ridx, ev)
}

// attribute names a command for the output store when it is a review's, and
// ties it to that review. Anything else run on the sidecars is bookkeeping, or
// the implementer's turn, whose raw output does not read as a log.
func (r *factoryRecorder) attribute(sidecarID, commandID string) string {
	e := r.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.cur >= len(e.s.Rounds) {
		return ""
	}
	round := &e.s.Rounds[r.cur]
	for i := range round.Reviews {
		p := &round.Reviews[i]
		if p.SidecarID == sidecarID && p.State == PromptRunning {
			p.CommandID = commandID
			return fmt.Sprintf("round %d review: %s", round.Number, p.Name)
		}
	}
	return ""
}

// checked records a validation command as it finishes.
func (r *factoryRecorder) checked(c factory.Check) {
	e := r.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	if r.cur >= len(e.s.Rounds) {
		return
	}
	round := &e.s.Rounds[r.cur]
	round.Checks = append(round.Checks, RoundCheck{
		Name:       c.Name,
		Status:     string(c.Status),
		SidecarID:  c.SidecarID,
		DurationMS: c.Duration.Milliseconds(),
		Error:      c.Error,
		Output:     c.Output,
	})
}

// finish records what the run left behind. It runs before the session is
// settled, which closes whatever was still in flight.
func (r *factoryRecorder) finish(rep factory.Report, err error) {
	e := r.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	f := e.s.Factory
	f.Committed = rep.Committed
	f.KeptSidecars = rep.KeptSidecars
	f.Log = rep.Log
	if rep.Started {
		f.Result = string(rep.Outcome.Result)
		f.Rounds = rep.Outcome.Rounds
	}
	if err == nil {
		e.loopNote = factoryResultNote(rep.Outcome)
		e.loopFailed = rep.Outcome.Result != factory.ResultPassed
	}
	now := time.Now()
	for i := range e.s.Rounds {
		round := &e.s.Rounds[i]
		if impl := round.Implement; impl != nil && impl.State == FixRunning {
			impl.State = FixFailed
			if err != nil {
				impl.Error = friendlyReviewError(err)
			}
		}
		// A round the loop stopped in after the implementer's turn, finding
		// nothing new to check, ended there: its reviews never ran.
		if err == nil && round.State == RoundImplementing {
			round.State, round.EndedAt = RoundDone, &now
			round.Note = "not checked: " + e.loopNote
			round.Reviews = nil
		}
	}
}
