package variants_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/variants"
)

const sidecarInstancesPath = "/api/v3/sidecar/instances"

func nopStatus(_ iostream.Level, _ string) {}

func newTestClient(t *testing.T, srv *httptest.Server) *circleci.Client {
	t.Helper()
	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	return client
}

func defaultOpts() variants.Options {
	return variants.Options{
		OrgID:     "org-aaa",
		Image:     "snap-abc",
		Workspace: "/home/user/repo",
		Commands:  []variants.Command{{Name: "test", Run: "go test ./..."}},
		Parallel:  5,
		StatusFn:  nopStatus,
	}
}

// variantName builds the sidecar name a variant created at ts would get,
// mirroring the timestamp scheme SweepOrphans dates names by. The double dash is
// the delimiter, which is why a sanitized ID can never contain one.
func variantName(ts time.Time, id string) string {
	return "variant-" + strconv.FormatInt(ts.Unix(), 36) + "--" + id
}

func TestRunEmpty(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	results, err := variants.Run(context.Background(), newTestClient(t, srv), nil, defaultOpts())
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(results, 0))
	assert.Check(t, cmp.Len(cci.Recorder.AllRequests(), 0))
}

func TestRunCreateError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.CreateStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	vs := []variants.Variant{
		{ID: "MUT-001", Description: "invert nil check", Patch: ""},
	}
	results, err := variants.Run(context.Background(), newTestClient(t, srv), vs, defaultOpts())
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(results, 1))
	assert.Equal(t, results[0].ID, "MUT-001")
	assert.Assert(t, results[0].Error != "", "expected error in result, got empty")
	assert.Check(t, !results[0].Killed)
}

func TestRunResultsInOrder(t *testing.T) {
	// All variants fail at create — fast path for ordering verification.
	cci := fakes.NewFakeCircleCI()
	cci.CreateStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	vs := []variants.Variant{
		{ID: "MUT-001"},
		{ID: "MUT-002"},
		{ID: "MUT-003"},
	}
	results, err := variants.Run(context.Background(), newTestClient(t, srv), vs, defaultOpts())
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(results, 3))
	assert.Equal(t, results[0].ID, "MUT-001")
	assert.Equal(t, results[1].ID, "MUT-002")
	assert.Equal(t, results[2].ID, "MUT-003")
}

// countSidecarCalls returns how many create and delete requests reached the API.
func countSidecarCalls(cci *fakes.FakeCircleCI) (creates, deletes int) {
	for _, r := range cci.Recorder.AllRequests() {
		switch {
		case r.URL.Path == sidecarInstancesPath && r.Method == http.MethodPost:
			creates++
		case strings.HasPrefix(r.URL.Path, sidecarInstancesPath+"/") && r.Method == http.MethodDelete:
			deletes++
		}
	}
	return creates, deletes
}

func TestRunDeleteCalledOnCreateSuccess(t *testing.T) {
	// Create succeeds; Sync fails at SSH key registration so delete must still run.
	// AddKeyStatusCode=500 prevents OpenSession from succeeding, so the variant
	// dies after the sidecar exists — the case where a missing delete leaks a
	// billed instance.
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	vs := []variants.Variant{
		{ID: "MUT-001", Patch: ""},
	}
	results, err := variants.Run(context.Background(), newTestClient(t, srv), vs, defaultOpts())
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(results, 1))
	assert.Assert(t, results[0].Error != "", "expected error (no SSH server)")

	creates, deletes := countSidecarCalls(cci)
	assert.Check(t, creates >= 1, "expected at least 1 create request")
	assert.Check(t, deletes >= 1, "expected at least 1 delete request")
}

// TestRunDeletesEverySidecarOnCancel is the regression guard for the leak an
// interrupt used to cause: the process died before any deferred delete ran and
// every in-flight sidecar kept billing. Cancellation is what the SIGINT handler
// installed by Run reduces to, so asserting on a cancelled context covers it
// without having to signal the test binary itself.
//
// The cancel has to land after a sidecar exists, which is why it fires from
// inside the create handler rather than before Run. Cancelling up front makes
// sidecar.Create fail on the dead context before any request is sent, leaving
// creates and deletes both at zero — an assertion that passes with the deferred
// delete removed entirely, and so guards nothing.
func TestRunDeletesEverySidecarOnCancel(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyStatusCode = 500

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var once sync.Once
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Cancel on the first request after the create, rather than on the create
		// itself. Cancelling during the create races the client's read of its own
		// response: the sidecar exists server-side, but CreateSidecar returns an
		// error, so runVariant returns before the deferred delete is registered and
		// there is no ID to delete by — only SweepOrphans can collect that one. The
		// add-key request happens only once create has returned, so by then the
		// sidecar is known and the defer is armed, which is the leak this test is
		// about.
		if strings.HasSuffix(r.URL.Path, "/ssh/add-key") {
			once.Do(cancel)
		}
		cci.ServeHTTP(w, r)
	}))
	defer srv.Close()

	vs := []variants.Variant{{ID: "MUT-001"}, {ID: "MUT-002"}, {ID: "MUT-003"}}
	opts := defaultOpts()
	opts.Parallel = 1

	results, err := variants.Run(ctx, newTestClient(t, srv), vs, opts)
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(results, 3))

	creates, deletes := countSidecarCalls(cci)
	assert.Assert(t, creates >= 1, "expected the first variant to create a sidecar before the cancel landed")
	assert.Equal(t, creates, deletes, "every created sidecar must be deleted")

	for _, r := range results {
		assert.Check(t, !r.Killed, "cancelled variant must not be reported as killed")
		assert.Check(t, r.Error != "", "cancelled variant must carry an error, not a silent pass")
	}
}

func TestSweepOrphansDeletesOnlyStaleVariantSidecars(t *testing.T) {
	old := time.Now().Add(-24 * time.Hour)

	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "sc-1", Name: variantName(old, "mut-001"), OrgID: "org-aaa"},
		{ID: "sc-2", Name: "happy-quickly-tesla", OrgID: "org-aaa"},
		{ID: "sc-3", Name: variantName(old, "mut-002"), OrgID: "org-aaa"},
		{ID: "sc-4", Name: variantName(old, "mut-003"), OrgID: "org-other"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	swept := variants.SweepOrphans(context.Background(), newTestClient(t, srv), "org-aaa", nopStatus)
	assert.Equal(t, swept, 2)

	// The user's own sidecar and another org's must survive: sweeping is a
	// cleanup for this package's leftovers, not a general-purpose purge.
	var remaining []string
	for _, s := range cci.Sidecars {
		remaining = append(remaining, s.ID)
	}
	assert.Check(t, cmp.Contains(remaining, "sc-2"))
	assert.Check(t, cmp.Contains(remaining, "sc-4"))
	assert.Check(t, !slices.Contains(remaining, "sc-1"))
	assert.Check(t, !slices.Contains(remaining, "sc-3"))
}

// TestSweepOrphansSparesConcurrentRun pins the hazard that two runs at once —
// two worktrees, two repos, an agent and a human — are a normal shape for this
// command. A sweep that deleted every variant sidecar it could see would kill the
// other run's in-flight work, and those variants would come back as errors: not a
// false pass, but a run silently destroyed by an unrelated one.
//
// The two sidecars here are what a long concurrent run looks like: it started
// hours ago and is still going, so one of its sidecars is long finished and
// stranded while the one it is working on right now was booted seconds ago. Only
// the stranded one may be taken. This is why the name carries each sidecar's own
// creation time rather than the run's start time — under a per-run stamp, every
// sidecar in a run this old would look collectable, including the live one.
func TestSweepOrphansSparesConcurrentRun(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "sc-live", Name: variantName(time.Now().Add(-30*time.Second), "mut-099"), OrgID: "org-aaa"},
		{ID: "sc-stale", Name: variantName(time.Now().Add(-48*time.Hour), "mut-001"), OrgID: "org-aaa"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	swept := variants.SweepOrphans(context.Background(), newTestClient(t, srv), "org-aaa", nopStatus)
	assert.Equal(t, swept, 1)

	var remaining []string
	for _, s := range cci.Sidecars {
		remaining = append(remaining, s.ID)
	}
	assert.Check(t, cmp.Contains(remaining, "sc-live"))
	assert.Check(t, !slices.Contains(remaining, "sc-stale"))
}

// TestSweepOrphansSparesUndatableNames covers a variant sidecar whose name has no
// parseable timestamp — including one written before this naming scheme existed.
// The sweep cannot show it is abandoned, so it must leave it alone rather than
// guess.
func TestSweepOrphansSparesUndatableNames(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "sc-1", Name: "variant-mut-001", OrgID: "org-aaa"},
		{ID: "sc-2", Name: "variant-", OrgID: "org-aaa"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	swept := variants.SweepOrphans(context.Background(), newTestClient(t, srv), "org-aaa", nopStatus)
	assert.Equal(t, swept, 0)
	assert.Equal(t, len(cci.Sidecars), 2)
}

func TestSweepOrphansToleratesListFailure(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ListStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	// A sweep failure must not abort the run the caller actually asked for.
	swept := variants.SweepOrphans(context.Background(), newTestClient(t, srv), "org-aaa", nopStatus)
	assert.Equal(t, swept, 0)
}
