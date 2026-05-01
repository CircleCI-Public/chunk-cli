package github

import (
	"context"
	"fmt"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

// CreateCommitStatus posts a commit status for the given SHA.
// state must be one of: "pending", "success", "failure", "error".
// statusContext is the check name shown in GitHub (e.g. "chunk/test").
func (c *Client) CreateCommitStatus(ctx context.Context, owner, repo, sha, state, statusContext, description string) error {
	body := map[string]string{
		"state":       state,
		"context":     statusContext,
		"description": description,
	}
	req := hc.NewRequest("POST", "/repos/%s/%s/statuses/%s",
		hc.RouteParams(owner, repo, sha),
		hc.Body(body),
	)
	_, err := c.http.Call(ctx, req)
	if err == nil {
		return nil
	}
	return mapErr(fmt.Sprintf("create commit status %s", statusContext), err)
}
