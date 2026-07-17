package circleci

import (
	"context"
	"net/http"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

// OrgInfo is the response from POST /api/v2/organization.
type OrgInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	VcsType string `json:"vcs_type"`
}

// CreateOrg creates a new standalone CircleCI organization.
func (c *Client) CreateOrg(ctx context.Context, name string) (*OrgInfo, error) {
	var org OrgInfo
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v2/organization",
		hc.Body(map[string]string{"name": name, "vcs_type": "circleci"}),
		hc.JSONDecoder(&org),
	))
	if err != nil {
		return nil, mapErr("create org", err)
	}
	return &org, nil
}
