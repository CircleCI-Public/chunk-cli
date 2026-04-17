package circleci

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

type Client struct {
	cl *httpcl.Client
}

// NewClient resolves a CircleCI token via config (env vars then config file)
// and returns a ready-to-use client.
func NewClient() (*Client, error) {
	rc, err := config.Resolve("", "")
	if err != nil {
		return nil, fmt.Errorf("resolve config: %w", err)
	}
	if rc.CircleCIToken == "" {
		return nil, fmt.Errorf("circleci token not found: set CIRCLE_TOKEN or run 'chunk auth set circleci'")
	}

	baseURL := os.Getenv("CIRCLECI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://circleci.com"
	}

	cl := httpcl.New(httpcl.Config{
		BaseURL:    baseURL,
		AuthToken:  rc.CircleCIToken,
		AuthHeader: "Circle-Token",
	})

	return &Client{cl: cl}, nil
}

// GetCurrentUser calls GET /api/v2/me to validate the token.
func (c *Client) GetCurrentUser(ctx context.Context) error {
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodGet, "/api/v2/me"))
	return err
}

func (c *Client) ListSandboxes(ctx context.Context, orgID string) ([]Sandbox, error) {
	var resp listSandboxesResponse
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodGet, "/api/v2/sandbox/instances",
		httpcl.QueryParam("org_id", orgID),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, fmt.Errorf("list sandboxes: %w", err)
	}
	return resp.Items, nil
}

func (c *Client) CreateSandbox(ctx context.Context, orgID, name, provider, image string) (*Sandbox, error) {
	body := CreateSandboxRequest{
		OrgID:    orgID,
		Name:     name,
		Provider: provider,
		Image:    image,
	}
	var resp Sandbox
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodPost, "/api/v2/sandbox/instances",
		httpcl.Body(body),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	return &resp, nil
}

func (c *Client) AddSSHKey(ctx context.Context, sandboxID, publicKey string) (*AddSSHKeyResponse, error) {
	var resp AddSSHKeyResponse
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v2/sandbox/instances/%s/ssh/add-key", sandboxID),
		httpcl.Body(AddSSHKeyRequest{PublicKey: publicKey}),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, fmt.Errorf("add ssh key: %w", err)
	}
	return &resp, nil
}

func (c *Client) Exec(ctx context.Context, sandboxID, command string, args []string) (*ExecResponse, error) {
	var resp ExecResponse
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v2/sandbox/instances/%s/exec", sandboxID),
		httpcl.Body(ExecRequest{
			Command: command,
			Args:    args,
		}),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, fmt.Errorf("exec: %w", err)
	}
	return &resp, nil
}

func (c *Client) TriggerRun(ctx context.Context, orgID, projectID string, body TriggerRunRequest) (*RunResponse, error) {
	var resp RunResponse
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v2/agents/org/%s/project/%s/runs", orgID, projectID),
		httpcl.Body(body),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, fmt.Errorf("trigger run: %w", err)
	}
	return &resp, nil
}
