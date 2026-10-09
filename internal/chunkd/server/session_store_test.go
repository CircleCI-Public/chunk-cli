package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

func TestSessionStoreAllowsOneActiveSessionPerProject(t *testing.T) {
	store := newSessionStore(t.Context())

	first, _, busy := store.add(chunkd.Session{ProjectRoot: "/p"})
	assert.Equal(t, busy, "")
	assert.Equal(t, len(first.snapshot().Stages), len(newStages()))

	_, _, busy = store.add(chunkd.Session{ProjectRoot: "/p"})
	assert.Assert(t, busy != "", "a second session on the same project must be refused")

	_, _, busy = store.add(chunkd.Session{ProjectRoot: "/other"})
	assert.Equal(t, busy, "", "another project is independent")

	finish(first, chunkd.SessionDone)
	_, _, busy = store.add(chunkd.Session{ProjectRoot: "/p"})
	assert.Equal(t, busy, "", "an ended session no longer blocks the project")
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
