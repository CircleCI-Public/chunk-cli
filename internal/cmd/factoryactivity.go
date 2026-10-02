package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// toolInterval is the least time between the implementer tool uses filed in
// the event log. A turn can use hundreds, and the daemon keeps only the most
// recent events per project, so filing every one would push the reviews out
// of the dashboard. One every so often is still far more often than the
// daemon's running timeout, so a long turn keeps reading as running.
const toolInterval = 15 * time.Second

// factoryActivity files a factory run in the project event log, so the
// dashboard shows it against the pool's sidecars: reviews as chunk review files
// them, and the implementer's turns and validation commands on the
// implementer's row. Everything is tagged with the run's branch, where the work
// is, rather than the developer's.
type factoryActivity struct {
	reviews *reviewActivity
	implID  string
	turn    *eventlog.Recorder
	checks  *eventlog.Recorder

	turns    int
	lastTool time.Time
}

// newFactoryActivity prepares the event log for a run in the project at root,
// on branch, whose implementer runs on implID. root must be the developer's
// checkout, not the run's worktree: the pool's state is kept there, and the
// dashboard only matches activity to sidecars within one project. It returns
// nil when the log is unavailable, which every method tolerates.
func newFactoryActivity(ctx context.Context, root, branch, implID string) *factoryActivity {
	reviews := newBranchActivity(ctx, root, branch)
	if reviews == nil {
		return nil
	}
	return &factoryActivity{
		reviews: reviews,
		implID:  implID,
		turn:    reviews.log.Recorder(nil, eventlog.OpImplement, implID, "", reviews.branch),
		checks:  reviews.log.Recorder(nil, eventlog.OpValidate, implID, "", reviews.branch),
	}
}

// reviewProgress files a review's state change, as chunk review does.
func (a *factoryActivity) reviewProgress(e review.ProgressEvent) {
	if a == nil {
		return
	}
	a.reviews.progress(e)
}

// reviewSubmitted registers a review's remote command, as chunk review does.
func (a *factoryActivity) reviewSubmitted(entry *sidecar.PoolEntry, prompt, commandID string) {
	if a == nil {
		return
	}
	a.reviews.submitted(entry, prompt, commandID)
}

// implementing opens an implementer turn.
func (a *factoryActivity) implementing() {
	if a == nil {
		return
	}
	a.turns++
	a.lastTool = time.Time{}
	if a.turns == 1 {
		a.turn.Status(iostream.LevelStep, "$ claude -p (implementing the prompt)")
		return
	}
	a.turn.Status(iostream.LevelStep, fmt.Sprintf("$ claude -p (turn %d: fixing failed checks)", a.turns))
}

// toolUsed files a tool the implementer used, at most one per toolInterval.
func (a *factoryActivity) toolUsed(act factory.Activity) {
	if a == nil || act.Tool == "" || time.Since(a.lastTool) < toolInterval {
		return
	}
	a.lastTool = time.Now()
	a.turn.Status(iostream.LevelInfo, act.Tool+" "+oneLineSummary(act.Detail))
}

// implemented closes an implementer turn.
func (a *factoryActivity) implemented(turn factory.Turn, err error) {
	if a == nil {
		return
	}
	took := ui.FormatDuration(turn.Duration)
	if err != nil {
		closeFailed(a.turn,
			fmt.Sprintf("implementer failed: %s", err),
			"implementer failed after "+took)
		return
	}
	a.turn.Final(iostream.LevelDone, fmt.Sprintf("implemented in %s ($%.2f)", took, turn.CostUSD), 1, 1)
}

// commandSubmitted opens a validation command and registers it, so its output
// can be replayed from the dashboard.
func (a *factoryActivity) commandSubmitted(c config.Command, commandID string) {
	if a == nil {
		return
	}
	a.checks.SetCommandID(commandID)
	a.checks.Status(iostream.LevelStep, "$ "+c.Run)
	watchd.RegisterCommand(watchd.CommandReg{
		CommandID:   commandID,
		SidecarID:   a.implID,
		ProjectRoot: a.reviews.projectRoot,
		Op:          string(eventlog.OpValidate),
		Name:        clampLabel(c.Name),
		SubmittedAt: time.Now(),
	})
}

// checked closes a validation command. Each closes as a run of its own, as a
// pooled validate's commands do.
func (a *factoryActivity) checked(c factory.Check) {
	if a == nil {
		return
	}
	took := ui.FormatDuration(c.Duration)
	closeRun := a.checks.PerCommand()
	switch c.Status {
	case factory.StatusPassed:
		closeRun(iostream.LevelDone, fmt.Sprintf("%s passed in %s", c.Name, took))
	case factory.StatusFailed:
		closeRun(iostream.LevelError, fmt.Sprintf("%s failed in %s", c.Name, took))
	case factory.StatusErrored:
		a.checks.Status(iostream.LevelError, fmt.Sprintf("%s could not run: %s", c.Name, c.Error))
		closeRun(iostream.LevelError, c.Name+" could not run")
	}
}

// finish closes any review a run stopped by err left open.
func (a *factoryActivity) finish(err error) {
	if a == nil {
		return
	}
	a.reviews.finish(err)
}

// recordedSteps files each implementer turn as it runs. It wraps the steps
// rather than following the loop's events so a turn that fails still closes:
// the loop reports no event for it.
type recordedSteps struct {
	factory.Steps
	activity *factoryActivity
}

func (s recordedSteps) Implement(ctx context.Context, prompt string) (factory.Turn, error) {
	s.activity.implementing()
	turn, err := s.Steps.Implement(ctx, prompt)
	s.activity.implemented(turn, err)
	return turn, err
}
