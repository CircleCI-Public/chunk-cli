package github_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"gotest.tools/v3/assert"
)

func TestCreateCommitStatus(t *testing.T) {
	var gotMethod, gotPath string
	var gotBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &gotBody)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	c, err := github.New(github.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	err = c.CreateCommitStatus(context.Background(), "myorg", "myrepo", "abc123", "success", "chunk/test", "chunk validate: test")
	assert.NilError(t, err)

	assert.Equal(t, gotMethod, "POST")
	assert.Equal(t, gotPath, "/repos/myorg/myrepo/statuses/abc123")
	assert.Equal(t, gotBody["state"], "success")
	assert.Equal(t, gotBody["context"], "chunk/test")
	assert.Equal(t, gotBody["description"], "chunk validate: test")
}

func TestCreateCommitStatus_Error(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()

	c, err := github.New(github.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	err = c.CreateCommitStatus(context.Background(), "org", "repo", "sha", "success", "chunk/test", "desc")
	assert.Assert(t, err != nil)
}
