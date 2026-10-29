package server

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// PlanConfig is what the daemon builds factory plans with. Every field is a
// test seam: left nil, the daemon uses the real thing.
type PlanConfig struct {
	// PlanAPI reads the factory plans a plan build builds; the daemon's
	// CircleCI client when nil.
	PlanAPI PlanAPI
	// OpenPlanWorktree opens a plan's worktree for its build;
	// factory.OpenPlanWorktree when nil.
	OpenPlanWorktree func(ctx context.Context, root, planID string) (string, error)
	// StartBuild starts a plan's build session; factory.StartAgent when nil.
	StartBuild func(ctx context.Context, cfg factory.AgentConfig, message string) (BuildAgent, error)
}

// Option customizes RunDaemon.
type Option func(*daemonOptions)

type daemonOptions struct {
	plans PlanConfig
}

// WithPlans configures how the daemon builds factory plans.
func WithPlans(cfg PlanConfig) Option {
	return func(o *daemonOptions) { o.plans = cfg }
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
		entry.s.Error = runErr.Error()
		stage.State, stage.Note = chunkd.StageFailed, entry.s.Error
	default:
		entry.s.State = chunkd.SessionDone
		stage.State, stage.Note = chunkd.StageDone, entry.loopNote
	}
}
