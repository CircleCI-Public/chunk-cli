package github

import (
	"context"
	"time"
)

// PRStatusInfo is the daemon's view of one open PR, as returned by FetchPRForBranch.
type PRStatusInfo struct {
	Number            int             `json:"number"`
	Title             string          `json:"title"`
	URL               string          `json:"url"`
	UpdatedAt         time.Time       `json:"updated_at,omitempty"`
	OverallCheckState string          `json:"overall_check_state,omitempty"` // SUCCESS, FAILURE, PENDING, etc.
	Checks            []PRCheckStatus `json:"checks,omitempty"`
	Comments          []PRCommentInfo `json:"comments,omitempty"`
	ChangesRequested  bool            `json:"changes_requested,omitempty"`
}

// PRCheckStatus describes one CI check on a PR commit.
type PRCheckStatus struct {
	Name       string `json:"name"`
	Status     string `json:"status"`     // QUEUED, IN_PROGRESS, COMPLETED
	Conclusion string `json:"conclusion"` // SUCCESS, FAILURE, NEUTRAL, CANCELLED, etc.
}

// PRCommentInfo is one review thread comment on a PR.
type PRCommentInfo struct {
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	Resolved  bool      `json:"resolved,omitempty"`
}

// branchPRData is the decoded response for branchPRQuery.
type branchPRData struct {
	Repository *struct {
		PullRequests struct {
			Nodes []branchPRNode `json:"nodes"`
		} `json:"pullRequests"`
	} `json:"repository"`
}

type branchPRNode struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	UpdatedAt string `json:"updatedAt"`
	Reviews   struct {
		Nodes []struct {
			State string `json:"state"`
		} `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads struct {
		Nodes []struct {
			IsResolved bool `json:"isResolved"`
			Comments   struct {
				Nodes []struct {
					Author    *Author `json:"author"`
					Body      string  `json:"body"`
					CreatedAt string  `json:"createdAt"`
				} `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	Commits struct {
		Nodes []struct {
			Commit struct {
				StatusCheckRollup *struct {
					State    string `json:"state"`
					Contexts struct {
						Nodes []checkContextNode `json:"nodes"`
					} `json:"contexts"`
				} `json:"statusCheckRollup"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// checkContextNode maps both CheckRun and StatusContext union members.
// Fields from the non-matching type are empty strings.
type checkContextNode struct {
	Typename string `json:"__typename"`
	// CheckRun fields
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	// StatusContext fields
	Context string `json:"context"`
	State   string `json:"state"`
}

// FetchPRForBranch returns the open PR for the given branch, or nil if none exists.
// Returns nil, nil when the repo has no open PR for that branch.
func (c *Client) FetchPRForBranch(ctx context.Context, owner, repo, branch string) (*PRStatusInfo, error) {
	var resp graphQLResponse[branchPRData]
	vars := map[string]any{"owner": owner, "repo": repo, "branch": branch}
	if err := c.doWithRetry(ctx, branchPRQuery, vars, &resp); err != nil {
		return nil, err
	}
	if hasResolutionError(resp.Errors) {
		return nil, nil // repo not found or not accessible — not fatal for monitoring
	}
	if resp.Data == nil || resp.Data.Repository == nil {
		return nil, nil
	}
	nodes := resp.Data.Repository.PullRequests.Nodes
	if len(nodes) == 0 {
		return nil, nil
	}
	pr := nodes[0]

	info := &PRStatusInfo{
		Number: pr.Number,
		Title:  pr.Title,
		URL:    pr.URL,
	}
	if t, err := time.Parse(time.RFC3339, pr.UpdatedAt); err == nil {
		info.UpdatedAt = t
	}

	// Populate check runs from the last commit's status rollup.
	if len(pr.Commits.Nodes) > 0 {
		rollup := pr.Commits.Nodes[0].Commit.StatusCheckRollup
		if rollup != nil {
			info.OverallCheckState = rollup.State
			for _, node := range rollup.Contexts.Nodes {
				switch node.Typename {
				case "CheckRun":
					info.Checks = append(info.Checks, PRCheckStatus{
						Name:       node.Name,
						Status:     node.Status,
						Conclusion: node.Conclusion,
					})
				case "StatusContext":
					info.Checks = append(info.Checks, PRCheckStatus{
						Name:       node.Context,
						Status:     "COMPLETED",
						Conclusion: node.State,
					})
				}
			}
		}
	}

	// Populate review thread comments (one per thread, the first/opening comment).
	for _, thread := range pr.ReviewThreads.Nodes {
		if len(thread.Comments.Nodes) == 0 {
			continue
		}
		c := thread.Comments.Nodes[0]
		author := ""
		if c.Author != nil {
			author = c.Author.Login
		}
		var ts time.Time
		if t, err := time.Parse(time.RFC3339, c.CreatedAt); err == nil {
			ts = t
		}
		info.Comments = append(info.Comments, PRCommentInfo{
			Author:    author,
			Body:      c.Body,
			CreatedAt: ts,
			Resolved:  thread.IsResolved,
		})
	}

	// Flag CHANGES_REQUESTED.
	for _, r := range pr.Reviews.Nodes {
		if r.State == "CHANGES_REQUESTED" {
			info.ChangesRequested = true
			break
		}
	}

	return info, nil
}
