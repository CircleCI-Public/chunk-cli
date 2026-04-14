package circleci

import (
	"context"
	"net/http/httptest"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// newTestClient creates a Client pointed at the given test server.
func newTestClient(t *testing.T, url string) *Client {
	t.Helper()
	cl := httpcl.New(httpcl.Config{
		BaseURL:    url,
		AuthToken:  "test-token",
		AuthHeader: "Circle-Token",
	})
	return &Client{cl: cl}
}

func TestNewClient(t *testing.T) {
	t.Run("creates client with token from env", func(t *testing.T) {
		t.Setenv("CIRCLE_TOKEN", "explicit-token")
		t.Setenv("CIRCLECI_BASE_URL", "")
		c, err := NewClient()
		assert.NilError(t, err)
		assert.Assert(t, c != nil)
	})

	t.Run("uses base URL from env", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()

		t.Setenv("CIRCLE_TOKEN", "test-token")
		t.Setenv("CIRCLECI_BASE_URL", srv.URL)
		c, err := NewClient()
		assert.NilError(t, err)

		ctx := context.Background()
		assert.NilError(t, c.GetCurrentUser(ctx))
	})

	t.Run("returns error when no token", func(t *testing.T) {
		t.Setenv("CIRCLE_TOKEN", "")
		t.Setenv("CIRCLECI_TOKEN", "")
		t.Setenv("XDG_CONFIG_HOME", t.TempDir())
		_, err := NewClient()
		assert.Assert(t, err != nil)
	})
}

func TestListSandboxes(t *testing.T) {
	fake := fakes.NewFakeCircleCI()
	fake.Sandboxes = []fakes.Sandbox{
		{ID: "sb-1", Name: "dev", OrgID: "org-1"},
		{ID: "sb-2", Name: "staging", OrgID: "org-1"},
		{ID: "sb-3", Name: "other", OrgID: "org-2"},
	}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	ctx := context.Background()

	t.Run("filters by org", func(t *testing.T) {
		sandboxes, err := client.ListSandboxes(ctx, "org-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sandboxes) != 2 {
			t.Fatalf("expected 2 sandboxes, got %d", len(sandboxes))
		}
		if sandboxes[0].ID != "sb-1" || sandboxes[1].ID != "sb-2" {
			t.Errorf("unexpected sandbox IDs: %v, %v", sandboxes[0].ID, sandboxes[1].ID)
		}
	})

	t.Run("empty result", func(t *testing.T) {
		sandboxes, err := client.ListSandboxes(ctx, "org-nonexistent")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(sandboxes) != 0 {
			t.Fatalf("expected 0 sandboxes, got %d", len(sandboxes))
		}
	})

	t.Run("records request", func(t *testing.T) {
		fake.Recorder.AllRequests() // baseline
		_, err := client.ListSandboxes(ctx, "org-1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		if last.Method != "GET" {
			t.Errorf("expected GET, got %s", last.Method)
		}
		if got := last.URL.Query().Get("org_id"); got != "org-1" {
			t.Errorf("expected org_id=org-1, got %s", got)
		}
	})
}

func TestCreateSandbox(t *testing.T) {
	fake := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	ctx := context.Background()

	sb, err := client.CreateSandbox(ctx, "org-1", "my-sandbox", "", "ubuntu:22.04")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if sb.ID != "sandbox-new-123" {
		t.Errorf("expected ID sandbox-new-123, got %s", sb.ID)
	}
	if sb.Name != "my-sandbox" {
		t.Errorf("expected name my-sandbox, got %s", sb.Name)
	}
	if sb.OrgID != "org-1" {
		t.Errorf("expected org org-1, got %s", sb.OrgID)
	}
	if sb.Image != "ubuntu:22.04" {
		t.Errorf("expected image ubuntu:22.04, got %s", sb.Image)
	}
}

func TestAddSSHKey(t *testing.T) {
	fake := fakes.NewFakeCircleCI()
	fake.AddKeyURL = "sandbox-host.example.com"
	srv := httptest.NewServer(fake)
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	ctx := context.Background()

	resp, err := client.AddSSHKey(ctx, "sb-1", "ssh-rsa AAAA...")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.URL != "sandbox-host.example.com" {
		t.Errorf("expected sandbox-host.example.com, got %s", resp.URL)
	}

	// Verify Circle-Token auth header was sent.
	reqs := fake.Recorder.AllRequests()
	last := reqs[len(reqs)-1]
	if got := last.Header.Get("Circle-Token"); got != "test-token" {
		t.Errorf("expected Circle-Token test-token, got %s", got)
	}
	// Verify sandbox ID in URL path.
	if last.URL.Path != "/api/v2/sandbox/instances/sb-1/ssh/add-key" {
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

		resp, err := client.Exec(ctx, "sb-1", "ls", []string{"-la"})
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

		resp, err := client.Exec(ctx, "sb-1", "echo", []string{"hello"})
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

	t.Run("sends sandbox ID in path", func(t *testing.T) {
		fake := fakes.NewFakeCircleCI()
		srv := httptest.NewServer(fake)
		defer srv.Close()

		client := newTestClient(t, srv.URL)
		ctx := context.Background()

		_, err := client.Exec(ctx, "sb-1", "pwd", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		reqs := fake.Recorder.AllRequests()
		last := reqs[len(reqs)-1]
		if last.URL.Path != "/api/v2/sandbox/instances/sb-1/exec" {
			t.Errorf("expected /api/v2/sandbox/instances/sb-1/exec, got %s", last.URL.Path)
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

func TestAuthRequired(t *testing.T) {
	// Verify that the fake returns 401 when no Circle-Token header is present.
	fake := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(fake)
	defer srv.Close()

	// Create a client with no auth token to trigger 401.
	cl := httpcl.New(httpcl.Config{
		BaseURL: srv.URL,
	})
	client := &Client{cl: cl}
	ctx := context.Background()

	t.Run("ListSandboxes", func(t *testing.T) {
		_, err := client.ListSandboxes(ctx, "org-1")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("CreateSandbox", func(t *testing.T) {
		_, err := client.CreateSandbox(ctx, "org-1", "name", "", "image")
		if err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("Exec", func(t *testing.T) {
		_, err := client.Exec(ctx, "sb-1", "ls", nil)
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
}
