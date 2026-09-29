package watchd

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// staticList is a listFn that always returns ids, counting its calls.
func staticList(calls *atomic.Int32, ids ...string) listFn {
	return func(context.Context, string) (map[string]bool, error) {
		calls.Add(1)
		out := make(map[string]bool, len(ids))
		for _, id := range ids {
			out[id] = true
		}
		return out, nil
	}
}

func ids(sidecars []SidecarState) []string {
	out := make([]string, 0, len(sidecars))
	for _, sc := range sidecars {
		out = append(out, sc.ID)
	}
	return out
}

func TestReconcileDropsSidecarsTheListOmits(t *testing.T) {
	var calls atomic.Int32
	l := newLivenessChecker(staticList(&calls, "live"))
	written := time.Now().Add(-time.Minute)
	sidecars := []SidecarState{
		{ID: "live", OrgID: "org", FileMtime: written},
		{ID: "gone", OrgID: "org", FileMtime: written},
	}

	// Before any list has landed there is nothing to judge against.
	before := l.reconcile(append([]SidecarState(nil), sidecars...))
	assert.DeepEqual(t, ids(before), []string{"live", "gone"})
	assert.Check(t, !before[0].Verified)

	l.fetch(context.Background(), "org")
	after := l.reconcile(append([]SidecarState(nil), sidecars...))
	assert.DeepEqual(t, ids(after), []string{"live"})
	assert.Check(t, after[0].Verified)
}

func TestReconcileKeepsStateNewerThanTheList(t *testing.T) {
	var calls atomic.Int32
	l := newLivenessChecker(staticList(&calls))
	l.fetch(context.Background(), "org")

	// Written after the list was fetched: the sidecar may have been created
	// since, so its absence proves nothing yet.
	fresh := SidecarState{ID: "new", OrgID: "org", FileMtime: time.Now().Add(time.Second)}
	got := l.reconcile([]SidecarState{fresh})
	assert.DeepEqual(t, ids(got), []string{"new"})
	assert.Check(t, !got[0].Verified)
}

func TestReconcileKeepsSidecarsWithoutAnOrg(t *testing.T) {
	var calls atomic.Int32
	l := newLivenessChecker(staticList(&calls))
	l.fetch(context.Background(), "org")

	got := l.reconcile([]SidecarState{{ID: "orphan", FileMtime: time.Now().Add(-time.Hour)}})
	assert.DeepEqual(t, ids(got), []string{"orphan"})
}

func TestReconcileNilCheckerKeepsEverything(t *testing.T) {
	var l *livenessChecker
	l.maybeRefresh(context.Background(), []SidecarState{{ID: "a", OrgID: "org"}})
	got := l.reconcile([]SidecarState{{ID: "a", OrgID: "org"}})
	assert.DeepEqual(t, ids(got), []string{"a"})
}

func TestFetchFailureKeepsThePreviousList(t *testing.T) {
	fail := false
	l := newLivenessChecker(func(context.Context, string) (map[string]bool, error) {
		if fail {
			return nil, errors.New("boom")
		}
		return map[string]bool{"live": true}, nil
	})
	l.fetch(context.Background(), "org")
	fail = true
	l.fetch(context.Background(), "org")

	written := time.Now().Add(-time.Hour)
	got := l.reconcile([]SidecarState{
		{ID: "live", OrgID: "org", FileMtime: written},
		{ID: "gone", OrgID: "org", FileMtime: written},
	})
	assert.DeepEqual(t, ids(got), []string{"live"})
	assert.Check(t, got[0].Verified)
}

func TestMaybeRefreshThrottlesPerOrg(t *testing.T) {
	var calls atomic.Int32
	l := newLivenessChecker(staticList(&calls))
	sidecars := []SidecarState{{ID: "a", OrgID: "org", FileMtime: time.Now().Add(-time.Hour)}}

	l.maybeRefresh(context.Background(), sidecars)
	waitIdle(t, l)
	l.maybeRefresh(context.Background(), sidecars)
	waitIdle(t, l)
	assert.Check(t, cmp.Equal(calls.Load(), int32(1)), "second poll inside the interval must not refetch")

	// A state file written after the last fetch is a sidecar the list may not
	// know about yet, so it is fetched again without waiting out the interval.
	sidecars = append(sidecars, SidecarState{ID: "b", OrgID: "org", FileMtime: time.Now().Add(time.Second)})
	l.maybeRefresh(context.Background(), sidecars)
	waitIdle(t, l)
	assert.Check(t, cmp.Equal(calls.Load(), int32(2)))
}

func TestListForUsesTheSidecarAPI(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "mine", OrgID: "org"},
		{ID: "elsewhere", OrgID: "other-org"},
	}
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	got, err := listFor(newTestClient(t, srv.URL))(context.Background(), "org")
	assert.NilError(t, err)
	assert.DeepEqual(t, got, map[string]bool{"mine": true})

	cci.ListStatusCode = http.StatusInternalServerError
	_, err = listFor(newTestClient(t, srv.URL))(context.Background(), "org")
	assert.Check(t, err != nil)
}

func TestListForWithoutClientIsNil(t *testing.T) {
	assert.Check(t, listFor(nil) == nil)
}

// waitIdle blocks until no fetch is in flight.
func waitIdle(t *testing.T, l *livenessChecker) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		l.mu.Lock()
		n := len(l.inflight)
		l.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("fetch still in flight")
}
