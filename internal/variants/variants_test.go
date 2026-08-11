package variants_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

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
		Workspace: "./workspace/repo",
		Commands:  []string{"go test ./..."},
		Parallel:  5,
		StatusFn:  nopStatus,
	}
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
func TestRunDeletesEverySidecarOnCancel(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	vs := []variants.Variant{{ID: "MUT-001"}, {ID: "MUT-002"}, {ID: "MUT-003"}}
	opts := defaultOpts()
	opts.Parallel = 1

	results, err := variants.Run(ctx, newTestClient(t, srv), vs, opts)
	assert.NilError(t, err)
	assert.Check(t, cmp.Len(results, 3))

	// Whichever variants won the race to start, none may outlive the run: a
	// created sidecar without a matching delete is a leaked instance.
	creates, deletes := countSidecarCalls(cci)
	assert.Equal(t, creates, deletes, "every created sidecar must be deleted")

	for _, r := range results {
		assert.Check(t, !r.Killed, "cancelled variant must not be reported as killed")
	}
}

func TestSweepOrphansDeletesOnlyVariantSidecars(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "sc-1", Name: "variant-mut-001", OrgID: "org-aaa"},
		{ID: "sc-2", Name: "happy-quickly-tesla", OrgID: "org-aaa"},
		{ID: "sc-3", Name: "variant-mut-002", OrgID: "org-aaa"},
		{ID: "sc-4", Name: "variant-mut-003", OrgID: "org-other"},
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

func TestSweepOrphansToleratesListFailure(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ListStatusCode = 500
	srv := httptest.NewServer(cci)
	defer srv.Close()

	// A sweep failure must not abort the run the caller actually asked for.
	swept := variants.SweepOrphans(context.Background(), newTestClient(t, srv), "org-aaa", nopStatus)
	assert.Equal(t, swept, 0)
}
