package watchd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// poolCloseTimeout bounds how long a kept pool waits for pending sandbox work
// when a round ends.
const poolCloseTimeout = 30 * time.Second

// ReviewPool is the sandbox pool one round draws from. It mirrors the methods of
// sidecar.Pool that RunPass needs, as fields, so a test can supply a fake
// without booting anything.
type ReviewPool struct {
	Acquire func(context.Context) (*sidecar.PoolEntry, error)
	// AcquireID checks out one member by sidecar ID, failing with
	// sidecar.ErrNotPoolMember when the pool cannot hand it out. Nil means the
	// pool cannot pin members, and any member is used instead.
	AcquireID func(context.Context, string) (*sidecar.PoolEntry, error)
	Release   func(*sidecar.PoolEntry)
	WaitReady func(context.Context) error
	Close     func(context.Context)
}

// ReviewPoolSpec says what pool a round needs.
type ReviewPoolSpec struct {
	// Root is the tracked project; its config decides org and image.
	Root string
	// WorkDir is the tree synced to the sandboxes: the user's project itself,
	// or a factory session's worktree.
	WorkDir string
	Size    int
	// Name names the pool; empty means review.PoolName.
	Name string
	// StateRoot is where the pool's state is kept; empty means WorkDir. A
	// factory session keeps it out of the worktree, whose contents are the work.
	StateRoot string
	// Image overrides the project's configured sidecar image.
	Image string
}

// SubmitFunc submits a script on a pool member and returns its command ID.
type SubmitFunc func(ctx context.Context, entry *sidecar.PoolEntry, script string, env map[string]string) (string, error)

// StreamFunc reads a submitted command's output to its end and returns its
// exit code.
type StreamFunc func(ctx context.Context, entry *sidecar.PoolEntry, commandID string, onOutput circleci.OutputFn) (int, error)

// ReviewConfig is everything the daemon needs to run sessions. The credential is
// resolved once by the caller at daemon start; it is only ever placed in the
// environment of a Claude command, and never logged or put in a snapshot.
type ReviewConfig struct {
	Credential review.Credential
	// BaseURL is forwarded to claude when it is not Anthropic's own.
	BaseURL string
	// AuthError explains a missing Credential, reported in the snapshot so an
	// absent capability is explained rather than silent.
	AuthError string

	// The fields below are test seams. Left nil, the daemon builds a real pool
	// from its CircleCI client and talks to the sandbox through it.
	NewPool func(ctx context.Context, spec ReviewPoolSpec) (*ReviewPool, error)
	// DeletePool deletes a pool's sidecars and its state, for a factory session
	// that has finished with them.
	DeletePool func(ctx context.Context, spec ReviewPoolSpec) error
	Submit     SubmitFunc
	Stream     StreamFunc
}

// Option customizes RunDaemon.
type Option func(*daemonOptions)

type daemonOptions struct {
	review ReviewConfig
}

// WithReview configures the daemon's review capability.
func WithReview(cfg ReviewConfig) Option {
	return func(o *daemonOptions) { o.review = cfg }
}

// apiError is a refusal carrying the HTTP status the route should answer with.
type apiError struct {
	status int
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func apiErr(status int, format string, args ...any) *apiError {
	return &apiError{status: status, msg: fmt.Sprintf(format, args...)}
}

// reviewAuthError explains why sessions cannot run, or "" when they can.
func (d *daemon) reviewAuthError() string {
	if d.rcfg.Credential.Value == "" {
		if d.rcfg.AuthError != "" {
			return d.rcfg.AuthError
		}
		return "no Claude credential configured for the watch daemon — sessions unavailable"
	}
	if d.rcfg.NewPool == nil && d.client == nil {
		return "not authenticated to CircleCI — sessions unavailable (run: chunk auth login)"
	}
	return ""
}

// lookupProject finds a tracked project by its own spelling or its
// symlink-resolved one. A root that is registered but not yet picked up by the
// poll loop is adopted, which is what lets a client register a project and start
// a session in one motion.
func (d *daemon) lookupProject(root string) *projectState {
	if root == "" {
		return nil
	}
	want := canonicalRoot(root)
	find := func() *projectState {
		d.mu.RLock()
		defer d.mu.RUnlock()
		for _, ps := range d.projects {
			if ps.root == root || ps.canonRoot == want {
				return ps
			}
		}
		return nil
	}
	if ps := find(); ps != nil {
		return ps
	}
	known, _ := sidecar.AllProjectRoots()
	for _, k := range known {
		if k != want && canonicalRoot(k) != want {
			continue
		}
		ps := d.initProject(k)
		if ps == nil {
			return nil
		}
		d.mu.Lock()
		if existing, ok := d.projects[k]; ok {
			ps = existing
		} else {
			d.projects[k] = ps
		}
		d.mu.Unlock()
		return ps
	}
	return nil
}

// sessionPromptsDir resolves the prompts directory inside the project. The
// request names a place under the project root only.
func sessionPromptsDir(root, rel string) (string, error) {
	if rel == "" {
		rel = review.DefaultDir
	}
	if filepath.IsAbs(rel) {
		return "", errors.New("prompts_dir must be relative to the project root")
	}
	dir := filepath.Join(root, rel)
	within, err := filepath.Rel(root, dir)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", errors.New("prompts_dir must stay inside the project")
	}
	return dir, nil
}

// sessionPlan is what a session runs, settled when it is accepted.
type sessionPlan struct {
	req SessionRequest
	// root is the project the daemon tracks.
	root      string
	prompts   []review.Prompt
	commands  []config.Command
	maxRounds int
	// workDir is the session's worktree and stateRoot its directory, where its
	// pool keeps state; both are set once the worktree exists.
	workDir   string
	stateRoot string
}

// poolSpec is the pool the session's turns and rounds draw from: one member
// per review, capped at the requested parallelism, and one more for the
// implementer, which also runs validation.
func (p *sessionPlan) poolSpec() ReviewPoolSpec {
	return ReviewPoolSpec{
		Root:      p.root,
		WorkDir:   p.workDir,
		Size:      review.PoolSize(p.req.Parallelism, len(p.prompts)) + 1,
		Name:      factoryPoolName,
		StateRoot: p.stateRoot,
		Image:     p.req.Image,
	}
}

// scopedPrompts are the review prompts as the reviewers are given them. The
// prompt files are written for a developer's branch; in a session the change
// is the implementer's uncommitted work on top of where the user started.
func (p *sessionPlan) scopedPrompts() []review.Prompt {
	out := make([]review.Prompt, len(p.prompts))
	for i, pr := range p.prompts {
		out[i] = review.Prompt{Name: pr.Name, Body: reviewScope + pr.Body}
	}
	return out
}

// reviewScope tells each reviewer what the change under review is.
const reviewScope = "The change under review is the uncommitted work in this repository: run " +
	"`git diff HEAD` to see it, including new files. Committed history is the baseline and is not under review.\n\n"

// startSession validates a request, records a session and starts it in the
// background. It returns as soon as the session is accepted.
func (d *daemon) startSession(req SessionRequest) (Session, error) {
	if msg := d.reviewAuthError(); msg != "" {
		return Session{}, apiErr(http.StatusServiceUnavailable, "%s", msg)
	}
	if strings.TrimSpace(req.Task) == "" {
		return Session{}, apiErr(http.StatusBadRequest, "a session needs a task")
	}
	ps := d.lookupProject(req.ProjectRoot)
	if ps == nil {
		return Session{}, apiErr(http.StatusNotFound, "the watch daemon is not tracking %q", req.ProjectRoot)
	}
	plan := &sessionPlan{req: req, root: ps.root}
	if err := plan.load(); err != nil {
		return Session{}, err
	}

	entry, ctx := d.sessions.add(Session{
		ProjectRoot: ps.root,
		Branch:      currentBranch(ps.root),
		HeadSHA:     headRef(ps.root),
		Task:        req.Task,
	})
	go d.executeSession(ctx, entry, plan)
	return entry.snapshot(), nil
}

// load reads what the session checks the work with, and refuses a request
// that cannot run.
func (p *sessionPlan) load() error {
	if p.req.MaxRounds < 0 || p.req.MaxRounds > MaxRounds {
		return apiErr(http.StatusBadRequest, "a session runs between 1 and %d rounds", MaxRounds)
	}
	p.maxRounds = DefaultRounds
	if p.req.MaxRounds > 0 {
		p.maxRounds = p.req.MaxRounds
	}

	dir, err := sessionPromptsDir(p.root, p.req.PromptsDir)
	if err != nil {
		return apiErr(http.StatusBadRequest, "%v", err)
	}
	prompts, err := review.LoadPrompts(dir)
	// The work can be checked by validation alone, so the default prompts
	// directory is optional; one that was named is not.
	optional := p.req.PromptsDir == ""
	switch {
	case optional && (errors.Is(err, review.ErrNoPrompts) || errors.Is(err, fs.ErrNotExist)):
	case errors.Is(err, review.ErrNoPrompts):
		return apiErr(http.StatusBadRequest, "no prompts found in %s", dir)
	case err != nil:
		return apiErr(http.StatusBadRequest, "read prompts: %v", err)
	}
	p.prompts = prompts

	if !p.req.NoValidate {
		cfg, err := config.LoadProjectConfig(p.root)
		switch {
		case err == nil:
			p.commands = validationCommands(cfg.Commands)
		case !errors.Is(err, fs.ErrNotExist):
			return apiErr(http.StatusBadRequest, "load project config: %v", err)
		}
	}
	if len(p.prompts) == 0 && len(p.commands) == 0 {
		return apiErr(http.StatusBadRequest,
			"nothing to check the work with: add review prompts to %s, or validation commands with 'chunk init'", review.DefaultDir)
	}
	return nil
}

// executeSession runs a session and settles it. It always closes entry.done.
func (d *daemon) executeSession(ctx context.Context, entry *sessionEntry, plan *sessionPlan) {
	defer close(entry.done)
	defer entry.cancel()

	err := d.runFactory(ctx, entry, plan)
	d.settleSession(entry, err)
}

// roundPool is a round's sandboxes.
type roundPool struct{ *ReviewPool }

func (p roundPool) close(ctx context.Context) {
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), poolCloseTimeout)
	defer cancel()
	p.Close(closeCtx)
}

// openRoundPool opens the sandbox pool for one round and waits for it to be
// ready. The pool is named and persisted, so a later round reuses the same warm
// sandboxes and only has to sync what changed.
func (d *daemon) openRoundPool(ctx context.Context, plan *sessionPlan) (roundPool, error) {
	newPool := d.rcfg.NewPool
	if newPool == nil {
		newPool = d.defaultReviewPool
	}
	pool, err := newPool(ctx, plan.poolSpec())
	if err != nil {
		return roundPool{}, fmt.Errorf("prepare sandbox pool: %w", err)
	}
	rp := roundPool{pool}
	if err := review.WaitReady(ctx, pool.WaitReady); err != nil {
		rp.close(ctx)
		return roundPool{}, err
	}
	return rp, nil
}

// runReviews runs every review prompt once, in parallel on the round's pool.
func (d *daemon) runReviews(ctx context.Context, entry *sessionEntry, ridx int, plan *sessionPlan, pool roundPool) ([]review.Result, error) {
	if len(plan.prompts) == 0 {
		return nil, nil
	}
	req := plan.req
	opts := review.Options{
		Credential: d.rcfg.Credential,
		BaseURL:    d.rcfg.BaseURL,
		Model:      req.Model,
		Timeout:    time.Duration(req.TimeoutSeconds) * time.Second,
		ProgressFn: func(ev review.ProgressEvent) { entry.applyProgress(ridx, ev) },
		// Fixes are chosen from findings a program can read. The prose review is
		// still asked for and still kept.
		StructuredFindings: true,
	}
	exec := d.execerFor(plan.root, func(sidecarID, commandID string) string {
		return entry.attribute(ridx, sidecarID, commandID)
	})
	return review.RunPass(ctx, pool.Acquire, pool.Release, exec, plan.scopedPrompts(), opts)
}

// defaultDeletePool deletes a session's pool sidecars and its state.
func (d *daemon) defaultDeletePool(ctx context.Context, spec ReviewPoolSpec) error {
	if d.client == nil {
		return nil
	}
	return sidecar.DeletePool(ctx, d.client, spec.StateRoot, spec.Name)
}

// defaultReviewPool builds a real pool the way `chunk review` does: named so its
// state persists and a later round reuses the warm sandboxes.
func (d *daemon) defaultReviewPool(ctx context.Context, spec ReviewPoolSpec) (*ReviewPool, error) {
	cfg, err := config.LoadProjectConfig(spec.Root)
	if err != nil {
		return nil, fmt.Errorf("load project config (run 'chunk init' in the project): %w", err)
	}
	orgID := cfg.OrgID
	if orgID == "" {
		orgID, _ = config.ResolveOrgID(spec.Root)
	}
	if orgID == "" {
		return nil, errors.New("no CircleCI org ID configured for this project")
	}
	image := spec.Image
	if image == "" && cfg.Validation != nil {
		image = cfg.Validation.SidecarImage
	}
	name := spec.Name
	if name == "" {
		name = review.PoolName
	}
	status := func(_ iostream.Level, msg string) {
		log.Printf("watchd: session pool %s (%s): %s", name, spec.WorkDir, msg)
	}
	pool, err := sidecar.NewPool(ctx, d.client, sidecar.PoolOptions{
		Size:      spec.Size,
		Name:      name,
		OrgID:     orgID,
		Image:     image,
		WorkDir:   spec.WorkDir,
		StateRoot: spec.StateRoot,
	}, status)
	if err != nil {
		return nil, err
	}
	return &ReviewPool{
		Acquire:   pool.Acquire,
		AcquireID: pool.AcquireID,
		Release:   pool.Release,
		WaitReady: pool.WaitSynced,
		Close:     pool.Close,
	}, nil
}

// execerFor runs each Claude command through the exec API in two phases, submit
// then stream, so the command can be registered with the output store between
// them. That registration is what puts a command's log in the dashboard's output
// pane while it runs: a call that submits and streams in one step would leave
// the command ID with nobody to hand it to.
func (d *daemon) execerFor(root string, attribute func(sidecarID, commandID string) string) review.Execer {
	submit := d.rcfg.Submit
	if submit == nil {
		submit = func(ctx context.Context, pe *sidecar.PoolEntry, script string, env map[string]string) (string, error) {
			return pe.Client.SubmitExec(ctx, pe.ID, "sh", []string{"-c", script}, env)
		}
	}
	stream := d.rcfg.Stream
	if stream == nil {
		stream = func(ctx context.Context, pe *sidecar.PoolEntry, commandID string, onOutput circleci.OutputFn) (int, error) {
			resp, err := pe.Client.StreamOutput(ctx, commandID, "", onOutput)
			if err != nil {
				return 0, err
			}
			return resp.ExitCode, nil
		}
	}
	return func(ctx context.Context, pe *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(string)) (int, error) {
		commandID, err := submit(ctx, pe, script, env)
		if err != nil {
			return 0, fmt.Errorf("submit: %w", err)
		}
		if onSubmitted != nil {
			onSubmitted(commandID)
		}
		// An empty name means the command is bookkeeping, not worth a log pane.
		if name := attribute(pe.ID, commandID); name != "" {
			d.out.register(CommandReg{
				CommandID:   commandID,
				SidecarID:   pe.ID,
				ProjectRoot: root,
				Op:          "review",
				Name:        name,
				SubmittedAt: time.Now(),
			}, streamFor(pe.Client))
		}
		code, err := stream(ctx, pe, commandID, onOutput)
		if err != nil {
			return 0, fmt.Errorf("stream output: %w", err)
		}
		return code, nil
	}
}

// beginRound adds the next round with its reviews and validation commands
// queued, and returns its index.
func (e *sessionEntry) beginRound(prompts []review.Prompt, commands []config.Command) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := Round{Number: len(e.s.Rounds) + 1, State: RoundReviewing, StartedAt: time.Now()}
	for _, p := range prompts {
		r.Reviews = append(r.Reviews, ReviewPrompt{Name: p.Name, State: PromptQueued})
	}
	for _, c := range commands {
		r.Reviews = append(r.Reviews, ReviewPrompt{Name: c.Name, Kind: CheckValidate, State: PromptQueued})
	}
	e.s.Rounds = append(e.s.Rounds, r)
	e.details = append(e.details, RoundDetail{Number: r.Number})
	return len(e.s.Rounds) - 1
}

// applyProgress records one review's state change from RunPass.
func (e *sessionEntry) applyProgress(ridx int, ev review.ProgressEvent) {
	e.mu.Lock()
	defer e.mu.Unlock()
	i := e.reviewIndexLocked(ridx, ev.Prompt)
	if i < 0 {
		return
	}
	p := &e.s.Rounds[ridx].Reviews[i]
	switch ev.State {
	case review.StateQueued:
		p.State = PromptQueued
	case review.StateRunning:
		p.State = PromptRunning
		e.sidecarReview[ev.SidecarID] = ev.Prompt
	case review.StateDone:
		p.State = PromptDone
	case review.StateFailed:
		p.State = PromptFailed
	}
	if ev.SidecarID != "" {
		p.SidecarID = ev.SidecarID
	}
	if ev.Duration > 0 {
		p.DurationMS = ev.Duration.Milliseconds()
	}
	p.Error = ev.Error
}

// attribute ties a submitted command to the review running on its sandbox and
// returns a name for the command.
func (e *sessionEntry) attribute(ridx int, sidecarID, commandID string) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	name := e.sidecarReview[sidecarID]
	if i := e.reviewIndexLocked(ridx, name); i >= 0 {
		e.s.Rounds[ridx].Reviews[i].CommandID = commandID
	}
	if name == "" {
		name = sidecarID
	}
	return fmt.Sprintf("round %d review: %s", e.s.Rounds[ridx].Number, name)
}

// finishReviews stores a round's review results, and the results of its
// validation commands, which come already in the record's form.
func (e *sessionEntry) finishReviews(ridx int, results []review.Result, validation []ReviewResult) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rd := &e.details[ridx]
	rd.Results = rd.Results[:0]
	var all []review.Finding
	for _, r := range results {
		res := ReviewResult{
			Prompt:     r.Prompt,
			SidecarID:  r.SidecarID,
			Output:     truncateOutput(r.Output),
			Error:      r.Error,
			DurationMS: r.Duration.Milliseconds(),
		}
		res.FindingsDropped = r.Parsed.Dropped
		for i, f := range r.Parsed.Findings {
			f.Prompt = r.Prompt
			f.ID = fmt.Sprintf("%s-%d", r.Prompt, i+1)
			res.Findings = append(res.Findings, f)
		}
		if i := e.reviewIndexLocked(ridx, r.Prompt); i >= 0 {
			e.s.Rounds[ridx].Reviews[i].Findings = len(res.Findings)
		}
		all = append(all, res.Findings...)
		rd.Results = append(rd.Results, res)
	}
	for _, res := range validation {
		all = append(all, res.Findings...)
		rd.Results = append(rd.Results, res)
	}
	// The round's counts are of distinct findings: several reviews flagging the
	// same line count once.
	unique := review.DedupeFindings(all)
	e.s.Rounds[ridx].Findings = len(unique)
	e.s.Rounds[ridx].Worth = len(worthChanging(unique))
}

// worthChanging keeps the findings serious enough to fix: severity high or
// medium.
func worthChanging(findings []review.Finding) []review.Finding {
	var out []review.Finding
	for _, f := range findings {
		if f.WorthChanging() {
			out = append(out, f)
		}
	}
	return out
}

// endRound closes a round in state with a note.
func (e *sessionEntry) endRound(ridx int, state RoundState, note string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	r := &e.s.Rounds[ridx]
	r.State, r.Note, r.EndedAt = state, note, &now
}

// friendlyReviewError phrases the pass-level failures a person can act on. The
// credential is never named or echoed.
func friendlyReviewError(err error) string {
	switch {
	case errors.Is(err, review.ErrClaudeMissing):
		return "Claude Code is not installed on the sandboxes — install it in the sandbox image and set validation.sidecarImage"
	case errors.Is(err, review.ErrCredentialRejected):
		return "Anthropic rejected the watch daemon's Claude credential — replace it and restart the daemon"
	}
	return err.Error()
}

// settleSession closes a session that has ended, however it ended.
func (d *daemon) settleSession(entry *sessionEntry, runErr error) {
	entry.mu.Lock()
	defer entry.mu.Unlock()

	now := time.Now()
	entry.s.EndedAt = &now
	// The stage the session was in is the one that ended it; any after it that
	// were waiting their turn never ran.
	stage := entry.activeStageLocked()
	switch {
	case entry.cancelled:
		entry.s.State = SessionCancelled
		stage.State, stage.Note = StageFailed, "cancelled"
	case runErr != nil:
		entry.s.State = SessionFailed
		entry.s.Error = friendlyReviewError(runErr)
		stage.State, stage.Note = StageFailed, entry.s.Error
	default:
		entry.s.State = SessionDone
		stage.State, stage.Note = StageDone, entry.loopNote
		entry.s.Outcome = entry.outcome
	}
	for i := range entry.s.Stages {
		if st := &entry.s.Stages[i]; st.State == StagePending {
			st.State = StageSkipped
		}
	}

	// Anything still in flight when the session ended did not finish; say so
	// rather than leaving it shown as running on a session that is over.
	reason := entry.s.Error
	if entry.cancelled {
		reason = "cancelled"
	}
	if reason == "" {
		reason = "did not finish"
	}
	settleFix := func(fix *RoundFix) {
		// A turn stopped by a cancel failed with whatever the stop made of it
		// ("context canceled" from three layers down): that is the cancel again.
		if fix != nil && (fix.State == FixRunning || (entry.cancelled && fix.State == FixFailed)) {
			fix.State, fix.Error, fix.Activity = FixFailed, reason, ""
		}
	}
	settleFix(entry.s.Implement)
	for ri := range entry.s.Rounds {
		r := &entry.s.Rounds[ri]
		if r.State == RoundDone || r.State == RoundFailed {
			continue
		}
		r.State, r.EndedAt = RoundFailed, &now
		if r.Note == "" {
			r.Note = reason
		}
		settleFix(r.Fix)
		for i := range r.Reviews {
			p := &r.Reviews[i]
			switch {
			case entry.cancelled && p.State != PromptDone:
				// Whatever a stopped review reported ("context canceled" from
				// three layers down) is just the cancellation again.
				p.State, p.Error = PromptFailed, reason
			case p.State == PromptQueued || p.State == PromptRunning:
				p.State, p.Error = PromptFailed, reason
			}
		}
	}
}

// activeStageLocked is the stage the session is in: the first running one,
// else the review loop.
func (e *sessionEntry) activeStageLocked() *Stage {
	for i := range e.s.Stages {
		switch e.s.Stages[i].State {
		case StageRunning:
			return &e.s.Stages[i]
		case StageNotBuilt, StagePending, StageDone, StageFailed, StageSkipped:
		}
	}
	return e.stageLocked(StageReviewLoop)
}
