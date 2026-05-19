package github

import (
	"testing"
	"time"
)

// TestProcessPR_ExcludesAuthorSelfReview verifies that review activity and
// comment details contributed by the PR author themselves are not counted.
// MUT-011 caught this gap by inverting the prAuthor check so that author
// comments were always included.
func TestProcessPR_ExcludesAuthorSelfReview(t *testing.T) {
	pr := PRNode{
		Number:    42,
		Title:     "My PR",
		URL:       "https://github.com/org/repo/pull/42",
		State:     "OPEN",
		UpdatedAt: "2026-01-01T00:00:00Z",
		Author:    &Author{Login: "alice"},
		Reviews: struct {
			Nodes []ReviewNode `json:"nodes"`
		}{
			Nodes: []ReviewNode{
				// alice reviews her own PR — should be excluded
				{Author: &Author{Login: "alice"}, State: "APPROVED", CreatedAt: "2026-01-01T00:00:00Z"},
				// bob is a genuine reviewer — should be included
				{Author: &Author{Login: "bob"}, State: "CHANGES_REQUESTED", CreatedAt: "2026-01-01T00:00:00Z"},
			},
		},
		ReviewThreads: struct {
			Nodes []struct {
				Comments struct {
					Nodes []CommentNode `json:"nodes"`
				} `json:"comments"`
			} `json:"nodes"`
		}{
			Nodes: []struct {
				Comments struct {
					Nodes []CommentNode `json:"nodes"`
				} `json:"comments"`
			}{
				{Comments: struct {
					Nodes []CommentNode `json:"nodes"`
				}{Nodes: []CommentNode{
					// alice comments on her own PR — should be excluded
					{Author: &Author{Login: "alice"}, Body: "self-comment", DiffHunk: "@@", CreatedAt: "2026-01-01T00:00:00Z"},
					// bob comments — should be included
					{Author: &Author{Login: "bob"}, Body: "nit: rename this", DiffHunk: "@@", CreatedAt: "2026-01-01T00:00:00Z"},
				}}},
			},
		},
	}

	activityMap := map[string]*UserActivity{}
	var details []ReviewCommentDetail
	processPR(pr, time.Time{}, "my-repo", activityMap, &details)

	if _, ok := activityMap["alice"]; ok {
		t.Error("PR author alice should not appear in reviewer activity")
	}
	bob, ok := activityMap["bob"]
	if !ok {
		t.Fatal("expected bob to appear in reviewer activity")
	}
	if bob.ChangesRequested != 1 {
		t.Errorf("bob.ChangesRequested: got %d, want 1", bob.ChangesRequested)
	}
	if bob.ReviewComments != 1 {
		t.Errorf("bob.ReviewComments: got %d, want 1", bob.ReviewComments)
	}

	for _, d := range details {
		if d.Reviewer == "alice" {
			t.Error("PR author alice should not appear in review details")
		}
	}
	if len(details) != 1 {
		t.Errorf("expected 1 detail entry (bob), got %d", len(details))
	}
}

// TestProcessPR_ExcludesAuthorSelfReview_CaseInsensitive verifies that the
// author check is case-insensitive: if the PR author is "Alice" and the
// reviewer login is "alice", the review is still excluded.
func TestProcessPR_ExcludesAuthorSelfReview_CaseInsensitive(t *testing.T) {
	pr := PRNode{
		Number:    1,
		UpdatedAt: "2026-01-01T00:00:00Z",
		Author:    &Author{Login: "Alice"}, // mixed-case author
		Reviews: struct {
			Nodes []ReviewNode `json:"nodes"`
		}{
			Nodes: []ReviewNode{
				{Author: &Author{Login: "alice"}, State: "APPROVED", CreatedAt: "2026-01-01T00:00:00Z"},
			},
		},
		ReviewThreads: struct {
			Nodes []struct {
				Comments struct {
					Nodes []CommentNode `json:"nodes"`
				} `json:"comments"`
			} `json:"nodes"`
		}{},
	}

	activityMap := map[string]*UserActivity{}
	var details []ReviewCommentDetail
	processPR(pr, time.Time{}, "repo", activityMap, &details)

	if _, ok := activityMap["alice"]; ok {
		t.Error("lowercase 'alice' should be excluded when PR author is 'Alice'")
	}
}

func TestIsBot(t *testing.T) {
	tests := []struct {
		login string
		want  bool
	}{
		{"dependabot[bot]", true},
		{"renovate-bot", true},
		{"circleci-app", true},
		{"wiz-inc-scanner", true},
		{"github-actions", true},
		{"dependabot", true},
		{"renovate", true},
		{"codecov", true},
		{"sonarcloud", true},
		{"alice", false},
		{"bob-builder", false},
		{"my-bot-account", false}, // no trailing -bot
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.login, func(t *testing.T) {
			if got := isBot(tt.login); got != tt.want {
				t.Fatalf("isBot(%q) = %v, want %v", tt.login, got, tt.want)
			}
		})
	}
}
