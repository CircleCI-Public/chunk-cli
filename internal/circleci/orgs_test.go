package circleci

import (
	"context"
	"errors"
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
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if org.ID == "" {
			t.Error("expected non-empty org ID")
		}
		if org.Name != "my-org" {
			t.Errorf("expected name my-org, got %s", org.Name)
		}
		if org.Slug == "" {
			t.Error("expected non-empty org slug")
		}
	})

	t.Run("sends POST to /api/v2/organization with auth token", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		fake.Recorder.AllRequests() // baseline
		_, err := client.CreateOrg(ctx, "test-org")
		assert.NilError(t, err)

		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		assert.Equal(t, last.Method, "POST")
		assert.Equal(t, last.URL.Path, "/api/v2/organization")
		assert.Equal(t, last.Header.Get("Circle-Token"), "test-token")
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
		assert.Assert(t, err != nil)
		if !errors.Is(err, ErrNotAuthorized) {
			t.Errorf("expected ErrNotAuthorized, got %v", err)
		}
	})

}
