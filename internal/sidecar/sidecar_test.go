package sidecar_test

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func newClient(t *testing.T, serverURL string) *circleci.Client {
	t.Helper()
	cl, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: serverURL})
	assert.NilError(t, err)
	return cl
}

// TestOpenSessionGeneratesMissingKey covers the promise made in
// GETTING_STARTED.md: a user never has to create keys by hand, whichever
// command reaches a sidecar first.
func TestOpenSessionGeneratesMissingKey(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	// Use a temp dir as HOME so ~/.ssh/chunk_ai definitely doesn't exist.
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	cl := newClient(t, srv.URL)
	ctx := context.Background()

	keyPath := filepath.Join(home, ".ssh", "chunk_ai")
	sess, err := sidecar.OpenSession(ctx, cl, "sb-1", false)
	assert.NilError(t, err)
	assert.Equal(t, sess.IdentityFile, keyPath)

	priv, err := os.Stat(keyPath)
	assert.NilError(t, err)
	assert.Equal(t, priv.Mode().Perm(), os.FileMode(0o600))
	_, err = os.Stat(keyPath + ".pub")
	assert.NilError(t, err)
}

// TestOpenSessionKeepsExistingKey guards against regenerating over a key the
// sidecar has already been told about.
func TestOpenSessionKeepsExistingKey(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	home := t.TempDir()
	t.Setenv(config.EnvHome, home)

	keyPath := filepath.Join(home, ".ssh", "chunk_ai")
	fakes.GenerateSSHKeypairAt(t, keyPath)
	privBefore, err := os.ReadFile(keyPath)
	assert.NilError(t, err)
	pubBefore, err := os.ReadFile(keyPath + ".pub")
	assert.NilError(t, err)

	_, err = sidecar.OpenSession(context.Background(), newClient(t, srv.URL), "sb-1", false)
	assert.NilError(t, err)

	privAfter, err := os.ReadFile(keyPath)
	assert.NilError(t, err)
	pubAfter, err := os.ReadFile(keyPath + ".pub")
	assert.NilError(t, err)
	assert.Equal(t, string(privAfter), string(privBefore))
	assert.Equal(t, string(pubAfter), string(pubBefore))
}

func TestCreate(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cl := newClient(t, srv.URL)
	ctx := context.Background()

	sb, err := sidecar.Create(ctx, cl, "org-1", "my-sidecar", "ubuntu:22.04")
	assert.NilError(t, err)
	assert.Equal(t, sb.ID, "sidecar-new-1")
	assert.Equal(t, sb.Name, "my-sidecar")
	assert.Equal(t, sb.OrgID, "org-1")
}

func TestAddSSHKey(t *testing.T) {
	t.Run("from string", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		cci.AddKeyURL = "sidecar.example.com"
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		resp, err := sidecar.AddSSHKey(ctx, cl, "sb-1", "ssh-ed25519 AAAA test@test", "")
		assert.NilError(t, err)
		assert.Equal(t, resp.URL, "sidecar.example.com")
	})

	t.Run("from file", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		cci.AddKeyURL = "sidecar.example.com"
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		dir := t.TempDir()
		keyFile := filepath.Join(dir, "key.pub")
		err := os.WriteFile(keyFile, []byte("ssh-ed25519 AAAA test@test\n"), 0o644)
		assert.NilError(t, err)

		resp, err := sidecar.AddSSHKey(ctx, cl, "sb-1", "", keyFile)
		assert.NilError(t, err)
		assert.Equal(t, resp.URL, "sidecar.example.com")
	})

	t.Run("mutually exclusive", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		_, err := sidecar.AddSSHKey(ctx, cl, "sb-1", "ssh-ed25519 AAAA", "/some/file")
		assert.ErrorContains(t, err, "mutually exclusive")
	})

	t.Run("neither provided", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		_, err := sidecar.AddSSHKey(ctx, cl, "sb-1", "", "")
		assert.ErrorContains(t, err, "required")
	})

	t.Run("rejects private key string", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		_, err := sidecar.AddSSHKey(ctx, cl, "sb-1", "-----BEGIN OPENSSH PRIVATE KEY-----\ndata\n-----END OPENSSH PRIVATE KEY-----", "")
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

		_, err = sidecar.AddSSHKey(ctx, cl, "sb-1", "", keyFile)
		assert.ErrorContains(t, err, "private key")
	})

	t.Run("missing file", func(t *testing.T) {
		cci := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(cci)
		defer srv.Close()

		cl := newClient(t, srv.URL)
		ctx := context.Background()

		_, err := sidecar.AddSSHKey(ctx, cl, "sb-1", "", "/nonexistent/key.pub")
		assert.ErrorContains(t, err, "read public key file")
	})
}
