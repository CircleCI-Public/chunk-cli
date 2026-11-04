package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/CircleCI-Public/chunk-cli/internal/anthropic"
	"github.com/CircleCI-Public/chunk-cli/internal/bootstrap"
	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitremote"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// msgNoChecks is a Setup run's summary when the project's config has nothing
// for factory to check a plan's build with, so no plan can start.
const msgNoChecks = "Add a validation command or a review prompt"

// msgRetrying starts a new attempt's lines in a run's feed.
const msgRetrying = "Retrying setup"

// errSetupsStopped is a start refused once the daemon is shutting down.
var errSetupsStopped = &apiError{status: http.StatusServiceUnavailable, msg: "the chunk daemon is shutting down"}

// setups holds each project's Setup run, keyed by project root. A run lives
// as long as the daemon: a watch that quits leaves it running, and one that
// opens again reads its progress.
type setups struct {
	ctx    context.Context
	cancel context.CancelFunc
	// run does an attempt: bootstrapSetup unless a test sets it.
	run func(ctx context.Context, r bootstrap.Run) (bootstrap.Result, error)

	mu      sync.Mutex
	byRoot  map[string]*setupRun
	stopped bool
	wg      sync.WaitGroup
}

// newSetups makes the daemon's Setup runs, stopped with ctx or stopAll.
func newSetups(ctx context.Context) *setups {
	ctx, cancel := context.WithCancel(ctx)
	return &setups{ctx: ctx, cancel: cancel, run: bootstrapSetup, byRoot: make(map[string]*setupRun)}
}

// bootstrapSetup runs bootstrap.Setup, detecting validation commands with
// Claude when an Anthropic key is configured. Without a key, command
// detection uses its non-Claude fallback.
func bootstrapSetup(ctx context.Context, r bootstrap.Run) (bootstrap.Result, error) {
	if r.Anthropic == nil {
		rc, _ := config.Resolve("", "", false)
		r.Anthropic, _ = anthropic.New(anthropic.Config{APIKey: rc.AnthropicAPIKey, BaseURL: rc.AnthropicBaseURL})
	}
	return bootstrap.Setup(ctx, r)
}

// stopAll cancels every attempt and waits for them to finish, sidecar
// cleanup included. It is safe to call more than once.
func (s *setups) stopAll() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.stopped = true
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

// get is root's run, nil when the project has none.
func (s *setups) get(root string) *setupRun {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.byRoot[root]
}

// start starts root's run, or returns it unchanged when it is running, ok or
// continued. A stuck or failed run starts a new attempt in the same feed,
// which first deletes the sidecar the last attempt left.
func (s *setups) start(root, orgID string, client *circleci.Client) (*setupRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, errSetupsStopped
	}
	r, ok := s.byRoot[root]
	if !ok {
		r = &setupRun{root: root, changed: make(chan struct{})}
		s.byRoot[root] = r
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	retry := ok
	if retry && r.act.State != chunkd.SetupStuck && r.act.State != chunkd.SetupFailed {
		return r, nil
	}
	old := r.sidecar
	r.sidecar = ""
	if retry {
		addFeedLine(&r.feed, iostream.LevelStep, msgRetrying)
	}
	r.act = chunkd.SetupActivity{State: chunkd.SetupRunning, Step: string(bootstrap.StepConfig), Checks: -1}
	r.notifyLocked()

	ctx, cancel := context.WithCancel(s.ctx)
	s.wg.Go(func() {
		defer cancel()
		r.attempt(ctx, cancel, s.run, orgID, client, old)
	})
	return r, nil
}

// cont records Continue without an image on root's stuck run.
func (s *setups) cont(root string) error {
	r := s.get(root)
	if r == nil {
		return &apiError{status: http.StatusNotFound, msg: "no setup for this project"}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.act.State != chunkd.SetupStuck {
		return &apiError{status: http.StatusConflict, msg: fmt.Sprintf("setup is %s, not stuck: only a stuck setup can continue without an image", r.act.State)}
	}
	r.act.State = chunkd.SetupContinued
	addFeedLine(&r.feed, iostream.LevelInfo, "Continuing without a sidecar image: builds run on the default image")
	r.notifyLocked()
	return nil
}

// setupRun is one project's Setup run, across its attempts.
type setupRun struct {
	root string

	mu   sync.Mutex
	act  chunkd.SetupActivity // the run's state; its feed is kept in feed
	feed chunkd.Feed
	// sidecar is the attempt's sidecar ID, deleted by the next attempt.
	sidecar string
	// changed is closed and replaced on every state change.
	changed chan struct{}
}

// notifyLocked wakes the run's waiters. r.mu is held.
func (r *setupRun) notifyLocked() {
	close(r.changed)
	r.changed = make(chan struct{})
}

// wait blocks until the run is ok or continued (nil), failed (its error), or
// ctx is done (ctx.Err()); a stuck or running run keeps it waiting.
func (r *setupRun) wait(ctx context.Context) error {
	for {
		r.mu.Lock()
		state, summary, changed := r.act.State, r.act.Summary, r.changed
		r.mu.Unlock()
		switch state {
		case chunkd.SetupOK, chunkd.SetupContinued:
			return nil
		case chunkd.SetupFailed:
			return errors.New(summary)
		case chunkd.SetupRunning, chunkd.SetupStuck:
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// view is the run's state with its feed lines from after on; an after out of
// range reads from the first line kept.
func (r *setupRun) view(after int) chunkd.SetupActivity {
	r.mu.Lock()
	defer r.mu.Unlock()
	act := r.act
	kept := r.feed.Total - len(r.feed.Lines)
	if after < 0 || after > r.feed.Total {
		after = 0
	}
	from := max(after, kept)
	act.First = from
	act.Lines = append([]chunkd.FeedLine(nil), r.feed.Lines[from-kept:]...)
	act.Total = r.feed.Total
	return act
}

// line adds a line to the run's feed.
func (r *setupRun) line(level iostream.Level, text string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	addFeedLine(&r.feed, level, text)
}

// attempt does one attempt of the run and records how it ended. cancel stops
// it, which it does itself once the config step leaves nothing to check.
func (r *setupRun) attempt(ctx context.Context, cancel context.CancelFunc,
	run func(context.Context, bootstrap.Run) (bootstrap.Result, error),
	orgID string, client *circleci.Client, oldSidecar string,
) {
	if oldSidecar != "" {
		r.deleteSidecar(ctx, client, oldSidecar)
	}

	// check counts the checks once the config step is done: at the first
	// event or sidecar past it, or when the run returns. Provisioning may
	// report from more than one goroutine, hence the Once.
	var checkOnce sync.Once
	var noChecks atomic.Bool
	check := func() {
		checkOnce.Do(func() {
			n, ok := countChecks(r.root)
			if !ok {
				return
			}
			r.mu.Lock()
			r.act.Checks = n
			r.notifyLocked()
			r.mu.Unlock()
			if n == 0 {
				// No plan can start, so there is no point in a sidecar image.
				noChecks.Store(true)
				cancel()
			}
		})
	}

	out := &lineWriter{line: func(text string) {
		if strings.TrimSpace(text) != "" {
			r.line(iostream.LevelInfo, text)
		}
	}}
	res, err := run(ctx, bootstrap.Run{
		WorkDir: r.root,
		Repo:    repoName(ctx, r.root),
		OrgID:   orgID,
		Client:  client,
		Output:  out,
		OnEvent: func(ev bootstrap.Event) {
			if ev.Step != bootstrap.StepConfig {
				check()
			}
			if noChecks.Load() {
				return
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if r.act.Step != string(ev.Step) {
				r.act.Step = string(ev.Step)
				r.notifyLocked()
			}
			addFeedLine(&r.feed, ev.Level, ev.Text)
		},
		OnSidecar: func(id string) {
			check()
			r.mu.Lock()
			defer r.mu.Unlock()
			r.sidecar = id
		},
	})
	out.flush()
	if se, ok := errors.AsType[*bootstrap.StepError](err); !noChecks.Load() && (err == nil || ok && se.Step != bootstrap.StepConfig) {
		// The config step is done: count its checks if no event did.
		check()
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	defer r.notifyLocked()
	switch {
	case noChecks.Load():
		r.act.State = chunkd.SetupFailed
		r.act.Summary = msgNoChecks
		addFeedLine(&r.feed, iostream.LevelError, msgNoChecks)
		return
	case err == nil:
		r.act.State = chunkd.SetupOK
		r.act.Image = res.Image
		if r.act.Image == "" {
			r.act.Image = recordedImage(r.root)
		}
		return
	}
	r.act.Summary = err.Error()
	addFeedLine(&r.feed, iostream.LevelError, err.Error())
	se, ok := errors.AsType[*bootstrap.StepError](err)
	if ok && (se.Step == bootstrap.StepEnvironment || se.Step == bootstrap.StepSnapshot) && ctx.Err() == nil {
		r.act.State = chunkd.SetupStuck
		return
	}
	r.act.State = chunkd.SetupFailed
}

// deleteSidecar deletes the sidecar a failed attempt left, so Retry starts on
// a fresh one. One already gone is fine: the attempt deletes its sidecar
// when it ends, and this is for when that did not happen.
func (r *setupRun) deleteSidecar(ctx context.Context, client *circleci.Client, id string) {
	if client == nil {
		return
	}
	err := client.DeleteSidecar(ctx, id)
	if se, ok := errors.AsType[*circleci.StatusError](err); ok && se.StatusCode == http.StatusNotFound {
		return
	}
	if err != nil {
		r.line(iostream.LevelWarn, fmt.Sprintf("Could not delete sidecar %s: %v. Delete it with 'chunk sidecar delete', or wait for it to expire.", id, err))
	}
}

// countChecks is how many validation commands and review prompts root's
// config has, as factory counts them for a plan's build; ok is false when the
// config cannot be read.
func countChecks(root string) (n int, ok bool) {
	cfg, err := config.LoadProjectConfig(root)
	if err != nil {
		return 0, false
	}
	prompts, commands, err := factory.LoadChecks(filepath.Join(root, review.DefaultDir), true, cfg, false)
	switch {
	case errors.Is(err, factory.ErrNothingToCheck):
		return 0, true
	case err != nil:
		// The prompts could not be read; the commands still count.
		return len(factory.ValidationCommands(cfg.Commands)), true
	}
	return len(prompts) + len(commands), true
}

// recordedImage is root's validation.sidecarImage, "" when it has none: a run
// with no sidecar part to do reports the image already recorded.
func recordedImage(root string) string {
	cfg, err := config.LoadProjectConfig(root)
	if err != nil || !cfg.HasSidecarImage() {
		return ""
	}
	return cfg.Validation.SidecarImage
}

// repoName is the repository name the run's sidecar and snapshot are named
// for: the origin remote's, or root's base name.
func repoName(ctx context.Context, root string) string {
	if repo, err := gitremote.RepoName(ctx, root); err == nil && repo != "" {
		return repo
	}
	return filepath.Base(root)
}

// lineWriter hands each complete line written to it to line.
type lineWriter struct {
	mu   sync.Mutex
	buf  []byte
	line func(string)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.line(string(bytes.TrimRight(w.buf[:i], "\r")))
		w.buf = w.buf[i+1:]
	}
	return len(p), nil
}

// flush hands on what is left of a line with no newline.
func (w *lineWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(w.buf) > 0 {
		w.line(string(w.buf))
		w.buf = nil
	}
}
