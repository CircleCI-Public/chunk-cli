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

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/factory"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/all"
	"github.com/smeltdev/smelt/internal/harness/status"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/review"
	"github.com/smeltdev/smelt/internal/sidecar"
	"github.com/smeltdev/smelt/internal/sidecar/docker"
	"github.com/smeltdev/smelt/internal/smeltd"
)

// DefaultAttempts is how many rounds a factory session checks at most when the
// request does not say.
const DefaultAttempts = 3

// harnessAuth is the credential setup of the named harness.
func (d *daemon) harnessAuth(name harness.Name) HarnessAuth {
	if name == harness.Codex {
		return d.rcfg.Codex
	}
	return HarnessAuth{Credential: d.rcfg.Credential, BaseURL: d.rcfg.BaseURL, AuthError: d.rcfg.AuthError}
}

// factoryAuthError explains why a factory session on the given backend with the
// named harness cannot run, or "" when it can. Every backend needs the
// harness's credential; only the CircleCI backend needs a CircleCI client.
func (d *daemon) factoryAuthError(backend string, name harness.Name) string {
	auth := d.harnessAuth(name)
	if auth.Credential.Value == "" && auth.Refresh == nil {
		if auth.AuthError != "" {
			return auth.AuthError
		}
		return fmt.Sprintf("no %s credential configured for the smelt daemon — factory runs unavailable (run: smelt auth login %s)", name, name)
	}
	if config.BackendOrDefault(backend) == smeltd.BackendDocker {
		return ""
	}
	if d.rcfg.RunFactory == nil && d.circleCIClient() == nil {
		return "not authenticated to CircleCI — factory runs unavailable (run: smelt auth login)"
	}
	return ""
}

// anyFactoryAuthError is what the snapshot reports: empty when some harness can
// run, otherwise why the first, Claude, cannot.
func (d *daemon) anyFactoryAuthError(backend string) string {
	first := ""
	for _, h := range all.All() {
		msg := d.factoryAuthError(backend, h.Name())
		if msg == "" {
			return ""
		}
		if first == "" {
			first = msg
		}
	}
	return first
}

// factoryTarget is the org and image a factory request's sidecars use: the
// request's own, filled in from the project config. The Docker backend needs no
// CircleCI org, and an empty image is fine for it: it has its own default.
func factoryTarget(root string, cfg *config.ProjectConfig, req smeltd.FactoryRequest) (orgID, image string, err error) {
	orgID, image = req.OrgID, req.Image
	if req.Backend == smeltd.BackendDocker {
		// validation.sidecarImage is a CircleCI snapshot ID, not a Docker image.
		if image == "" && cfg.Factory != nil {
			image = cfg.Factory.Image
		}
		return orgID, image, nil
	}
	if orgID != "" && image != "" {
		return orgID, image, nil
	}
	cfgOrg, cfgImage, err := poolTarget(root, cfg)
	if err != nil && orgID == "" {
		return "", "", err
	}
	if orgID == "" {
		orgID = cfgOrg
	}
	if image == "" {
		image = cfgImage
	}
	return orgID, image, nil
}

// factoryBackend builds the backend a factory request names. Nil means the run
// uses the CircleCI backend it derives from the daemon's client. It is built
// when the request arrives so a daemon that cannot reach the Docker socket
// fails the request rather than the background run.
func factoryBackend(name string, cfg *config.ProjectConfig) (sidecar.Backend, error) {
	if name != smeltd.BackendDocker {
		return nil, nil
	}
	b, err := docker.New()
	if err != nil {
		return nil, fmt.Errorf("docker backend unavailable: %w", err)
	}
	if cfg.Factory != nil {
		b.ManagedDockerfile = cfg.Factory.Dockerfile
	}
	return b, nil
}

// startFactory validates a request, records a factory session and starts it in
// the background. It returns as soon as the session is accepted.
func (d *daemon) startFactory(req smeltd.FactoryRequest) (smeltd.Session, error) {
	if !config.ValidBackend(req.Backend) {
		return smeltd.Session{}, apiErr(http.StatusBadRequest, "unknown backend %q (want %s or %s)",
			req.Backend, smeltd.BackendCircleCI, smeltd.BackendDocker)
	}
	req.Backend = config.BackendOrDefault(req.Backend)
	if req.Harness == "" {
		req.Harness = config.HarnessClaude
	}
	agent, err := all.ByName(harness.Name(req.Harness))
	if err != nil {
		return smeltd.Session{}, apiErr(http.StatusBadRequest, "%v", err)
	}
	if msg := d.factoryAuthError(req.Backend, agent.Name()); msg != "" {
		return smeltd.Session{}, apiErr(http.StatusServiceUnavailable, "%s", msg)
	}
	auth := d.harnessAuth(agent.Name())
	if auth.Refresh != nil {
		if auth.Credential, err = auth.Refresh(context.Background()); err != nil {
			return smeltd.Session{}, apiErr(http.StatusServiceUnavailable, "%v", err)
		}
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" && req.Continue == "" {
		return smeltd.Session{}, apiErr(http.StatusBadRequest, "prompt required")
	}
	ps := d.lookupProject(req.ProjectRoot)
	if ps == nil {
		ps = d.adoptProject(req.ProjectRoot)
	}
	if ps == nil {
		return smeltd.Session{}, apiErr(http.StatusNotFound, "the smelt daemon is not tracking %q, and it is not the top of a git repository", req.ProjectRoot)
	}
	var cont *factory.Continuation
	record := &smeltd.FactoryRun{Prompt: prompt}
	if req.Continue != "" {
		from, err := factory.LoadRecord(ps.root, factory.ParseRunID(req.Continue))
		if errors.Is(err, factory.ErrNoRecord) {
			return smeltd.Session{}, apiErr(http.StatusNotFound, "%v", err)
		}
		if err != nil {
			return smeltd.Session{}, apiErr(http.StatusBadRequest, "%v", err)
		}
		cont = &factory.Continuation{From: from, Guidance: prompt}
		record = &smeltd.FactoryRun{Prompt: from.Prompt, Guidance: prompt, ContinuesRunID: from.RunID}
	}
	cfg, err := loadProjectConfig(ps.root)
	if err != nil {
		return smeltd.Session{}, apiErr(http.StatusBadRequest, "%v", err)
	}
	dir, err := sessionPromptsDir(ps.root, req.ReviewsDir)
	if err != nil {
		return smeltd.Session{}, apiErr(http.StatusBadRequest, "%v", err)
	}
	prompts, commands, err := factory.LoadChecks(dir, req.ReviewsDir == "", cfg, req.NoValidate)
	if err != nil {
		return smeltd.Session{}, apiErr(http.StatusBadRequest, "%v", err)
	}
	orgID, image, err := factoryTarget(ps.root, cfg, req)
	if err != nil {
		return smeltd.Session{}, apiErr(http.StatusBadRequest, "%v", err)
	}
	backend, err := factoryBackend(req.Backend, cfg)
	if err != nil {
		return smeltd.Session{}, apiErr(http.StatusServiceUnavailable, "%v", err)
	}
	author, err := cfg.Factory.CommitAuthor()
	if err != nil {
		return smeltd.Session{}, apiErr(http.StatusBadRequest, "factory.author: %v", err)
	}
	// The daemon's working directory is not the caller's, so a relative path
	// would put the log somewhere the caller did not mean.
	if req.Log != "" && req.Log != factory.LogDefault && !filepath.IsAbs(req.Log) {
		return smeltd.Session{}, apiErr(http.StatusBadRequest, "log must be an absolute path, got %q", req.Log)
	}
	attempts := req.Attempts
	if attempts <= 0 {
		attempts = DefaultAttempts
	}
	record.Attempts = attempts
	model := req.Model
	if model == "" {
		model = cfg.Factory.ModelFor(string(agent.Name()))
	}
	record.Harness, record.Model = string(agent.Name()), model
	record.KitOutdated = req.KitWarning != ""

	entry, ctx, busy := d.sessions.add(smeltd.Session{
		ProjectRoot: ps.root,
		Branch:      currentBranch(ps.root),
		HeadSHA:     headRef(ps.root),
		Factory:     record,
	}, cfg.Factory.ConcurrentRuns())
	if busy != "" {
		return smeltd.Session{}, apiErr(http.StatusConflict, "%s", busy)
	}
	opts := factory.RunOptions{
		Root:                    ps.root,
		Prompt:                  prompt,
		Continue:                cont,
		Attempts:                attempts,
		Reviewers:               factory.ReviewerCount(req.Reviewers, prompts),
		Prompts:                 prompts,
		Commands:                commands,
		Plan:                    !req.NoPlan && cfg.Factory.PlanEnabled(),
		GatesFirst:              cfg.Factory.GatesFirstEnabled(),
		Client:                  d.factoryClient(req.Backend),
		OrgID:                   orgID,
		Image:                   image,
		Backend:                 backend,
		Install:                 imageInstallCommand(req.Backend, cfg),
		Author:                  author,
		KeepSidecars:            req.KeepSidecars,
		Harness:                 agent,
		Credential:              auth.Credential,
		BaseURL:                 auth.BaseURL,
		Model:                   model,
		ImplementerInstructions: req.ImplementerInstructions,
		ImplementTimeout:        time.Duration(req.ImplementTimeoutSeconds) * time.Second,
		ReviewTimeout:           time.Duration(req.ReviewTimeoutSeconds) * time.Second,
		Warnings:                warnings(req),
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

	rec := &factoryRecorder{entry: entry, prompts: opts.Prompts, rounds: map[int]int{}, harness: opts.Harness.Name()}
	root := opts.Root
	opts.Status = func(level iostream.Level, msg string) {
		log.Printf("smeltd: factory (%s): %s", root, msg)
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
	case factory.ResultBlocked:
		return "the implementer is blocked: " + o.Blocked
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

	// harness is the agent the run uses.
	harness harness.Name
}

func (r *factoryRecorder) started(runID string, wt factory.Worktree) {
	e := r.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	f := e.s.Factory
	f.RunID, f.Worktree, f.Branch, f.Baseline, f.Head = runID, wt.Path, wt.Branch, wt.Baseline, wt.Head
	f.BaseBranch, f.DefaultBranch = wt.BaseBranch, wt.DefaultBranch
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
	// The plan comes before the first round.
	if ev.Kind == factory.EventPlanning {
		r.progress(iostream.LevelStep, "planning")
		return
	}
	if ev.Kind == factory.EventPlanReviewed {
		r.planReviewed(ev)
		return
	}
	ridx := r.round(ev.Round)
	e := r.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	round := &e.s.Rounds[ridx]
	switch ev.Kind {
	case factory.EventPlanning, factory.EventPlanReviewed:
		// Recorded above: the plan comes before the first round.
	case factory.EventImplementing:
		round.State = smeltd.RoundImplementing
		round.Implement = &smeltd.RoundImplement{State: smeltd.ImplementRunning}
	case factory.EventImplemented:
		if impl := round.Implement; impl != nil {
			impl.State = smeltd.ImplementApplied
			impl.DurationMS = ev.Turn.Duration.Milliseconds()
			impl.CostUSD = ev.Turn.CostUSD
			impl.Harness, impl.Model = string(ev.Turn.Harness), ev.Turn.Model
			impl.InputTokens, impl.CachedTokens, impl.OutputTokens = ev.Turn.InputTokens, ev.Turn.CachedTokens, ev.Turn.OutputTokens
			impl.RateLimit = ev.Turn.RateLimit
			r.recordRateLimit(ev.Turn.RateLimit)
			impl.Summary = ev.Turn.Summary
			impl.Nudged, impl.Unproven = ev.Turn.Nudged, ev.Turn.Unproven
		}
	case factory.EventCollected:
		if impl := round.Implement; impl != nil {
			impl.Stat = ev.Change.Stat
			if ev.Change.Empty() {
				impl.State = smeltd.ImplementEmpty
			}
		}
	case factory.EventChecking:
		round.State = smeltd.RoundChecking
	case factory.EventChecked:
		r.recordReviewsLocked(ridx, ev.Checks)
		passed, skipped := 0, 0
		for _, c := range ev.Checks {
			switch c.Status {
			case factory.StatusPassed:
				passed++
			case factory.StatusSkipped:
				skipped++
			case factory.StatusFailed, factory.StatusErrored:
			}
		}
		now := time.Now()
		round.State, round.EndedAt = smeltd.RoundDone, &now
		round.Note = fmt.Sprintf("%d of %d checks passed", passed, len(ev.Checks))
		if skipped > 0 {
			round.Note += fmt.Sprintf("; %d review(s) skipped (gates failed)", skipped)
		}
		if impl := round.Implement; impl != nil && len(impl.Unproven) > 0 {
			round.Note += "; the implementer claimed gates it did not run after its last edit: " + strings.Join(impl.Unproven, ", ")
		}
		e.s.Factory.SkippedTests = factory.Outcome{Checks: ev.Checks}.Skipped()
	}
}

// planReviewed records the settled plan. It reads as a line of the run's
// progress, so a follower sees the plan before the first turn.
func (r *factoryRecorder) planReviewed(ev factory.Event) {
	msg := "plan reviewed"
	if n := len(ev.Findings); n > 0 {
		msg += fmt.Sprintf(" (%d finding(s) still open, sent with the plan)", n)
	}
	r.progress(iostream.LevelDone, msg)
	e := r.entry
	e.mu.Lock()
	defer e.mu.Unlock()
	e.s.Factory.Plan = wirePlan(ev.Plan)
}

// wirePlan is a plan as the session API carries it.
func wirePlan(p *factory.Plan) *smeltd.Plan {
	if p == nil {
		return nil
	}
	out := &smeltd.Plan{Summary: p.Summary, Risks: p.Risks, Questions: p.Questions, CannotVerifyHere: p.CannotVerifyHere}
	for _, f := range p.Files {
		out.Files = append(out.Files, smeltd.PlanFile(f))
	}
	for _, a := range p.Acceptance {
		out.Acceptance = append(out.Acceptance, smeltd.PlanItem(a))
	}
	return out
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
		res := smeltd.ReviewResult{Prompt: c.Name, SidecarID: c.SidecarID, Error: c.Error, DurationMS: c.Duration.Milliseconds(), Status: string(c.Status), Harness: string(r.harness)}
		for i, f := range c.Findings {
			f.Prompt = c.Name
			f.ID = fmt.Sprintf("%s-%d", c.Name, i+1)
			res.Findings = append(res.Findings, wireFinding(f))
			all = append(all, f)
		}
		if i := e.reviewIndexLocked(ridx, c.Name); i >= 0 {
			p := &e.s.Rounds[ridx].Reviews[i]
			p.Findings, p.Worth, p.Status = len(res.Findings), len(worthChanging(c.Findings)), string(c.Status)
			if c.Status == factory.StatusSkipped {
				p.State = smeltd.PromptSkipped
			}
		}
		rd.Results = append(rd.Results, res)
	}
	unique := review.DedupeFindings(all)
	e.s.Rounds[ridx].Findings = len(unique)
	e.s.Rounds[ridx].Worth = len(worthChanging(unique))
}

// wireFinding is a review's finding as the session API carries it.
func wireFinding(f review.Finding) smeltd.Finding {
	return smeltd.Finding{ID: f.ID, Prompt: f.Prompt, File: f.File, Line: f.Line, Severity: f.Severity, Body: f.Body, Patch: f.Patch}
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
		if p.SidecarID == sidecarID && p.State == smeltd.PromptRunning {
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
	round.Checks = append(round.Checks, smeltd.RoundCheck{
		Name:       c.Name,
		Status:     string(c.Status),
		SidecarID:  c.SidecarID,
		DurationMS: c.Duration.Milliseconds(),
		Error:      c.Error,
		Output:     c.Output,
		Skipped:    c.Skipped,
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
	if rep.Notes != nil {
		n := smeltd.Notes(*rep.Notes)
		f.Notes = &n
	}
	f.WorktreeRemoved = rep.WorktreeRemoved
	f.KeptSidecars = rep.KeptSidecars
	f.Log = rep.Log
	if rep.Started {
		f.Result = string(rep.Outcome.Result)
		f.Rounds = rep.Outcome.Rounds
		f.Blocked = rep.Outcome.Blocked
		f.SkippedTests = rep.Outcome.Skipped()
		for _, fd := range rep.Outcome.Incomplete() {
			f.Incomplete = append(f.Incomplete, factory.FindingTitle(fd))
		}
	}
	if err == nil {
		e.loopNote = factoryResultNote(rep.Outcome)
		e.loopFailed = rep.Outcome.Result != factory.ResultPassed
	}
	now := time.Now()
	for i := range e.s.Rounds {
		round := &e.s.Rounds[i]
		if impl := round.Implement; impl != nil && impl.State == smeltd.ImplementRunning {
			impl.State = smeltd.ImplementFailed
			if err != nil {
				impl.Error = friendlyReviewError(err)
			}
		}
		// A round the loop stopped in after the implementer's turn, finding
		// nothing new to check, ended there: its reviews never ran.
		if err == nil && round.State == smeltd.RoundImplementing {
			round.State, round.EndedAt = smeltd.RoundDone, &now
			round.Note = "not checked: " + e.loopNote
			round.Reviews = nil
		}
	}
}

// imageInstallCommand is the dependency install to run on each sidecar. Only
// the docker backend needs it: its image bakes the package manager's store, not
// an installed tree.
func imageInstallCommand(backend string, cfg *config.ProjectConfig) string {
	if backend != smeltd.BackendDocker {
		return ""
	}
	return cfg.InstallCommand()
}

// recordRateLimit keeps the run's last rate-limit signal where `smelt harness
// status` finds it. The status is best effort, so a failure is not the run's.
func (r *factoryRecorder) recordRateLimit(rl *harness.RateLimit) {
	if rl == nil {
		return
	}
	if dir, err := status.Dir(); err == nil {
		status.RecordSignal(dir, r.harness, *rl, time.Now())
	}
}

// warnings are what the client flagged about the run's setup, for its log.
func warnings(req smeltd.FactoryRequest) []string {
	if req.KitWarning == "" {
		return nil
	}
	return []string{req.KitWarning}
}
