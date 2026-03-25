package sandbox_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/sandbox"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func newClient(t *testing.T, serverURL string) *circleci.Client {
	t.Helper()
	t.Setenv("CIRCLE_TOKEN", "fake-token")
	t.Setenv("CIRCLECI_BASE_URL", serverURL)
	cl, err := circleci.NewClient()
	assert.NilError(t, err)
	return cl
}

func TestList(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sandboxes = []fakes.Sandbox{
		{ID: "sb-1", Name: "alpha", OrganizationID: "org-1"},
		{ID: "sb-2", Name: "beta", OrganizationID: "org-1"},
		{ID: "sb-3", Name: "gamma", OrganizationID: "org-2"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cl := newClient(t, srv.URL)
	ctx := context.Background()

	t.Run("returns sandboxes for org", func(t *testing.T) {
		sandboxes, err := sandbox.List(ctx, cl, "org-1")
		assert.NilError(t, err)
		assert.Equal(t, len(sandboxes), 2)
		assert.Equal(t, sandboxes[0].Name, "alpha")
		assert.Equal(t, sandboxes[1].Name, "beta")
	})

	t.Run("empty for unknown org", func(t *testing.T) {
		sandboxes, err := sandbox.List(ctx, cl, "org-unknown")
		assert.NilError(t, err)
		assert.Equal(t, len(sandboxes), 0)
	})
}

func TestCreate(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cl := newClient(t, srv.URL)
	ctx := context.Background()

	sb, err := sandbox.Create(ctx, cl, "org-1", "my-sandbox", "ubuntu:22.04")
	assert.NilError(t, err)
	assert.Equal(t, sb.ID, "sandbox-new-123")
	assert.Equal(t, sb.Name, "my-sandbox")
	assert.Equal(t, sb.OrganizationID, "org-1")
}

func TestExec(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ExecResponse = &fakes.ExecResponse{
		CommandID: "cmd-1",
		PID:       10,
		Stdout:    "output\n",
		Stderr:    "",
		ExitCode:  0,
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cl := newClient(t, srv.URL)
	ctx := context.Background()

	resp, err := sandbox.Exec(ctx, cl, "sb-1", "echo", []string{"hello"})
	assert.NilError(t, err)
	assert.Equal(t, resp.Stdout, "output\n")
	assert.Equal(t, resp.ExitCode, 0)

	// Verify exec request was made with sandbox ID in path
	reqs := cci.Recorder.AllRequests()
	var gotExecReq bool
	for _, r := range reqs {
		if r.URL.Path == "/api/v2/sandbox/instances/sb-1/exec" {
			gotExecReq = true
		}
	}
	assert.Assert(t, gotExecReq, "expected exec request at /api/v2/sandbox/instances/sb-1/exec")
}

func TestAddSshKey(t *testing.T) {
	t.Run("from string", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		cci.AddKeyURL = "sandbox.example.com"
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		resp, err := sandbox.AddSshKey(ctx, cl, "sb-1", "ssh-ed25519 AAAA test@test", "")
		assert.NilError(t, err)
		assert.Equal(t, resp.URL, "sandbox.example.com")
	})

	t.Run("from file", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		cci.AddKeyURL = "sandbox.example.com"
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		dir := t.TempDir()
		keyFile := filepath.Join(dir, "key.pub")
		err := os.WriteFile(keyFile, []byte("ssh-ed25519 AAAA test@test\n"), 0o644)
		assert.NilError(t, err)

		resp, err := sandbox.AddSshKey(ctx, cl, "sb-1", "", keyFile)
		assert.NilError(t, err)
		assert.Equal(t, resp.URL, "sandbox.example.com")
	})

	t.Run("mutually exclusive", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		_, err := sandbox.AddSshKey(ctx, cl, "sb-1", "ssh-ed25519 AAAA", "/some/file")
		assert.ErrorContains(t, err, "mutually exclusive")
	})

	t.Run("neither provided", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		_, err := sandbox.AddSshKey(ctx, cl, "sb-1", "", "")
		assert.ErrorContains(t, err, "required")
	})

	t.Run("rejects private key string", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		_, err := sandbox.AddSshKey(ctx, cl, "sb-1", "-----BEGIN OPENSSH PRIVATE KEY-----\ndata\n-----END OPENSSH PRIVATE KEY-----", "")
		assert.ErrorContains(t, err, "private key")
	})

	t.Run("rejects private key file", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		dir := t.TempDir()
		keyFile := filepath.Join(dir, "priv.pem")
		err := os.WriteFile(keyFile, []byte("-----BEGIN OPENSSH PRIVATE KEY-----\ndata\n-----END OPENSSH PRIVATE KEY-----\n"), 0o644)
		assert.NilError(t, err)

		_, err = sandbox.AddSshKey(ctx, cl, "sb-1", "", keyFile)
		assert.ErrorContains(t, err, "private key")
	})

	t.Run("missing file", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		_, err := sandbox.AddSshKey(ctx, cl, "sb-1", "", "/nonexistent/key.pub")
		assert.ErrorContains(t, err, "read public key file")
	})
}
