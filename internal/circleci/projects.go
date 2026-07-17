package circleci

import (
	"context"
	"net/http"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

// FollowedProject represents a project returned by the v1.1 API.
type FollowedProject struct {
	Username string `json:"username"`
	Reponame string `json:"reponame"`
	VcsURL   string `json:"vcs_url"`
	VcsType  string `json:"vcs_type"`
}

// Collaboration represents an org the user belongs to.
type Collaboration struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	VcsType string `json:"vcs_type"`
}

// ProjectDetail represents detailed project info from the v2 API.
type ProjectDetail struct {
	ID    string `json:"id"`
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	OrgID string `json:"org_id"`
}

// ListFollowedProjects returns projects the user follows.
func (c *Client) ListFollowedProjects(ctx context.Context) ([]FollowedProject, error) {
	var resp []FollowedProject
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v1.1/projects",
		hc.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("list followed projects", err)
	}
	return resp, nil
}

// ListCollaborations returns organizations the user belongs to.
func (c *Client) ListCollaborations(ctx context.Context) ([]Collaboration, error) {
	var resp []Collaboration
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v2/me/collaborations",
		hc.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("list collaborations", err)
	}
	return resp, nil
}

// GetProjectBySlug fetches project details by slug (e.g. "gh/org/repo").
func (c *Client) GetProjectBySlug(ctx context.Context, slug string) (*ProjectDetail, error) {
	var resp ProjectDetail
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v2/project/%s",
		hc.RouteParams(slug),
		hc.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("get project by slug", err)
	}
	return &resp, nil
}

// OrgInfo is the response from POST /api/v2/organization.
type OrgInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Slug    string `json:"slug"`
	VcsType string `json:"vcs_type"`
}

// CreateOrg creates a new CircleCI organization. vcsType must be one of
// "circleci" (standalone), "github", or "bitbucket".
func (c *Client) CreateOrg(ctx context.Context, name, vcsType string) (*OrgInfo, error) {
	var org OrgInfo
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v2/organization",
		hc.Body(map[string]string{"name": name, "vcs_type": vcsType}),
		hc.JSONDecoder(&org),
	))
	if err != nil {
		return nil, mapErr("create org", err)
	}
	return &org, nil
}
