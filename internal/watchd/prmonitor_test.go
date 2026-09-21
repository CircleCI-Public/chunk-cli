package watchd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/github"
)

// cannedPRResponse returns a minimal valid GraphQL response containing one open PR.
func cannedPRResponse(t *testing.T) []byte {
	t.Helper()
	body := map[string]any{
		"data": map[string]any{
			"repository": map[string]any{
				"pullRequests": map[string]any{
					"nodes": []map[string]any{
						{
							"number":    42,
							"title":     "Old branch PR",
							"url":       "https://github.com/org/repo/pull/42",
							"updatedAt": "2026-01-01T00:00:00Z",
							"reviews":   map[string]any{"nodes": []any{}},
							"reviewThreads": map[string]any{
								"nodes": []any{},
							},
							"commits": map[string]any{
								"nodes": []map[string]any{
									{
										"commit": map[string]any{
											"statusCheckRollup": map[string]any{
												"state": "SUCCESS",
												"contexts": map[string]any{
													"nodes": []any{},
												},
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal canned response: %v", err)
	}
	return b
}

// TestPRMonitor_StaleStateNotAnnotatedOnBranchSwitch verifies that when the
// cached PR state was fetched for branch A, annotating a snapshot for branch B
// leaves snap.PR nil rather than writing branch A's stale data.
func TestPRMonitor_StaleStateNotAnnotatedOnBranchSwitch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(cannedPRResponse(t))
	}))
	defer srv.Close()

	client, err := github.New(github.Config{
		Token:   "test-token",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}

	pm := newPRMonitor(client)

	// Simulate a completed fetch for feature/old.
	oldKey := prProjectKey{root: "/repo", branch: "feature/old"}
	pm.fetch(context.Background(), "/repo", "feature/old", "org", "repo", oldKey)

	// Verify the fetch actually populated something for the old key.
	pm.mu.Lock()
	_, hasFetch := pm.lastFetch[oldKey]
	pm.mu.Unlock()
	if !hasFetch {
		t.Fatal("expected lastFetch to be set after fetch")
	}

	// Now annotate a snapshot that is on the *new* branch.
	snap := &ProjectSnapshot{
		Root:   "/repo",
		Branch: "feature/new",
	}
	pm.annotate(snap)

	if snap.PR != nil {
		t.Errorf("annotate wrote stale PR data from feature/old onto a feature/new snapshot: PR#%d %q",
			snap.PR.Number, snap.PR.Title)
	}
}

// TestPRMonitor_SameBranchAnnotated verifies that when the cached state matches
// the snapshot's branch, annotate does populate snap.PR.
func TestPRMonitor_SameBranchAnnotated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(cannedPRResponse(t))
	}))
	defer srv.Close()

	client, err := github.New(github.Config{
		Token:   "test-token",
		BaseURL: srv.URL,
	})
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}

	pm := newPRMonitor(client)

	// Simulate a completed fetch for feature/current.
	key := prProjectKey{root: "/repo", branch: "feature/current"}
	pm.fetch(context.Background(), "/repo", "feature/current", "org", "repo", key)

	// Allow some time for the fetch to complete (it's synchronous here).
	_ = time.Now() // just to reference the import

	snap := &ProjectSnapshot{
		Root:   "/repo",
		Branch: "feature/current",
	}
	pm.annotate(snap)

	if snap.PR == nil {
		t.Error("annotate did not populate snap.PR for matching branch")
	}
}
