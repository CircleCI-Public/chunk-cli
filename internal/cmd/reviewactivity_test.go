package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// reviewEvents returns everything the activity wrote for one project root.
func reviewEvents(t *testing.T, root string) []eventlog.Event {
	t.Helper()
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)
	log, err := eventlog.Open(dataDir)
	assert.NilError(t, err)
	events, err := log.Recent(100)
	assert.NilError(t, err)
	return events
}

// eventsFor filters as the dashboard does, on sidecar ID alone.
func eventsFor(events []eventlog.Event, sidecarID string) []eventlog.Event {
	var out []eventlog.Event
	for _, e := range events {
		if e.SidecarID == sidecarID {
			out = append(out, e)
		}
	}
	return out
}

func TestReviewActivityAttributesEachPromptToItsSidecar(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()

	activity := newReviewActivity(context.Background(), root)
	assert.Assert(t, activity != nil)

	// Two prompts on two sidecars, interleaved as concurrent reviews arrive.
	activity.progress(review.ProgressEvent{Prompt: "naming", State: review.StateQueued})
	activity.progress(review.ProgressEvent{Prompt: "errors", State: review.StateQueued})
	activity.progress(review.ProgressEvent{Prompt: "naming", SidecarID: "sb-1", State: review.StateRunning})
	activity.progress(review.ProgressEvent{Prompt: "errors", SidecarID: "sb-2", State: review.StateRunning})
	activity.progress(review.ProgressEvent{Prompt: "errors", SidecarID: "sb-2", State: review.StateFailed, Duration: 3 * time.Second, Error: "boom"})
	activity.progress(review.ProgressEvent{Prompt: "naming", SidecarID: "sb-1", State: review.StateDone, Duration: 12 * time.Second})

	events := reviewEvents(t, root)
	// A queued prompt has no sidecar yet, so recording it would file activity
	// the dashboard can never show against a row.
	assert.Equal(t, len(events), 5)

	passed := eventsFor(events, "sb-1")
	assert.Equal(t, len(passed), 2)
	assert.Equal(t, passed[0].Op, eventlog.OpReview)
	assert.Equal(t, passed[0].Msg, "$ claude -p naming")
	assert.Assert(t, !passed[0].Final, "the opening event must leave the run open")
	assert.Equal(t, passed[1].Msg, "naming reviewed in 12.0s")
	p, total, ok := passed[1].Outcome()
	assert.Assert(t, ok, "the closing event must report an outcome or the run never collapses")
	assert.Equal(t, p, 1)
	assert.Equal(t, total, 1)

	failed := eventsFor(events, "sb-2")
	assert.Equal(t, len(failed), 3)
	// The dashboard shows the closing event only as the tally in a header, so
	// the reason has to be on an ordinary event ahead of it.
	assert.Equal(t, failed[1].Msg, "errors failed: boom")
	assert.Equal(t, failed[1].Level, "error")
	assert.Assert(t, !failed[1].Final, "the reason must not close the run on its own")
	assert.Equal(t, failed[2].Msg, "errors failed after 3.0s")
	assert.Equal(t, failed[2].Level, "error")
	p, total, ok = failed[2].Outcome()
	assert.Assert(t, ok)
	assert.Equal(t, p, 0)
	assert.Equal(t, total, 1)
}

func TestReviewActivityFinishClosesAbandonedRuns(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()

	activity := newReviewActivity(context.Background(), root)
	assert.Assert(t, activity != nil)

	activity.progress(review.ProgressEvent{Prompt: "naming", SidecarID: "sb-1", State: review.StateRunning})
	// A fatal error stops the pass without reporting a per-prompt outcome.
	activity.finish(errors.New("claude is not installed"))

	events := eventsFor(reviewEvents(t, root), "sb-1")
	assert.Equal(t, len(events), 3)
	assert.Equal(t, events[1].Msg, "naming stopped: claude is not installed")
	assert.Assert(t, !events[1].Final)
	_, _, ok := events[2].Outcome()
	assert.Assert(t, ok, "an abandoned run must still close or the sidecar reads as busy")

	// A second finish has nothing left to close.
	activity.finish(errors.New("again"))
	assert.Equal(t, len(eventsFor(reviewEvents(t, root), "sb-1")), 3)
}

func TestReviewActivityFinishIgnoresSuccess(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()

	activity := newReviewActivity(context.Background(), root)
	activity.progress(review.ProgressEvent{Prompt: "naming", SidecarID: "sb-1", State: review.StateRunning})
	activity.progress(review.ProgressEvent{Prompt: "naming", SidecarID: "sb-1", State: review.StateDone, Duration: time.Second})
	activity.finish(nil)

	assert.Equal(t, len(eventsFor(reviewEvents(t, root), "sb-1")), 2)
}

func TestReviewActivityRegistersCommandForOutputReplay(t *testing.T) {
	regs := captureRegistrations(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()

	activity := newReviewActivity(context.Background(), root)
	activity.submitted(&sidecar.PoolEntry{ID: "sb-2"}, "naming", "cmd-7")

	select {
	case reg := <-regs:
		assert.Equal(t, reg.CommandID, "cmd-7")
		assert.Equal(t, reg.SidecarID, "sb-2")
		assert.Equal(t, reg.Op, string(eventlog.OpReview))
		assert.Equal(t, reg.Name, "naming")
		// The daemon buckets commands by project root; a root the events do not
		// share leaves the output pane unreachable from the run.
		assert.Equal(t, config.CanonicalProjectRoot(reg.ProjectRoot), config.CanonicalProjectRoot(root))
		assert.Assert(t, !reg.SubmittedAt.IsZero())
	case <-time.After(5 * time.Second):
		t.Fatal("review command was not registered")
	}
}

// Sidecar state and the daemon's project breadcrumb key on the git root, so a
// pass started below it has to file its events there, not under the subdirectory.
func TestReviewActivityFilesUnderTheGitRoot(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	repo := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	sub := filepath.Join(repo, "pkg", "deep")
	assert.NilError(t, os.MkdirAll(sub, 0o755))

	activity := newReviewActivity(context.Background(), sub)
	assert.Assert(t, activity != nil)
	assert.Equal(t, config.CanonicalProjectRoot(activity.projectRoot), config.CanonicalProjectRoot(repo))

	activity.progress(review.ProgressEvent{Prompt: "naming", SidecarID: "sb-1", State: review.StateRunning})

	assert.Equal(t, len(eventsFor(reviewEvents(t, repo), "sb-1")), 1)
	dataDir, err := config.ProjectDataDir(repo)
	assert.NilError(t, err)
	_, err = os.Stat(sidecar.ProjectRootPath(dataDir))
	assert.NilError(t, err, "the daemon finds a project through its breadcrumb")
}

// Reviews run concurrently, so every recorder shares one log and its mutex.
func TestReviewActivityRecordsConcurrentReviews(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	// submitted registers with the watch daemon; without this the registrations
	// would reach whichever one the developer running the tests has open.
	t.Setenv("CHUNK_WATCHD_DIR", t.TempDir())
	root := t.TempDir()

	activity := newReviewActivity(context.Background(), root)
	assert.Assert(t, activity != nil)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("sb-%d", i)
			activity.progress(review.ProgressEvent{Prompt: id, SidecarID: id, State: review.StateRunning})
			activity.submitted(&sidecar.PoolEntry{ID: id}, id, "cmd-"+id)
			activity.progress(review.ProgressEvent{Prompt: id, SidecarID: id, State: review.StateDone, Duration: time.Second})
		}(i)
	}
	wg.Wait()

	events := reviewEvents(t, root)
	assert.Equal(t, len(events), 16, "a torn write would lose or corrupt a line")
	for i := range 8 {
		assert.Equal(t, len(eventsFor(events, fmt.Sprintf("sb-%d", i))), 2)
	}
}

// A nil activity is what an unavailable event log degrades to, and recording
// must never be the reason a review fails.
func TestNilReviewActivityRecordsNothing(t *testing.T) {
	var activity *reviewActivity
	activity.progress(review.ProgressEvent{Prompt: "naming", SidecarID: "sb-1", State: review.StateRunning})
	activity.submitted(&sidecar.PoolEntry{ID: "sb-1"}, "naming", "cmd-1")
	activity.finish(errors.New("boom"))
}
