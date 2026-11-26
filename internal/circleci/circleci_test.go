package circleci

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/recorder"
)

// newTestClient creates a Client pointed at the given test server.
func newTestClient(t *testing.T, url string) *Client {
	t.Helper()
	cl := hc.New(hc.Config{
		BaseURL:    url,
		AuthToken:  "test-token",
		AuthHeader: "Circle-Token",
	})
	return &Client{cl: cl}
}

func TestNewClient(t *testing.T) {
	t.Run("creates client with token", func(t *testing.T) {
		c, err := NewClient(Config{Token: "explicit-token", BaseURL: "https://circleci.com"})
		assert.NilError(t, err)
		assert.Assert(t, c != nil)
	})

	t.Run("uses base URL", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()

		c, err := NewClient(Config{Token: "test-token", BaseURL: srv.URL})
		assert.NilError(t, err)

		ctx := context.Background()
		assert.NilError(t, c.GetCurrentUser(ctx))
	})

	t.Run("returns error when no token", func(t *testing.T) {
		_, err := NewClient(Config{BaseURL: "https://circleci.com"})
		assert.Assert(t, err != nil)
	})
}

func TestListSidecars(t *testing.T) {
	fake := fakes.NewFakeCircleCI()
	fake.Sidecars = []fakes.Sidecar{
		{ID: "sb-1", Name: "dev", OrgID: "org-1"},
		{ID: "sb-2", Name: "staging", OrgID: "org-1"},
		{ID: "sb-3", Name: "other", OrgID: "org-2"},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	ctx := context.Background()

	t.Run("filters by org", func(t *testing.T) {
		sidecars, err := client.ListSidecars(ctx, "org-1", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sidecars) != 2 {
			t.Fatalf("expected 2 sidecars, got %d", len(sidecars))
		}
		if sidecars[0].ID != "sb-1" || sidecars[1].ID != "sb-2" {
			t.Errorf("unexpected sidecar IDs: %v, %v", sidecars[0].ID, sidecars[1].ID)
		}
	})

	t.Run("empty result", func(t *testing.T) {
		sidecars, err := client.ListSidecars(ctx, "org-nonexistent", false)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sidecars) != 0 {
			t.Fatalf("expected 0 sidecars, got %d", len(sidecars))
		}
	})

	t.Run("sends all=false by default", func(t *testing.T) {
		fake.Recorder.AllRequests() // baseline
		_, err := client.ListSidecars(ctx, "org-1", false)
		assert.NilError(t, err)
		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		assert.Equal(t, last.Method, "GET")
		assert.Equal(t, last.URL.Query().Get("org_id"), "org-1")
		assert.Equal(t, last.URL.Query().Get("all"), "false")
	})

	t.Run("sends all=true when requested", func(t *testing.T) {
		fake.Recorder.AllRequests() // baseline
		_, err := client.ListSidecars(ctx, "org-1", true)
		assert.NilError(t, err)
		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		assert.Equal(t, last.URL.Query().Get("all"), "true")
	})
}

func countRequests(reqs []recorder.RecordedRequest, method, path string) int {
	n := 0
	for _, r := range reqs {
		if r.Method == method && r.URL.Path == path {
			n++
		}
	}
	return n
}

// shrinkStartWait makes CreateSidecar's wait for a sidecar to start fast.
func shrinkStartWait(t *testing.T, interval, timeout time.Duration) {
	t.Helper()
	prevInterval, prevTimeout := startPollInterval, startTimeout
	startPollInterval, startTimeout = interval, timeout
	t.Cleanup(func() { startPollInterval, startTimeout = prevInterval, prevTimeout })
}

const newSidecarPath = "/api/v3/sidecar/instances/sidecar-new-1"

func TestCreateSidecar(t *testing.T) {
	shrinkStartWait(t, time.Millisecond, 200*time.Millisecond)

	tests := []struct {
		name       string
		setup      func(*fakes.FakeCircleCI)
		wantErr    string // substring; "" means success
		wantIsFail bool   // errors.Is(err, ErrSidecarFailedToStart)
		wantGets   int    // GET newSidecarPath calls; -1 means do not check
		wantDelete bool   // DELETE newSidecarPath sent
	}{
		{
			name:     "started on first poll",
			wantGets: 1,
		},
		{
			name:     "received then started",
			setup:    func(f *fakes.FakeCircleCI) { f.ReceivedPolls = 3 },
			wantGets: 4,
		},
		{
			name:     "old provisioner without phase",
			setup:    func(f *fakes.FakeCircleCI) { f.OmitCreatePhase = true },
			wantGets: 0,
		},
		{
			name:     "create already started",
			setup:    func(f *fakes.FakeCircleCI) { f.CreatePhase = "started" },
			wantGets: 0,
		},
		{
			name:       "create already ended",
			setup:      func(f *fakes.FakeCircleCI) { f.CreatePhase = "ended" },
			wantErr:    "failed to start",
			wantIsFail: true,
			wantGets:   0,
		},
		{
			name:     "GET answers an old-style phase",
			setup:    func(f *fakes.FakeCircleCI) { f.GetPhase = "running" },
			wantGets: 1,
		},
		{
			name:     "GET answers no phase",
			setup:    func(f *fakes.FakeCircleCI) { f.OmitGetPhase = true },
			wantGets: 1,
		},
		{
			name:       "ended before starting",
			setup:      func(f *fakes.FakeCircleCI) { f.FailStart = map[string]bool{"sidecar-new-1": true} },
			wantErr:    "sidecar sidecar-new-1 failed to start (outcome: failed)",
			wantIsFail: true,
			wantGets:   1,
		},
		{
			name:       "GET fails while waiting",
			setup:      func(f *fakes.FakeCircleCI) { f.GetStatusCode = http.StatusNotFound },
			wantErr:    "get sidecar",
			wantGets:   1,
			wantDelete: true,
		},
		{
			name:       "never starts",
			setup:      func(f *fakes.FakeCircleCI) { f.ReceivedPolls = 1 << 30 },
			wantErr:    "did not start within",
			wantGets:   -1, // how many polls fit in the time limit varies
			wantDelete: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := fakes.NewFakeCircleCI()
			if tt.setup != nil {
				tt.setup(fake)
			}
			srv := httptest.NewServer(fake)
			t.Cleanup(srv.Close)
			client := newTestClient(t, srv.URL)

			sb, err := client.CreateSidecar(context.Background(), "org-1", "my-sidecar", "ubuntu:22.04", "")

			if tt.wantErr == "" {
				assert.NilError(t, err)
				assert.Check(t, cmp.Equal(sb.ID, "sidecar-new-1"))
				assert.Check(t, cmp.Equal(sb.Name, "my-sidecar"))
				assert.Check(t, cmp.Equal(sb.OrgID, "org-1"))
			} else {
				assert.Check(t, cmp.ErrorContains(err, tt.wantErr))
				assert.Check(t, cmp.Equal(errors.Is(err, ErrSidecarFailedToStart), tt.wantIsFail))
			}
			reqs := fake.Recorder.AllRequests()
			if tt.wantGets >= 0 {
				assert.Check(t, cmp.Equal(countRequests(reqs, http.MethodGet, newSidecarPath), tt.wantGets))
			}
			assert.Check(t, cmp.Equal(countRequests(reqs, http.MethodDelete, newSidecarPath) == 1, tt.wantDelete))
		})
	}
}

func TestCreateSidecarCancelledWhileWaiting(t *testing.T) {
	shrinkStartWait(t, 5*time.Millisecond, time.Minute)

	fake := fakes.NewFakeCircleCI()
	fake.ReceivedPolls = 1 << 30
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	client := newTestClient(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	_, err := client.CreateSidecar(ctx, "org-1", "my-sidecar", "ubuntu:22.04", "")

	assert.Check(t, cmp.ErrorIs(err, context.Canceled))
	reqs := fake.Recorder.AllRequests()
	assert.Check(t, cmp.Equal(countRequests(reqs, http.MethodDelete, newSidecarPath), 1),
		"the sidecar should be deleted even though the caller's context was cancelled")
}

func TestDeleteSidecar(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.Sidecars = []fakes.Sidecar{{ID: "sb-1", OrgID: "org-1"}}
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		err := client.DeleteSidecar(ctx, "sb-1")
		assert.NilError(t, err)

		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		if last.Method != "DELETE" {
			t.Errorf("expected DELETE, got %s", last.Method)
		}
		if last.URL.Path != "/api/v3/sidecar/instances/sb-1" {
			t.Errorf("unexpected path: %s", last.URL.Path)
		}
		if got := last.Header.Get("Circle-Token"); got != "test-token" {
			t.Errorf("expected Circle-Token test-token, got %s", got)
		}
	})

	t.Run("returns error on server failure", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.DeleteStatusCode = 500
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		err := client.DeleteSidecar(ctx, "sb-1")
		assert.Assert(t, err != nil, "expected error for 500 response")
	})

	t.Run("maps 401 to ErrNotAuthorized", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.DeleteStatusCode = 401
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		err := client.DeleteSidecar(ctx, "sb-1")
		assert.Assert(t, err != nil, "expected error for 401 response")
		if !errors.Is(err, ErrNotAuthorized) {
			t.Errorf("expected ErrNotAuthorized, got %v", err)
		}
	})
}

func TestAddSSHKey(t *testing.T) {
	fake := fakes.NewFakeCircleCI()
	fake.AddKeyURL = "sidecar-host.example.com"
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	ctx := context.Background()

	resp, err := client.AddSSHKey(ctx, "sb-1", "ssh-rsa AAAA...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.URL != "sidecar-host.example.com" {
		t.Errorf("expected sidecar-host.example.com, got %s", resp.URL)
	}

	// Verify Circle-Token auth header was sent.
	reqs := fake.Recorder.AllRequests()
	last := reqs[len(reqs)-1]
	if got := last.Header.Get("Circle-Token"); got != "test-token" {
		t.Errorf("expected Circle-Token test-token, got %s", got)
	}
	// Verify sidecar ID in URL path.
	if last.URL.Path != "/api/v3/sidecar/instances/sb-1/ssh/add-key" {
		t.Errorf("unexpected path: %s", last.URL.Path)
	}
}

func TestExec(t *testing.T) {
	t.Run("default response", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		resp, err := client.Exec(ctx, "sb-1", "ls", []string{"-la"}, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.CommandID != "cmd-123" {
			t.Errorf("expected cmd-123, got %s", resp.CommandID)
		}
		if resp.ExitCode != 0 {
			t.Errorf("expected exit code 0, got %d", resp.ExitCode)
		}
		if resp.PID != 42 {
			t.Errorf("expected PID 42, got %d", resp.PID)
		}
	})

	t.Run("custom response", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.ExecResponse = &fakes.ExecResponse{
			CommandID: "cmd-custom",
			PID:       99,
			Stdout:    "hello\n",
			Stderr:    "warn\n",
			ExitCode:  1,
		}
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		resp, err := client.Exec(ctx, "sb-1", "echo", []string{"hello"}, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.Stdout != "hello\n" {
			t.Errorf("expected hello\\n, got %s", resp.Stdout)
		}
		if resp.Stderr != "warn\n" {
			t.Errorf("expected warn\\n, got %s", resp.Stderr)
		}
		if resp.ExitCode != 1 {
			t.Errorf("expected exit code 1, got %d", resp.ExitCode)
		}
	})

	t.Run("outdated sidecar surfaces server message", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.CommandOutputStatusCode = http.StatusGone
		fake.CommandOutputMessage = "sidecar predates async exec; recreate with: chunk sidecar create"
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		_, err := client.Exec(context.Background(), "sb-1", "echo", nil, nil, nil)
		var se *StatusError
		if !errors.As(err, &se) || se.StatusCode != http.StatusGone {
			t.Fatalf("expected 410 StatusError, got %v", err)
		}
		if se.ServerMessage != fake.CommandOutputMessage {
			t.Errorf("expected server message %q, got %q", fake.CommandOutputMessage, se.ServerMessage)
		}
	})

	t.Run("sends sidecar ID in path", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		_, err := client.Exec(ctx, "sb-1", "pwd", nil, nil, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		var found bool
		for _, req := range fake.Recorder.AllRequests() {
			if req.URL.Path == "/api/v3/sidecar/instances/sb-1/exec" {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected a request to /api/v3/sidecar/instances/sb-1/exec")
		}
	})
}

func TestTriggerRun(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.RunResponse = &fakes.RunResponse{
			RunID:      "run-xyz",
			PipelineID: "pipe-abc",
		}
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		resp, err := client.TriggerRun(ctx, "org-1", "proj-1", TriggerRunRequest{
			AgentType:      "chunk",
			DefinitionID:   "def-1",
			CheckoutBranch: "main",
			TriggerSource:  "cli",
			TriggerType:    "api",
			Parameters:     map[string]interface{}{"key": "val"},
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.RunID != "run-xyz" {
			t.Errorf("expected run-xyz, got %s", resp.RunID)
		}
		if resp.PipelineID != "pipe-abc" {
			t.Errorf("expected pipe-abc, got %s", resp.PipelineID)
		}

		// Verify path includes org and project.
		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		expected := "/api/v2/agents/org/org-1/project/proj-1/runs"
		if last.URL.Path != expected {
			t.Errorf("expected path %s, got %s", expected, last.URL.Path)
		}
	})

	t.Run("default response", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		resp, err := client.TriggerRun(ctx, "org-1", "proj-1", TriggerRunRequest{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp.RunID != "run-abc-123" {
			t.Errorf("expected run-abc-123, got %s", resp.RunID)
		}
	})

	t.Run("error status code", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		fake.RunStatusCode = 500
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		_, err := client.TriggerRun(ctx, "org-1", "proj-1", TriggerRunRequest{})
		if err == nil {
			t.Fatal("expected error on 500 response")
		}
	})
}

func TestMapErr_410_SurfacesBody(t *testing.T) {
	t.Run("json error field", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"error":"upgrade chunk CLI to v2.x or later"}`))
		}))
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		_, err := client.ListSidecars(context.Background(), "org-1", false)

		assert.Assert(t, err != nil)
		assert.Assert(t, strings.Contains(err.Error(), "upgrade chunk CLI"), "error should include server message, got: %v", err)
	})

	t.Run("json message field", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte(`{"message":"please upgrade"}`))
		}))
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		_, err := client.ListSidecars(context.Background(), "org-1", false)

		assert.Assert(t, err != nil)
		assert.Assert(t, strings.Contains(err.Error(), "please upgrade"), "error should include server message, got: %v", err)
	})

	t.Run("non-json body falls back to raw string", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusGone)
			_, _ = w.Write([]byte("this endpoint has been removed"))
		}))
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		_, err := client.ListSidecars(context.Background(), "org-1", false)

		assert.Assert(t, err != nil)
		assert.Assert(t, strings.Contains(err.Error(), "this endpoint has been removed"), "error should include raw body, got: %v", err)
	})

	t.Run("empty body shows plain 410", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusGone)
		}))
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		_, err := client.ListSidecars(context.Background(), "org-1", false)

		assert.Assert(t, err != nil)
		var se *hc.StatusError
		assert.Assert(t, errors.As(err, &se))
		assert.Equal(t, se.StatusCode, http.StatusGone)
		assert.Equal(t, se.ServerMessage, "")
	})
}

func TestAuthRequired(t *testing.T) {
	// Verify that the fake returns 401 when no Circle-Token header is present.
	fake := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	// Create a client with no auth token to trigger 401.
	cl := hc.New(hc.Config{
		BaseURL: srv.URL,
	})
	client := &Client{cl: cl}
	ctx := context.Background()

	t.Run("ListSidecars", func(t *testing.T) {
		_, err := client.ListSidecars(ctx, "org-1", false)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("CreateSidecar", func(t *testing.T) {
		_, err := client.CreateSidecar(ctx, "org-1", "name", "image", "")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("Exec", func(t *testing.T) {
		_, err := client.Exec(ctx, "sb-1", "ls", nil, nil, nil)
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("AddSSHKey", func(t *testing.T) {
		_, err := client.AddSSHKey(ctx, "sb-1", "ssh-rsa AAAA")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("TriggerRun", func(t *testing.T) {
		_, err := client.TriggerRun(ctx, "org-1", "proj-1", TriggerRunRequest{})
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("CreateOrg", func(t *testing.T) {
		_, err := client.CreateOrg(ctx, "my-org")
		if err == nil {
			t.Fatal("expected error")
		}
	})
}
