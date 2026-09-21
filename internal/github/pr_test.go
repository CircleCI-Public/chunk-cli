package github_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CircleCI-Public/chunk-cli/internal/github"
)

func prCannedServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newPRTestClient(t *testing.T, srv *httptest.Server) *github.Client {
	t.Helper()
	c, err := github.New(github.Config{Token: "test-token", BaseURL: srv.URL})
	if err != nil {
		t.Fatalf("github.New: %v", err)
	}
	return c
}

// TestFetchPRForBranch_ChangesRequested_ClearedByApproval verifies that when
// reviewDecision is APPROVED, ChangesRequested is false — even if an older
// CHANGES_REQUESTED review exists. The old loop-based approach broke here
// because it stopped at the first CHANGES_REQUESTED node (reviews are
// oldest-first) without checking later reviews from the same author.
func TestFetchPRForBranch_ChangesRequested_ClearedByApproval(t *testing.T) {
	const body = `{
		"data": {
			"repository": {
				"pullRequests": {
					"nodes": [{
						"number": 10,
						"title": "My PR",
						"url": "https://github.com/org/repo/pull/10",
						"reviewDecision": "APPROVED",
						"commits": {"nodes": []}
					}]
				}
			}
		}
	}`

	srv := prCannedServer(t, body)
	c := newPRTestClient(t, srv)

	info, err := c.FetchPRForBranch(context.Background(), "org", "repo", "my-branch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil PRStatusInfo")
	}
	if info.ChangesRequested {
		t.Error("ChangesRequested should be false when reviewDecision is APPROVED")
	}
}

// TestFetchPRForBranch_ChangesRequested_True verifies that when reviewDecision
// is CHANGES_REQUESTED, ChangesRequested is true.
func TestFetchPRForBranch_ChangesRequested_True(t *testing.T) {
	const body = `{
		"data": {
			"repository": {
				"pullRequests": {
					"nodes": [{
						"number": 11,
						"title": "Needs work",
						"url": "https://github.com/org/repo/pull/11",
						"reviewDecision": "CHANGES_REQUESTED",
						"commits": {"nodes": []}
					}]
				}
			}
		}
	}`

	srv := prCannedServer(t, body)
	c := newPRTestClient(t, srv)

	info, err := c.FetchPRForBranch(context.Background(), "org", "repo", "needs-work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil PRStatusInfo")
	}
	if !info.ChangesRequested {
		t.Error("ChangesRequested should be true when reviewDecision is CHANGES_REQUESTED")
	}
}

// TestFetchPRForBranch_NoPR verifies that nil, nil is returned when the branch
// has no open pull request.
func TestFetchPRForBranch_NoPR(t *testing.T) {
	const body = `{"data": {"repository": {"pullRequests": {"nodes": []}}}}`

	srv := prCannedServer(t, body)
	c := newPRTestClient(t, srv)

	info, err := c.FetchPRForBranch(context.Background(), "org", "repo", "no-pr-branch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info != nil {
		t.Errorf("expected nil PRStatusInfo for branch with no PR, got %+v", info)
	}
}

// TestFetchPRForBranch_CheckRunAndStatusContext verifies that CheckRun and
// StatusContext nodes from the statusCheckRollup are both surfaced in Checks.
func TestFetchPRForBranch_CheckRunAndStatusContext(t *testing.T) {
	const body = `{
		"data": {
			"repository": {
				"pullRequests": {
					"nodes": [{
						"number": 12,
						"title": "CI PR",
						"url": "https://github.com/org/repo/pull/12",
						"reviewDecision": "APPROVED",
						"commits": {
							"nodes": [{
								"commit": {
									"statusCheckRollup": {
										"state": "SUCCESS",
										"contexts": {
											"nodes": [
												{
													"__typename": "CheckRun",
													"name": "build-and-test",
													"status": "COMPLETED",
													"conclusion": "SUCCESS"
												},
												{
													"__typename": "StatusContext",
													"context": "ci/circleci: lint",
													"state": "SUCCESS"
												}
											]
										}
									}
								}
							}]
						}
					}]
				}
			}
		}
	}`

	srv := prCannedServer(t, body)
	c := newPRTestClient(t, srv)

	info, err := c.FetchPRForBranch(context.Background(), "org", "repo", "ci-branch")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if info == nil {
		t.Fatal("expected non-nil PRStatusInfo")
	}
	if info.OverallCheckState != "SUCCESS" {
		t.Errorf("OverallCheckState: got %q, want %q", info.OverallCheckState, "SUCCESS")
	}
	if len(info.Checks) != 2 {
		t.Fatalf("expected 2 checks, got %d: %+v", len(info.Checks), info.Checks)
	}

	cr := info.Checks[0]
	if cr.Name != "build-and-test" || cr.Status != "COMPLETED" || cr.Conclusion != "SUCCESS" {
		t.Errorf("CheckRun check: got %+v", cr)
	}

	sc := info.Checks[1]
	if sc.Name != "ci/circleci: lint" || sc.Conclusion != "SUCCESS" {
		t.Errorf("StatusContext check: got %+v", sc)
	}
}
