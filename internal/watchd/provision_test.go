package watchd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func newTestClient(t *testing.T, serverURL string) *circleci.Client {
	t.Helper()
	cl, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: serverURL})
	assert.NilError(t, err)
	return cl
}

// startTestDaemonWithClient starts a daemon backed by the given circleci.Client.
func startTestDaemonWithClient(t *testing.T, client *circleci.Client) {
	t.Helper()
	dir, err := os.MkdirTemp("", "wd-prov")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_WATCHD_DIR", dir)
	t.Setenv("XDG_DATA_HOME", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- RunDaemon(ctx, client, "", nil) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-errCh:
		case <-time.After(5 * time.Second):
			t.Error("daemon did not shut down within 5s")
		}
	})

	sockPath, err := SocketPath()
	assert.NilError(t, err)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reachable, _ := ping(sockPath); reachable {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("daemon did not become reachable within 5s")
}

func postProvision(t *testing.T, req ProvisionRequest) *http.Response {
	t.Helper()
	sockPath, err := SocketPath()
	assert.NilError(t, err)
	body, err := json.Marshal(req)
	assert.NilError(t, err)
	resp, err := unixClient(sockPath).Post("http://watchd/sidecar", "application/json", bytes.NewReader(body))
	assert.NilError(t, err)
	return resp
}

func deleteProvision(t *testing.T, id string) *http.Response {
	t.Helper()
	sockPath, err := SocketPath()
	assert.NilError(t, err)
	req, err := http.NewRequest(http.MethodDelete, "http://watchd/sidecar/"+id, nil)
	assert.NilError(t, err)
	resp, err := unixClient(sockPath).Do(req)
	assert.NilError(t, err)
	return resp
}

func TestProvisionReturns503WhenNoClient(t *testing.T) {
	startTestDaemon(t)

	resp := postProvision(t, ProvisionRequest{OrgID: "org-1", Name: "sc"})
	defer func() { _ = resp.Body.Close() }()
	assert.Check(t, cmp.Equal(resp.StatusCode, http.StatusServiceUnavailable))
}

func TestProvisionRejectsMissingOrgID(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()
	startTestDaemonWithClient(t, newTestClient(t, srv.URL))

	resp := postProvision(t, ProvisionRequest{Name: "sc"})
	defer func() { _ = resp.Body.Close() }()
	assert.Check(t, cmp.Equal(resp.StatusCode, http.StatusBadRequest))
}

func TestProvisionRejectsMissingName(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()
	startTestDaemonWithClient(t, newTestClient(t, srv.URL))

	resp := postProvision(t, ProvisionRequest{OrgID: "org-1"})
	defer func() { _ = resp.Body.Close() }()
	assert.Check(t, cmp.Equal(resp.StatusCode, http.StatusBadRequest))
}

func TestProvisionCreatesAndReturnsSidecarID(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()
	startTestDaemonWithClient(t, newTestClient(t, srv.URL))

	resp := postProvision(t, ProvisionRequest{OrgID: "org-1", Name: "test-sc"})
	defer func() { _ = resp.Body.Close() }()
	assert.Check(t, cmp.Equal(resp.StatusCode, http.StatusOK))

	var result ProvisionResponse
	err := json.NewDecoder(resp.Body).Decode(&result)
	assert.NilError(t, err)
	assert.Check(t, result.SidecarID != "", "expected non-empty sidecar ID")
}

func TestDeprovisionReturns503WhenNoClient(t *testing.T) {
	startTestDaemon(t)

	resp := deleteProvision(t, "some-id")
	defer func() { _ = resp.Body.Close() }()
	assert.Check(t, cmp.Equal(resp.StatusCode, http.StatusServiceUnavailable))
}

func TestDeprovisionDeletesSidecar(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()
	startTestDaemonWithClient(t, newTestClient(t, srv.URL))

	// Provision a sidecar first so we have an ID to delete.
	provResp := postProvision(t, ProvisionRequest{OrgID: "org-1", Name: "to-delete"})
	defer func() { _ = provResp.Body.Close() }()
	assert.Check(t, cmp.Equal(provResp.StatusCode, http.StatusOK))

	var prov ProvisionResponse
	err := json.NewDecoder(provResp.Body).Decode(&prov)
	assert.NilError(t, err)

	resp := deleteProvision(t, prov.SidecarID)
	defer func() { _ = resp.Body.Close() }()
	assert.Check(t, cmp.Equal(resp.StatusCode, http.StatusNoContent))
}

func TestProvisionSidecarMethodNotAllowed(t *testing.T) {
	startTestDaemon(t)

	sockPath, err := SocketPath()
	assert.NilError(t, err)
	resp, err := unixClient(sockPath).Get("http://watchd/sidecar")
	assert.NilError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Check(t, cmp.Equal(resp.StatusCode, http.StatusMethodNotAllowed))
}

// Verify the provisioner cleans up owned sidecars on shutdown.
func TestProvisionerStopAllDeletesOwnedSidecars(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	cl := newTestClient(t, srv.URL)
	p := newProvisioner(cl)
	ctx := context.Background()

	id, err := p.create(ctx, "org-1", "cleanup-sc", "")
	assert.NilError(t, err)
	assert.Check(t, id != "")

	p.mu.Lock()
	owned := len(p.owned)
	p.mu.Unlock()
	assert.Check(t, cmp.Equal(owned, 1))

	p.stopAll(ctx)

	p.mu.Lock()
	remaining := len(p.owned)
	p.mu.Unlock()
	assert.Check(t, cmp.Equal(remaining, 0))
}
