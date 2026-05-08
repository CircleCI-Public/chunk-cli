package variants_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/variants"
)

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

func TestRunDeleteCalledOnCreateSuccess(t *testing.T) {
	// Create succeeds; Sync fails at SSH key registration so delete must still run.
	// AddKeyStatusCode=500 prevents OpenSession from succeeding, which means Sync
	// never reaches persistWorkspace and cannot corrupt the caller's active sidecar.
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

	reqs := cci.Recorder.AllRequests()
	var creates, deletes int
	for _, r := range reqs {
		if r.URL.Path == "/api/v2/sidecar/instances" && r.Method == "POST" {
			creates++
		}
		if r.URL.Path != "/api/v2/sidecar/instances" && r.Method == "DELETE" {
			deletes++
		}
	}
	assert.Check(t, creates >= 1, "expected at least 1 create request")
	assert.Check(t, deletes >= 1, "expected at least 1 delete request")
}
