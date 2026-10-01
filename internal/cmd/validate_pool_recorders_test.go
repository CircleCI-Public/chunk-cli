package cmd

import (
	"fmt"
	"sync"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func poolEvents(t *testing.T, root string) []eventlog.Event {
	t.Helper()
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)
	log, err := eventlog.Open(dataDir)
	assert.NilError(t, err)
	events, err := log.Recent(100)
	assert.NilError(t, err)
	return events
}

func TestPoolRecordersFileEachCommandUnderItsSidecar(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)

	run := eventlog.Record(dataDir, nil, eventlog.OpValidate, "sb-1", "", "main")
	recorders := newPoolRecorders(run, true)

	// Two commands, one per sidecar, as RunDistributed would report them.
	one := recorders.status("sb-1")
	two := recorders.status("sb-2")
	one(iostream.LevelStep, "$ go test ./...")
	two(iostream.LevelStep, "$ golangci-lint run")
	two(iostream.LevelDone, "lint  3.1s")
	one(iostream.LevelError, "test  18.4s")

	byID := map[string][]eventlog.Event{}
	for _, e := range poolEvents(t, root) {
		byID[e.SidecarID] = append(byID[e.SidecarID], e)
	}
	// The representative ID must not collect the whole run.
	assert.Equal(t, len(byID["sb-1"]), 2)
	assert.Equal(t, len(byID["sb-2"]), 2)

	passed, total, ok := byID["sb-2"][1].Outcome()
	assert.Assert(t, ok, "a command has to close its own run")
	assert.Equal(t, passed, 1)
	assert.Equal(t, total, 1)

	passed, _, ok = byID["sb-1"][1].Outcome()
	assert.Assert(t, ok)
	assert.Equal(t, passed, 0)
}

func TestPoolRecordersReuseOneRecorderPerSidecar(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	dataDir, err := config.ProjectDataDir(t.TempDir())
	assert.NilError(t, err)

	recorders := newPoolRecorders(eventlog.Record(dataDir, nil, eventlog.OpValidate, "sb-1", "", "main"), true)
	first := recorders.status("sb-1")
	assert.Equal(t, len(recorders.byID), 1)
	recorders.status("sb-1")
	assert.Equal(t, len(recorders.byID), 1, "a worker asks once per run, but must not mint a recorder each time")
	assert.Assert(t, first != nil)
}

// A pooled command's closing event is written by its sidecar's own recorder, so
// that is where its ID has to be set. Set on the run's recorder it stamps
// nothing, and waits there for the next local command to finish.
func TestPoolRecordersStampCommandIDOnTheSidecarThatRanIt(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)

	run := eventlog.Record(dataDir, nil, eventlog.OpValidate, "sb-1", "", "main")
	recorders := newPoolRecorders(run, true)

	// Two commands in flight at once, each with its own ID.
	recorders.commandIDSetter("sb-1")("cmd-1")
	recorders.commandIDSetter("sb-2")("cmd-2")
	recorders.status("sb-2")(iostream.LevelDone, "lint  3.1s")
	recorders.status("sb-1")(iostream.LevelError, "test  18.4s")
	// A local command finishing afterwards, through the run's own recorder.
	run.Status(iostream.LevelDone, "fmt  0.1s")

	byID := map[string][]eventlog.Event{}
	for _, e := range poolEvents(t, root) {
		byID[e.SidecarID] = append(byID[e.SidecarID], e)
	}
	assert.Equal(t, byID["sb-2"][0].CommandID, "cmd-2")
	assert.Equal(t, byID["sb-1"][0].CommandID, "cmd-1")
	assert.Equal(t, byID["sb-1"][1].CommandID, "", "a local command inherited a pooled command's ID")
}

// Workers report concurrently, so the registry and the shared log both have to
// tolerate it.
func TestPoolRecordersRecordConcurrentWorkers(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)

	recorders := newPoolRecorders(eventlog.Record(dataDir, nil, eventlog.OpValidate, "sb-0", "", "main"), true)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("sb-%d", i)
			status := recorders.status(id)
			status(iostream.LevelStep, "$ "+id)
			status(iostream.LevelDone, id+"  1.0s")
		}(i)
	}
	wg.Wait()

	events := poolEvents(t, root)
	assert.Equal(t, len(events), 16, "a torn write would lose or corrupt a line")
}

// A run with no readable data directory records nothing, and must not be the
// reason a validation fails.
func TestNilPoolRecordersYieldNoStatus(t *testing.T) {
	recorders := newPoolRecorders(nil, true)
	assert.Assert(t, recorders == nil)
	assert.Assert(t, recorders.status("sb-1") == nil)
	assert.Assert(t, recorders.commandIDSetter("sb-1") == nil)
}

// A run on one sidecar closes once, on the run-wide summary. Closing each
// command too would show it as one run per command plus the summary.
func TestUnpooledRecordersLeaveOneRunToClose(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)

	run := eventlog.Record(dataDir, nil, eventlog.OpValidate, "sb-1", "", "main")
	recorders := newPoolRecorders(run, false)
	assert.Assert(t, recorders.status("sb-1") == nil, "an unpooled run reports through the run's own recorder")

	// Two commands as RunDistributed would report them, falling back to the
	// run's reporter, then the summary.
	recorders.commandIDSetter("sb-1")("cmd-1")
	run.Status(iostream.LevelDone, "lint  3.1s")
	run.Status(iostream.LevelDone, "test  18.4s")
	run.Final(iostream.LevelDone, "2/2 passed  21.5s", 2, 2)

	events := poolEvents(t, root)
	assert.Equal(t, len(events), 3)
	var closes int
	for _, e := range events {
		assert.Equal(t, e.SidecarID, "sb-1")
		if _, _, ok := e.Outcome(); ok {
			closes++
		}
	}
	assert.Equal(t, closes, 1)
	assert.Equal(t, events[0].CommandID, "cmd-1", "the ID belongs on the command's own event")
	assert.Equal(t, events[1].CommandID, "")
}

// A pooled run's summary tallies the whole pool, so it must not close a run on
// any one member's row.
func TestPooledRunSummaryIsFiledUnderNoSidecar(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)

	run := eventlog.Record(dataDir, nil, eventlog.OpValidate, runRecorderSidecarID(true, "sb-1"), "", "main")
	recorders := newPoolRecorders(run, true)
	recorders.status("sb-1")(iostream.LevelDone, "test  18.4s")
	recorders.status("sb-2")(iostream.LevelError, "lint  3.1s")
	run.Final(iostream.LevelError, "1/2 passed  18.5s", 1, 2)

	byID := map[string][]eventlog.Event{}
	for _, e := range poolEvents(t, root) {
		byID[e.SidecarID] = append(byID[e.SidecarID], e)
	}
	assert.Equal(t, len(byID["sb-1"]), 1, "the pool's summary landed on the representative's row")
	assert.Equal(t, byID["sb-1"][0].Level, "done")
	assert.Equal(t, len(byID[""]), 1)
	assert.Equal(t, runRecorderSidecarID(false, "sb-1"), "sb-1")
}
