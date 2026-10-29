package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

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

	first, _ := store.add(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{}})
	assert.Equal(t, len(first.snapshot().Stages), len(newStages()))

	store.add(chunkd.Session{ProjectRoot: "/p", Factory: &chunkd.FactoryRun{}})
	assert.Equal(t, len(store.forProject("/p")), 2)
}

func TestSessionStoreKeepsTheNewestFinishedSessionsNewestFirst(t *testing.T) {
	store := newSessionStore(t.Context())

	var ids []string
	for range MaxSessionsPerProject + 3 {
		e, _ := store.add(chunkd.Session{ProjectRoot: "/p"})
		ids = append(ids, e.snapshot().ID)
		finish(e, chunkd.SessionDone)
	}

	got := store.forProject("/p")
	assert.Equal(t, len(got), MaxSessionsPerProject)
	assert.Equal(t, got[0].ID, ids[len(ids)-1], "newest first")
	assert.Assert(t, store.get(ids[0]) == nil, "the oldest finished session is dropped")
}

// The session API serves plan builds only: the old factory loop's routes are
// gone, and a plan build can still be stopped and forgotten.
func TestFactoryRoutes(t *testing.T) {
	pb := newPlanBuildDaemon(t)
	sess, err := pb.startPlanBuild(chunkd.PlanBuildRequest{ProjectRoot: pb.root, PlanID: "plan-1"})
	assert.NilError(t, err)
	srv := httptest.NewServer(newServer(pb.daemon).Handler)
	t.Cleanup(srv.Close)

	do := func(method, path string) int {
		t.Helper()
		req, err := http.NewRequest(method, srv.URL+path, nil)
		assert.NilError(t, err)
		resp, err := http.DefaultClient.Do(req)
		assert.NilError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/factory"},
		{http.MethodGet, "/factory"},
		{http.MethodGet, "/factory/x"},
		{http.MethodGet, "/factory/" + sess.ID},
		{http.MethodPost, "/factory/warm"},
		{http.MethodPost, "/factory/warm/x/cancel"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			code := do(tc.method, tc.path)
			assert.Check(t, code == http.StatusNotFound || code == http.StatusMethodNotAllowed, "got %d", code)
		})
	}

	t.Run("cancel stops the build", func(t *testing.T) {
		code := do(http.MethodPost, "/factory/"+sess.ID+"/cancel")
		assert.Check(t, code >= 200 && code < 300, "got %d", code)
		got := waitForSessionEnd(t, pb.daemon, sess.ID)
		assert.Check(t, cmp.Equal(got.State, chunkd.SessionCancelled))
		assert.Check(t, pb.build.wasStopped(), "the build session was left running")
	})

	t.Run("delete forgets the build", func(t *testing.T) {
		code := do(http.MethodDelete, "/factory/"+sess.ID)
		assert.Check(t, code >= 200 && code < 300, "got %d", code)
		for _, p := range pb.snapshot(nil).Projects {
			for _, s := range p.Sessions {
				assert.Check(t, s.ID != sess.ID, "the deleted build is still in the snapshot")
			}
		}
	})
}
