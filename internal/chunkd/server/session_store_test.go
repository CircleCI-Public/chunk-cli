package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
)

// finish settles an entry the way a session run would, so the store treats it
// as ended.
func finish(e *sessionEntry, state chunkd.SessionState) {
	e.mu.Lock()
	e.s.State = state
	e.mu.Unlock()
	close(e.done)
}

func TestSessionStoreAllowsSessionsSideBySide(t *testing.T) {
	store := newSessionStore(t.Context())

	first, _, busy := store.add(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{}})
	assert.Equal(t, busy, "")
	assert.Equal(t, len(first.snapshot().Stages), len(newStages()))

	_, _, busy = store.add(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{}})
	assert.Equal(t, busy, "", "a second run in the same project has its own worktree")
	assert.Equal(t, len(store.forProject("/p")), 2)
}

func TestSessionStoreRefusesToContinueARunInUse(t *testing.T) {
	store := newSessionStore(t.Context())
	const branch = "chunk/factory/add-a-flag/run-1"

	running, _, busy := store.add(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{RunID: "run-1", Branch: branch}})
	assert.Equal(t, busy, "")

	_, _, busy = store.addOn(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{ContinuesRunID: "run-1"}}, branch)
	assert.Assert(t, strings.Contains(busy, "still working on"), busy)

	finish(running, chunkd.SessionDone)
	continuing, _, busy := store.addOn(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{ContinuesRunID: "run-1"}}, branch)
	assert.Equal(t, busy, "", "an ended run can be continued")

	_, _, busy = store.addOn(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{ContinuesRunID: "run-1"}}, branch)
	assert.Assert(t, strings.Contains(busy, "already starting on"), busy)

	_, _, busy = store.addOn(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{ContinuesRunID: "run-9"}}, "chunk/factory/fix-the-typo/run-9")
	assert.Equal(t, busy, "", "continuing a run on another branch is independent")

	finish(continuing, chunkd.SessionDone)
	_, _, busy = store.addOn(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{ContinuesRunID: "run-1"}}, branch)
	assert.Equal(t, busy, "", "an ended continuation no longer holds the branch")
}

// Every run of a chain of continuations works in the first run's worktree, so
// continuing a later run of the chain is refused while another is going, though
// the two continue different runs.
func TestSessionStoreRefusesToContinueAChainInUse(t *testing.T) {
	store := newSessionStore(t.Context())
	const branch = "chunk/factory/add-a-flag/run-1"

	// run-2 continued run-1 and ended; run-3 continues run-1 again.
	second, _, busy := store.addOn(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{ContinuesRunID: "run-1"}}, branch)
	assert.Equal(t, busy, "")
	finish(second, chunkd.SessionDone)
	third, _, busy := store.addOn(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{ContinuesRunID: "run-1"}}, branch)
	assert.Equal(t, busy, "")

	_, _, busy = store.addOn(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{ContinuesRunID: "run-2"}}, branch)
	assert.Assert(t, busy != "", "two runs went in one worktree")

	finish(third, chunkd.SessionDone)
	_, _, busy = store.addOn(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{ContinuesRunID: "run-2"}}, branch)
	assert.Equal(t, busy, "")
}

func TestSessionStoreKeepsTheNewestFinishedSessionsNewestFirst(t *testing.T) {
	store := newSessionStore(t.Context())

	var ids []string
	for range MaxSessionsPerProject + 3 {
		e, _, busy := store.add(chunkd.Session{ProjectRoot: "/p"})
		assert.Equal(t, busy, "")
		ids = append(ids, e.snapshot().ID)
		finish(e, chunkd.SessionDone)
	}

	got := store.forProject("/p")
	assert.Equal(t, len(got), MaxSessionsPerProject)
	assert.Equal(t, got[0].ID, ids[len(ids)-1], "newest first")
	assert.Assert(t, store.get(ids[0]) == nil, "the oldest finished session is dropped")
}

func TestSessionAPIListsGetsAndCancels(t *testing.T) {
	d := newTestDaemon()
	mux := http.NewServeMux()
	registerSessionRoutes(mux, d)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	entry, ctx, busy := d.sessions.add(chunkd.Session{ProjectRoot: "/p"})
	assert.Equal(t, busy, "")
	id := entry.snapshot().ID
	// Cancelling cancels the entry's context; settle it as a run would.
	go func() {
		<-ctx.Done()
		finish(entry, chunkd.SessionCancelled)
	}()

	resp, err := http.Get(srv.URL + "/factory/" + id)
	assert.NilError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, resp.StatusCode, http.StatusOK)
	var detail chunkd.SessionDetail
	assert.NilError(t, json.NewDecoder(resp.Body).Decode(&detail))
	assert.Equal(t, detail.ID, id)

	missing, err := http.Get(srv.URL + "/factory/nope")
	assert.NilError(t, err)
	defer func() { _ = missing.Body.Close() }()
	assert.Equal(t, missing.StatusCode, http.StatusNotFound)

	for range 2 { // cancelling twice is not an error
		cancel, err := http.Post(srv.URL+"/factory/"+id+"/cancel", "", nil)
		assert.NilError(t, err)
		_ = cancel.Body.Close()
		assert.Equal(t, cancel.StatusCode, http.StatusAccepted)
	}
	<-entry.done
	assert.Equal(t, entry.snapshot().State, chunkd.SessionCancelled)
}
