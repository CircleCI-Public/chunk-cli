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
	recorders := newPoolRecorders(run)

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

	recorders := newPoolRecorders(eventlog.Record(dataDir, nil, eventlog.OpValidate, "sb-1", "", "main"))
	first := recorders.status("sb-1")
	assert.Equal(t, len(recorders.byID), 1)
	recorders.status("sb-1")
	assert.Equal(t, len(recorders.byID), 1, "a worker asks once per run, but must not mint a recorder each time")
	assert.Assert(t, first != nil)
}

// Workers report concurrently, so the registry and the shared log both have to
// tolerate it.
func TestPoolRecordersRecordConcurrentWorkers(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	root := t.TempDir()
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)

	recorders := newPoolRecorders(eventlog.Record(dataDir, nil, eventlog.OpValidate, "sb-0", "", "main"))
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
	recorders := newPoolRecorders(nil)
	assert.Assert(t, recorders == nil)
	assert.Assert(t, recorders.status("sb-1") == nil)
}
