package circleci

import (
	"context"
	"fmt"
	"net/http"

	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
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
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodGet, "/api/v1.1/projects",
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("list followed projects", err)
	}
	return resp, nil
}

// ListCollaborations returns organizations the user belongs to.
func (c *Client) ListCollaborations(ctx context.Context) ([]Collaboration, error) {
	var resp []Collaboration
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodGet, "/api/v2/me/collaborations",
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("list collaborations", err)
	}
	return resp, nil
}

// GetProjectBySlug fetches project details by slug (e.g. "gh/org/repo").
func (c *Client) GetProjectBySlug(ctx context.Context, slug string) (*ProjectDetail, error) {
	var resp ProjectDetail
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v2/project/%s", slug),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("get project by slug", err)
	}
	return &resp, nil
}
