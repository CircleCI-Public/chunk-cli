package github

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

var botPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\[bot\]$`),
	regexp.MustCompile(`(?i)-bot$`),
	regexp.MustCompile(`(?i)^circleci-app$`),
	regexp.MustCompile(`(?i)^wiz-inc-`),
	regexp.MustCompile(`(?i)^github-actions$`),
	regexp.MustCompile(`(?i)^dependabot$`),
	regexp.MustCompile(`(?i)^renovate$`),
	regexp.MustCompile(`(?i)^codecov$`),
	regexp.MustCompile(`(?i)^sonarcloud$`),
}

func isBot(login string) bool {
	for _, p := range botPatterns {
		if p.MatchString(login) {
			return true
		}
	}
	return false
}

// UserActivity tracks review activity counts for a single user.
type UserActivity struct {
	Login            string
	TotalActivity    int
	ReviewsGiven     int
	Approvals        int
	ChangesRequested int
	ReviewComments   int
	ReposActiveIn    map[string]bool
}

// ReviewCommentDetail is an enriched comment with PR metadata.
type ReviewCommentDetail struct {
	Reviewer  string                `json:"reviewer"`
	Body      string                `json:"body"`
	DiffHunk  string                `json:"diffHunk"`
	CreatedAt string                `json:"createdAt"`
	PR        ReviewCommentDetailPR `json:"pr"`
}

// ReviewCommentDetailPR holds PR metadata for a comment.
type ReviewCommentDetailPR struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	Author string `json:"author"`
	URL    string `json:"url"`
	State  string `json:"state"`
}

// FetchReviewActivityResult holds the results of fetching review activity.
type FetchReviewActivityResult struct {
	Activity map[string]*UserActivity
	Details  []ReviewCommentDetail
}

// FetchReviewActivity fetches review activity for a single repo.
// Returns an error containing "Could not resolve" for repos that don't exist.
func (c *Client) FetchReviewActivity(ctx context.Context, org, repo string, since time.Time) (*FetchReviewActivityResult, error) {
	activityMap := map[string]*UserActivity{}
	var details []ReviewCommentDetail
	var cursor *string

	for {
		vars := map[string]any{"org": org, "repo": repo}
		if cursor != nil {
			vars["cursor"] = *cursor
		}

		var resp graphQLResponse[RepoPRsData]
		if err := c.doWithRetry(ctx, reviewActivityQuery, vars, &resp); err != nil {
			return nil, err
		}
		if hasResolutionError(resp.Errors) {
			return nil, &repoResolutionError{org: org, repo: repo}
		}
		if resp.Data == nil || resp.Data.Repository == nil {
			break
		}

		prData := resp.Data.Repository.PullRequests

		for _, pr := range prData.Nodes {
			// Check if PR is older than since
			prUpdated, _ := time.Parse(time.RFC3339, pr.UpdatedAt)
			if !since.IsZero() && prUpdated.Before(since) {
				return &FetchReviewActivityResult{Activity: activityMap, Details: details}, nil
			}

			processPR(pr, since, repo, activityMap, &details)
		}

		if !prData.PageInfo.HasNextPage {
			break
		}
		cursor = prData.PageInfo.EndCursor

		// Throttle if rate limit is low
		if err := c.waitForRateLimit(ctx, resp.Data.RateLimit); err != nil {
			return nil, err
		}
	}

	return &FetchReviewActivityResult{Activity: activityMap, Details: details}, nil
}

type repoResolutionError struct {
	org, repo string
}

func (e *repoResolutionError) Error() string {
	return "Could not resolve to a Repository with the name '" + e.org + "/" + e.repo + "'."
}

// IsResolutionError checks if an error is a repo resolution error.
func IsResolutionError(err error) bool {
	if err == nil {
		return false
	}
	var resErr *repoResolutionError
	if errors.As(err, &resErr) {
		return true
	}
	return strings.Contains(err.Error(), "Could not resolve")
}

func processPR(pr PRNode, since time.Time, repoName string, activityMap map[string]*UserActivity, details *[]ReviewCommentDetail) {
	prAuthor := ""
	if pr.Author != nil {
		prAuthor = strings.ToLower(pr.Author.Login)
	}

	// Process reviews
	for _, review := range pr.Reviews.Nodes {
		if review.Author == nil {
			continue
		}
		login := review.Author.Login
		if isBot(login) {
			continue
		}
		if prAuthor != "" && strings.EqualFold(login, prAuthor) {
			continue
		}
		if !since.IsZero() {
			reviewDate, _ := time.Parse(time.RFC3339, review.CreatedAt)
			if reviewDate.Before(since) {
				continue
			}
		}

		activity := getOrCreate(activityMap, login)
		activity.ReposActiveIn[repoName] = true

		switch review.State {
		case "APPROVED":
			activity.Approvals++
			activity.ReviewsGiven++
		case "CHANGES_REQUESTED":
			activity.ChangesRequested++
			activity.ReviewsGiven++
		case "COMMENTED":
			activity.ReviewsGiven++
		}
	}

	// Process review thread comments
	for _, thread := range pr.ReviewThreads.Nodes {
		for _, comment := range thread.Comments.Nodes {
			if comment.Author == nil {
				continue
			}
			login := comment.Author.Login
			if isBot(login) {
				continue
			}
			if prAuthor != "" && strings.EqualFold(login, prAuthor) {
				continue
			}
			if !since.IsZero() {
				commentDate, _ := time.Parse(time.RFC3339, comment.CreatedAt)
				if commentDate.Before(since) {
					continue
				}
			}

			activity := getOrCreate(activityMap, login)
			activity.ReposActiveIn[repoName] = true
			activity.ReviewComments++
			activity.TotalActivity++

			author := "unknown"
			if pr.Author != nil {
				author = pr.Author.Login
			}

			*details = append(*details, ReviewCommentDetail{
				Reviewer:  login,
				Body:      comment.Body,
				DiffHunk:  comment.DiffHunk,
				CreatedAt: comment.CreatedAt,
				PR: ReviewCommentDetailPR{
					Repo:   repoName,
					Number: pr.Number,
					Title:  pr.Title,
					Author: author,
					URL:    pr.URL,
					State:  pr.State,
				},
			})
		}
	}
}

func getOrCreate(m map[string]*UserActivity, login string) *UserActivity {
	a, ok := m[login]
	if !ok {
		a = &UserActivity{
			Login:         login,
			ReposActiveIn: map[string]bool{},
		}
		m[login] = a
	}
	return a
}
