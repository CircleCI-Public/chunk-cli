package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar/docker"
)

// DefaultAttempts is how many rounds a factory session checks at most when the
// request does not say.
const DefaultAttempts = 3

// factoryAuthError explains why a factory session on the given backend cannot
// run, or "" when it can. Every backend needs the Claude credential; only the
// CircleCI backend needs a CircleCI client.
func (d *daemon) factoryAuthError(backend string) string {
	if d.rcfg.Credential.Value == "" {
		if d.rcfg.AuthError != "" {
			return d.rcfg.AuthError
		}
		return "no Claude credential configured for the chunk daemon — factory runs unavailable"
	}
	if backend == "docker" {
		return ""
	}
	if d.rcfg.RunFactory == nil && d.client == nil {
		return "not authenticated to CircleCI — factory runs unavailable (run: chunk auth login)"
	}
	return ""
}

// startFactory validates a request, records a factory session and starts it in
// the background. It returns as soon as the session is accepted.
func (d *daemon) startFactory(req chunkd.FactoryRequest) (chunkd.Session, error) {
	if req.Backend != "" && req.Backend != "circleci" && req.Backend != "docker" {
		return chunkd.Session{}, apiErr(http.StatusBadRequest, "unknown backend %q (want circleci or docker)", req.Backend)
	}
	if msg := d.factoryAuthError(req.Backend); msg != "" {
		return chunkd.Session{}, apiErr(http.StatusServiceUnavailable, "%s", msg)
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" && req.Continue == "" {
		return chunkd.Session{}, apiErr(http.StatusBadRequest, "prompt required")
	}
	ps := d.lookupProject(req.ProjectRoot)
	if ps == nil {
		ps = d.adoptProject(req.ProjectRoot)
	}
	if ps == nil {
		return chunkd.Session{}, apiErr(http.StatusNotFound, "the chunk daemon is not tracking %q, and it is not the top of a git repository", req.ProjectRoot)
	}
	var cont *factory.Continuation
	record := &chunkd.FactoryRun{Prompt: prompt}
	if req.Continue != "" {
		from, err := factory.LoadRecord(ps.root, factory.ParseRunID(req.Continue))
		if errors.Is(err, factory.ErrNoRecord) {
			return chunkd.Session{}, apiErr(http.StatusNotFound, "%v", err)
		}
		if err != nil {
			return chunkd.Session{}, apiErr(http.StatusBadRequest, "%v", err)
		}
		cont = &factory.Continuation{From: from, Guidance: prompt}
		record = &chunkd.FactoryRun{Prompt: from.Prompt, Guidance: prompt, ContinuesRunID: from.RunID}
	}
	cfg, err := loadProjectConfig(ps.root)
	if err != nil {
		return chunkd.Session{}, apiErr(http.StatusBadRequest, "%v", err)
	}
	dir, err := sessionPromptsDir(ps.root, req.ReviewsDir)
	if err != nil {
		return chunkd.Session{}, apiErr(http.StatusBadRequest, "%v", err)
	}
	prompts, commands, err := factory.LoadChecks(dir, req.ReviewsDir == "", cfg, req.NoValidate)
	if err != nil {
		return chunkd.Session{}, apiErr(http.StatusBadRequest, "%v", err)
	}
	orgID, image := req.OrgID, req.Image
	// The Docker backend needs no CircleCI org; take only the image from config
	// (empty is fine — the Docker backend has its own default image).
	if req.Backend == "docker" {
		if image == "" && cfg.Validation != nil {
			image = cfg.Validation.SidecarImage
		}
	} else if orgID == "" || image == "" {
		cfgOrg, cfgImage, err := poolTarget(ps.root, cfg)
		if err != nil && orgID == "" {
			return chunkd.Session{}, apiErr(http.StatusBadRequest, "%v", err)
		}
		if orgID == "" {
			orgID = cfgOrg
		}
		if image == "" {
			image = cfgImage
		}
	}

	// Build the Docker backend up front so a daemon that cannot reach the
	// Docker socket fails the request rather than the background run.
	var backend sidecar.Backend
	if req.Backend == "docker" {
		db, err := docker.New()
		if err != nil {
			return chunkd.Session{}, apiErr(http.StatusServiceUnavailable, "docker backend unavailable: %v", err)
		}
		backend = db
	}
	// The daemon's working directory is not the caller's, so a relative path
	// would put the log somewhere the caller did not mean.
	if req.Log != "" && req.Log != factory.LogDefault && !filepath.IsAbs(req.Log) {
		return chunkd.Session{}, apiErr(http.StatusBadRequest, "log must be an absolute path, got %q", req.Log)
	}
	attempts := req.Attempts
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	record.Attempts = attempts
	if cont != nil {
		record.Attempts = cont.Rounds(attempts)
	}

	entry, ctx, busy := d.sessions.add(chunkd.Session{
		ProjectRoot: ps.root,
		Branch:      currentBranch(ps.root),
		HeadSHA:     headRef(ps.root),
		Factory:     record,
	})
	if busy != "" {
		return chunkd.Session{}, apiErr(http.StatusConflict, "%s", busy)
	}
	opts := factory.RunOptions{
		Root:                    ps.root,
		Prompt:                  prompt,
		Continue:                cont,
		Attempts:                attempts,
		Reviewers:               factory.ReviewerCount(req.Reviewers, prompts),
		Prompts:                 prompts,
		Commands:                commands,
		Client:                  d.client,
		OrgID:                   orgID,
		Image:                   image,
		Backend:                 backend,
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
		log.Printf("chunkd: factory (%s): %s", root, msg)
		rec.progress(level, msg)
	}
	// execerFor wires each command into the dashboard's live output pane via the
	// CircleCI client. The Docker backend runs commands itself, so it uses the
	// backend's own Exec (review.ClientExec); its live output pane lands in a
	// later phase.
	if opts.Backend == nil {
		opts.Exec = d.execerFor(root, rec.attribute)
	} else {
		opts.Exec = review.ClientExec
	}
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
	addFeedLine(&e.s.Factory.Progress, level, msg)
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
		round.State = chunkd.RoundImplementing
		round.Implement = &chunkd.RoundImplement{State: chunkd.ImplementRunning}
	case factory.EventImplemented:
		if impl := round.Implement; impl != nil {
			impl.State = chunkd.ImplementApplied
			impl.DurationMS = ev.Turn.Duration.Milliseconds()
			impl.CostUSD = ev.Turn.CostUSD
			impl.Summary = ev.Turn.Summary
		}
	case factory.EventCollected:
		if impl := round.Implement; impl != nil {
			impl.Stat = ev.Change.Stat
			if ev.Change.Empty() {
				impl.State = chunkd.ImplementEmpty
			}
		}
	case factory.EventChecking:
		round.State = chunkd.RoundChecking
	case factory.EventChecked:
		r.recordReviewsLocked(ridx, ev.Checks)
		passed := 0
		for _, c := range ev.Checks {
			if c.Status == factory.StatusPassed {
				passed++
			}
		}
		now := time.Now()
		round.State, round.EndedAt = chunkd.RoundDone, &now
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
		res := chunkd.ReviewResult{Prompt: c.Name, SidecarID: c.SidecarID, Error: c.Error, DurationMS: c.Duration.Milliseconds(), Status: string(c.Status)}
		for i, f := range c.Findings {
			f.Prompt = c.Name
			f.ID = fmt.Sprintf("%s-%d", c.Name, i+1)
			res.Findings = append(res.Findings, wireFinding(f))
			all = append(all, f)
		}
		if i := e.reviewIndexLocked(ridx, c.Name); i >= 0 {
			p := &e.s.Rounds[ridx].Reviews[i]
			p.Findings, p.Worth, p.Status = len(res.Findings), len(worthChanging(c.Findings)), string(c.Status)
		}
		rd.Results = append(rd.Results, res)
	}
	unique := review.DedupeFindings(all)
	e.s.Rounds[ridx].Findings = len(unique)
	e.s.Rounds[ridx].Worth = len(worthChanging(unique))
}

// wireFinding is a review's finding as the session API carries it.
func wireFinding(f review.Finding) chunkd.Finding {
	return chunkd.Finding{ID: f.ID, Prompt: f.Prompt, File: f.File, Line: f.Line, Severity: f.Severity, Body: f.Body, Patch: f.Patch}
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
		if p.SidecarID == sidecarID && p.State == chunkd.PromptRunning {
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
	round.Checks = append(round.Checks, chunkd.RoundCheck{
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
	f.Stat = rep.Stat
	if rep.StatErr != nil {
		f.StatError = rep.StatErr.Error()
	}
	f.WorktreeRemoved = rep.WorktreeRemoved
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
		if impl := round.Implement; impl != nil && impl.State == chunkd.ImplementRunning {
			impl.State = chunkd.ImplementFailed
			if err != nil {
				impl.Error = friendlyReviewError(err)
			}
		}
		// A round the loop stopped in after the implementer's turn, finding
		// nothing new to check, ended there: its reviews never ran.
		if err == nil && round.State == chunkd.RoundImplementing {
			round.State, round.EndedAt = chunkd.RoundDone, &now
			round.Note = "not checked: " + e.loopNote
			round.Reviews = nil
		}
	}
}
