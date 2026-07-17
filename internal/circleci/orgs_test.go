package circleci

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestCreateOrg(t *testing.T) {
	t.Run("creates org and returns info", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		org, err := client.CreateOrg(ctx, "my-org")
		assert.NilError(t, err)
		assert.Equal(t, org.ID, "org-new-1")
		assert.Equal(t, org.Name, "my-org")
		assert.Equal(t, org.Slug, "my-org")
	})

	t.Run("sends POST to /api/v2/organization with auth token and body", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		_, err := client.CreateOrg(ctx, "test-org")
		assert.NilError(t, err)

		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		assert.Equal(t, last.Method, "POST")
		assert.Equal(t, last.URL.Path, "/api/v2/organization")
		assert.Equal(t, last.Header.Get("Circle-Token"), "test-token")

		var body map[string]string
		assert.NilError(t, json.Unmarshal(last.Body, &body))
		assert.Equal(t, body["name"], "test-org")
		assert.Equal(t, body["vcs_type"], "circleci")
	})

	t.Run("returns error on server failure", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.CreateOrgStatusCode = 500
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		_, err := client.CreateOrg(ctx, "my-org")
		assert.Assert(t, err != nil, "expected error for 500 response")
	})

	t.Run("maps 401 to ErrNotAuthorized", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.CreateOrgStatusCode = 401
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		_, err := client.CreateOrg(ctx, "my-org")
		assert.ErrorIs(t, err, ErrNotAuthorized)
	})

}
