package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/github"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func TestPRStatus(t *testing.T) {
	tests := []struct {
		name string
		info github.PRProgress
		want string
	}{
		{"merged", github.PRProgress{State: "MERGED", ChangesRequested: true}, chunkd.PRStatusMerged},
		{"closed", github.PRProgress{State: "CLOSED"}, chunkd.PRStatusClosed},
		{"nobody has looked", github.PRProgress{State: "OPEN"}, chunkd.PRStatusUp},
		{"a draft with a reviewer asked is still up", github.PRProgress{State: "OPEN", Draft: true, ReviewRequested: true}, chunkd.PRStatusUp},
		{"review requested", github.PRProgress{State: "OPEN", ReviewRequested: true}, chunkd.PRStatusInReview},
		{"reviewed", github.PRProgress{State: "OPEN", Reviewed: true}, chunkd.PRStatusInReview},
		{"changes requested", github.PRProgress{State: "OPEN", Reviewed: true, ChangesRequested: true}, chunkd.PRStatusFeedback},
		{"unresolved thread", github.PRProgress{State: "OPEN", OpenThreads: 1}, chunkd.PRStatusFeedback},
		{"failing checks", github.PRProgress{State: "OPEN", CheckState: "FAILURE"}, chunkd.PRStatusChecksFailing},
		{"feedback outranks failing checks", github.PRProgress{State: "OPEN", CheckState: "FAILURE", OpenThreads: 2}, chunkd.PRStatusFeedback},
		{"pending checks do not matter", github.PRProgress{State: "OPEN", CheckState: "PENDING"}, chunkd.PRStatusUp},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, prStatus(&tt.info), tt.want)
		})
	}
}

func TestOwnerRepoOf(t *testing.T) {
	owner, repo, ok := ownerRepoOf("https://github.com/acme/widgets/pull/7")
	assert.Assert(t, ok)
	assert.Equal(t, owner+"/"+repo, "acme/widgets")

	// An enterprise host with a path prefix still ends the same way.
	owner, repo, ok = ownerRepoOf("https://ghe.example.com/git/acme/widgets/pull/7")
	assert.Assert(t, ok)
	assert.Equal(t, owner+"/"+repo, "acme/widgets")

	for _, bad := range []string{"", "https://github.com/acme/widgets", "https://github.com/acme/widgets/issues/7", "https://github.com/acme/widgets/pull/x"} {
		_, _, ok = ownerRepoOf(bad)
		assert.Assert(t, !ok, "%q", bad)
	}
}

// prSession adds a session whose pull request is open as #7.
func prSession(t *testing.T, store *sessionStore, root string) *sessionEntry {
	t.Helper()
	e, _, busy := store.add(chunkd.Session{ProjectRoot: root, Factory: &chunkd.FactoryRun{
		PullRequest: &chunkd.PullRequest{State: chunkd.PROpened, Number: 7, URL: "https://github.com/acme/widgets/pull/7"},
	}})
	assert.Equal(t, busy, "")
	return e
}

func prStatusOf(e *sessionEntry) string {
	return e.snapshot().Factory.PullRequest.Status
}

func TestPRProgressFollowsARunsPullRequestUntilItSettles(t *testing.T) {
	gh := fakes.NewFakeGitHub()
	srv := httptest.NewServer(gh)
	t.Cleanup(srv.Close)
	client, err := github.New(github.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	store := newSessionStore(context.Background())
	e := prSession(t, store, "/repo")
	p := newPRProgress(client)

	// refresh runs the lookup in the background, so wait for what it records.
	waitStatus := func(want string) {
		t.Helper()
		assert.Assert(t, func() bool {
			for range 200 {
				if prStatusOf(e) == want {
					return true
				}
				time.Sleep(10 * time.Millisecond)
			}
			return false
		}(), "status is %q, want %q", prStatusOf(e), want)
	}

	p.refresh(context.Background(), store)
	waitStatus(chunkd.PRStatusUp)

	// Asked again inside the interval, it does not go back to GitHub.
	gh.SetPullRequestProgress(7, `{"state":"MERGED","reviewRequests":{"totalCount":0},"reviews":{"totalCount":0,"nodes":[]},"reviewThreads":{"nodes":[]},"commits":{"nodes":[]}}`)
	p.refresh(context.Background(), store)
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, prStatusOf(e), chunkd.PRStatusUp)

	// Once the interval has passed it sees the merge, and then stops asking.
	p.mu.Lock()
	p.lastFetch[e] = time.Now().Add(-2 * PRPollInterval)
	p.mu.Unlock()
	p.refresh(context.Background(), store)
	waitStatus(chunkd.PRStatusMerged)
	_, _, _, ok := e.trackedPullRequest(time.Now())
	assert.Assert(t, !ok, "a merged pull request is not followed")
}

func TestPRProgressIgnoresWhatIsNotWorthAsking(t *testing.T) {
	store := newSessionStore(context.Background())
	e := prSession(t, store, "/repo")
	_, _, number, ok := e.trackedPullRequest(time.Now())
	assert.Assert(t, ok)
	assert.Equal(t, number, 7)

	// A run that ended long ago is history.
	ended := time.Now().Add(-2 * prTrackFor)
	e.mu.Lock()
	e.s.EndedAt = &ended
	e.mu.Unlock()
	_, _, _, ok = e.trackedPullRequest(time.Now())
	assert.Assert(t, !ok)

	// So is a run with no pull request to follow.
	other, _, _ := store.add(chunkd.Session{ProjectRoot: "/other", Factory: &chunkd.FactoryRun{
		PullRequest: &chunkd.PullRequest{State: chunkd.PRSkipped, Message: "no GitHub token"},
	}})
	_, _, _, ok = other.trackedPullRequest(time.Now())
	assert.Assert(t, !ok)

	// With no GitHub client the watcher does nothing and does not panic.
	newPRProgress(nil).refresh(context.Background(), store)
	var none *prProgress
	none.refresh(context.Background(), store)
}
