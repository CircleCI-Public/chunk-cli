package cmd

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/gitutil"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/ui"
)

// reviewActivity files a review pass in the project event log. Readers match
// activity to a sidecar row on SidecarID alone, so each sidecar gets its own
// recorder: a pass tagged with one representative ID would show every prompt
// on one row and leave the rest of the pool blank.
type reviewActivity struct {
	log         *eventlog.Log
	projectRoot string
	branch      string

	mu        sync.Mutex
	recorders map[string]*eventlog.Recorder
	// inFlight is the prompt each sidecar is reviewing, so a pass that dies
	// before reporting an outcome can still close the runs it opened.
	inFlight map[string]string
}

// newReviewActivity prepares the event log for a pass rooted at workDir. It
// returns nil when the log is unavailable, which every method tolerates:
// recording is best-effort and must never fail a review.
func newReviewActivity(ctx context.Context, workDir string) *reviewActivity {
	// The git root, not workDir, because sidecar state and the daemon's project
	// breadcrumb key on it. A .chunk below the git root would otherwise split a
	// project's state across two data directories.
	root := workDir
	if gitRoot := gitutil.TopLevelCtx(ctx, workDir); gitRoot != "" {
		root = gitRoot
	}
	dataDir, err := config.ProjectDataDir(root)
	if err != nil {
		return nil
	}
	log, err := eventlog.Open(dataDir)
	if err != nil {
		return nil
	}
	// Registers the project so the daemon finds it, which matters for a repo
	// where review runs before validate ever has.
	_ = sidecar.RegisterProjectRoot(dataDir, root)
	return &reviewActivity{
		log:         log,
		projectRoot: root,
		branch:      sidecar.CurrentBranch(root),
		recorders:   map[string]*eventlog.Recorder{},
		inFlight:    map[string]string{},
	}
}

// progress records one prompt state change. It is called from every review
// goroutine at once.
func (a *reviewActivity) progress(e review.ProgressEvent) {
	if a == nil || e.SidecarID == "" {
		return
	}
	rec := a.recorder(e.SidecarID)
	switch e.State {
	case review.StateQueued:
		// Nothing to file: a queued prompt has no sidecar to file it against.
	case review.StateRunning:
		a.open(e.SidecarID, e.Prompt)
		rec.Status(iostream.LevelStep, "$ claude -p "+e.Prompt)
	case review.StateDone:
		a.settle(e.SidecarID)
		rec.Final(iostream.LevelDone, fmt.Sprintf("%s reviewed in %s", e.Prompt, ui.FormatDuration(e.Duration)), 1, 1)
	case review.StateFailed:
		a.settle(e.SidecarID)
		closeFailed(rec,
			fmt.Sprintf("%s failed: %s", e.Prompt, e.Error),
			fmt.Sprintf("%s failed after %s", e.Prompt, ui.FormatDuration(e.Duration)))
	}
}

// closeFailed files why a run went wrong and then closes it. The reason rides
// on an ordinary error event because the dashboard shows a closing event only
// as the tally in its header, so a reason written there is never displayed.
func closeFailed(rec *eventlog.Recorder, reason, closing string) {
	rec.Status(iostream.LevelError, reason)
	rec.Final(iostream.LevelError, closing, 0, 1)
}

// submitted registers the remote command so its output can be replayed from
// the dashboard. The registration has to carry the sidecar that ran it: output
// is matched to a run by sidecar and submission time, not by command ID.
func (a *reviewActivity) submitted(entry *sidecar.PoolEntry, prompt, commandID string) {
	if a == nil {
		return
	}
	chunkd.RegisterCommand(chunkd.CommandReg{
		CommandID:   commandID,
		SidecarID:   entry.ID,
		ProjectRoot: a.projectRoot,
		Op:          string(eventlog.OpReview),
		Name:        clampLabel(prompt),
		SubmittedAt: time.Now(),
	})
}

// finish closes any run still open, which is how a pass stopped by a fatal
// error avoids leaving a sidecar reading as busy until the daemon times it out.
func (a *reviewActivity) finish(err error) {
	if a == nil || err == nil {
		return
	}
	a.mu.Lock()
	stranded := a.inFlight
	a.inFlight = map[string]string{}
	a.mu.Unlock()

	for id, prompt := range stranded {
		closeFailed(a.recorder(id),
			fmt.Sprintf("%s stopped: %s", prompt, err),
			prompt+" stopped")
	}
}

// recorder returns the recorder for one sidecar, creating it on first use. All
// of them share one log, so its mutex serialises concurrent appends.
func (a *reviewActivity) recorder(sidecarID string) *eventlog.Recorder {
	a.mu.Lock()
	defer a.mu.Unlock()
	rec, ok := a.recorders[sidecarID]
	if !ok {
		// A nil inner reporter: the progress display and statusFn already own
		// the terminal, and recording must not write to it a second time.
		rec = a.log.Recorder(nil, eventlog.OpReview, sidecarID, "", a.branch)
		a.recorders[sidecarID] = rec
	}
	return rec
}

// open notes the prompt a sidecar has started, so finish can close it if the
// pass ends without an outcome.
func (a *reviewActivity) open(sidecarID, prompt string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inFlight[sidecarID] = prompt
}

// settle notes that a sidecar's prompt reported its own outcome.
func (a *reviewActivity) settle(sidecarID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.inFlight, sidecarID)
}
