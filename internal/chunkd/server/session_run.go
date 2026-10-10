package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// SubmitFunc submits a script on a pool member and returns its command ID.
type SubmitFunc func(ctx context.Context, entry *sidecar.PoolEntry, script string, env map[string]string) (string, error)

// StreamFunc reads a submitted command's output to its end and returns its
// exit code.
type StreamFunc func(ctx context.Context, entry *sidecar.PoolEntry, commandID string, onOutput iostream.OutputFn) (int, error)

// ReviewConfig is everything the daemon needs to run factory sessions. The
// credential is resolved once by the caller at daemon start; it is only ever
// placed in the environment of a Claude command, and never logged or put in a
// snapshot.
type ReviewConfig struct {
	Credential review.Credential
	// BaseURL is forwarded to claude when it is not Anthropic's own.
	BaseURL string
	// AuthError explains a missing Credential, reported in the snapshot so an
	// absent capability is explained rather than silent.
	AuthError string

	// The fields below are test seams. Left nil, the daemon talks to the
	// sandbox through the pool entry's own client.
	Submit SubmitFunc
	Stream StreamFunc
	// RunFactory runs a factory session; factory.Run when nil.
	RunFactory func(ctx context.Context, opts factory.RunOptions) (factory.Report, error)
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

// adoptProject starts tracking root, a project the daemon has not been told
// about, so that a client asking for work on it need not register it first:
// the registry is the daemon's to keep. Only the top of a git repository is
// adopted, the root the client is expected to send; anything else is nil.
func (d *daemon) adoptProject(root string) *projectState {
	if root == "" || !filepath.IsAbs(root) {
		return nil
	}
	top := gitutil.TopLevelCtx(context.Background(), root)
	if top == "" || canonicalRoot(top) != canonicalRoot(root) {
		return nil
	}
	canon := config.CanonicalProjectRoot(top)
	dataDir, err := config.ProjectDataDir(canon)
	if err != nil {
		return nil
	}
	if err := sidecar.RegisterProjectRoot(dataDir, canon); err != nil {
		log.Printf("chunkd: register %s: %v", canon, err)
		return nil
	}
	return d.lookupProject(canon)
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

// loadProjectConfig reads the configuration of a project the daemon runs work
// for.
func loadProjectConfig(root string) (*config.ProjectConfig, error) {
	cfg, err := config.LoadProjectConfig(root)
	if err != nil {
		return nil, fmt.Errorf("load project config (run 'chunk init' in the project): %w", err)
	}
	return cfg, nil
}

// poolTarget is the org a project's sidecars are created in and the image they
// start from, as `chunk review` resolves them.
func poolTarget(root string, cfg *config.ProjectConfig) (orgID, image string, err error) {
	orgID = cfg.OrgID
	if orgID == "" {
		orgID, _ = config.ResolveOrgID(root)
	}
	if orgID == "" {
		return "", "", errors.New("no CircleCI org ID configured for this project")
	}
	if cfg.Validation != nil {
		image = cfg.Validation.SidecarImage
	}
	return orgID, image, nil
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
			return d.client.SubmitExec(ctx, pe.ID, "sh", []string{"-c", script}, env)
		}
	}
	stream := d.rcfg.Stream
	if stream == nil {
		stream = func(ctx context.Context, pe *sidecar.PoolEntry, commandID string, onOutput iostream.OutputFn) (int, error) {
			resp, err := d.client.StreamOutput(ctx, commandID, "", onOutput)
			if err != nil {
				return 0, err
			}
			return resp.ExitCode, nil
		}
	}
	return func(ctx context.Context, pe *sidecar.PoolEntry, script string, env map[string]string, onOutput iostream.OutputFn, onSubmitted func(string)) (int, error) {
		commandID, err := submit(ctx, pe, script, env)
		if err != nil {
			return 0, fmt.Errorf("submit: %w", err)
		}
		if onSubmitted != nil {
			onSubmitted(commandID)
		}
		// An empty name means the command is bookkeeping, not worth a log pane.
		if name := attribute(pe.ID, commandID); name != "" {
			d.out.register(chunkd.CommandReg{
				CommandID:   commandID,
				SidecarID:   pe.ID,
				ProjectRoot: root,
				Op:          "review",
				Name:        name,
				SubmittedAt: time.Now(),
			}, streamFor(d.client))
		}
		code, err := stream(ctx, pe, commandID, onOutput)
		if err != nil {
			return 0, fmt.Errorf("stream output: %w", err)
		}
		return code, nil
	}
}

// beginRound adds the next round with its reviews queued, and returns its index.
func (e *sessionEntry) beginRound(prompts []review.Prompt) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := chunkd.Round{Number: e.nextNumberLocked(), State: chunkd.RoundStarted, StartedAt: time.Now()}
	for _, p := range prompts {
		r.Reviews = append(r.Reviews, chunkd.ReviewPrompt{Name: p.Name, State: chunkd.PromptQueued})
	}
	e.s.Rounds = append(e.s.Rounds, r)
	e.details = append(e.details, chunkd.RoundDetail{Number: r.Number})
	return len(e.s.Rounds) - 1
}

// nextNumberLocked is the number of the next round.
func (e *sessionEntry) nextNumberLocked() int {
	return len(e.s.Rounds) + 1
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
		p.State = chunkd.PromptQueued
	case review.StateRunning:
		p.State = chunkd.PromptRunning
	case review.StateDone:
		p.State = chunkd.PromptDone
	case review.StateFailed:
		p.State = chunkd.PromptFailed
	}
	if ev.SidecarID != "" {
		p.SidecarID = ev.SidecarID
	}
	if ev.Duration > 0 {
		p.DurationMS = ev.Duration.Milliseconds()
	}
	p.Error = ev.Error
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

// friendlyReviewError phrases the pass-level failures a person can act on. The
// credential is never named or echoed.
func friendlyReviewError(err error) string {
	switch {
	case errors.Is(err, review.ErrClaudeMissing):
		return "Claude Code is not installed on the sandboxes — install it in the sandbox image and set validation.sidecarImage"
	case errors.Is(err, review.ErrCredentialRejected):
		return "Anthropic rejected the chunk daemon's Claude credential — replace it and restart the daemon"
	}
	return err.Error()
}

// settleSession closes a session that has ended, however it ended.
func (d *daemon) settleSession(entry *sessionEntry, runErr error) {
	entry.mu.Lock()
	defer entry.mu.Unlock()

	now := time.Now()
	entry.s.EndedAt = &now
	stage := entry.stageLocked(chunkd.StageFactoryLoop)
	switch {
	case entry.cancelled:
		entry.s.State = chunkd.SessionCancelled
		stage.State, stage.Note = chunkd.StageFailed, "cancelled"
	case runErr != nil:
		entry.s.State = chunkd.SessionFailed
		entry.s.Error = friendlyReviewError(runErr)
		stage.State, stage.Note = chunkd.StageFailed, entry.s.Error
	default:
		entry.s.State = chunkd.SessionDone
		stage.State, stage.Note = chunkd.StageDone, entry.loopNote
		if entry.loopFailed {
			stage.State = chunkd.StageFailed
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
	for ri := range entry.s.Rounds {
		r := &entry.s.Rounds[ri]
		if r.State == chunkd.RoundDone || r.State == chunkd.RoundFailed {
			continue
		}
		r.State, r.EndedAt = chunkd.RoundFailed, &now
		if r.Note == "" {
			r.Note = reason
		}
		for i := range r.Reviews {
			p := &r.Reviews[i]
			switch {
			case entry.cancelled && p.State != chunkd.PromptDone:
				// Whatever a stopped review reported ("context canceled" from
				// three layers down) is just the cancellation again.
				p.State, p.Error = chunkd.PromptFailed, reason
			case p.State == chunkd.PromptQueued || p.State == chunkd.PromptRunning:
				p.State, p.Error = chunkd.PromptFailed, reason
			}
		}
	}
}
