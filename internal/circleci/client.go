package circleci

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/CircleCI-Public/chunk-cli/httpcl"
)

type Client struct {
	cl *httpcl.Client
}

func NewClient() (*Client, error) {
	token := os.Getenv("CIRCLE_TOKEN")
	if token == "" {
		token = os.Getenv("CIRCLECI_TOKEN")
	}
	if token == "" {
		return nil, fmt.Errorf("CIRCLE_TOKEN or CIRCLECI_TOKEN environment variable is required")
	}

	baseURL := os.Getenv("CIRCLECI_BASE_URL")
	if baseURL == "" {
		baseURL = "https://circleci.com"
	}

	cl := httpcl.New(httpcl.Config{
		BaseURL:    baseURL,
		AuthToken:  token,
		AuthHeader: "Circle-Token",
	})

	return &Client{cl: cl}, nil
}

func (c *Client) ListSandboxes(ctx context.Context, orgID string) ([]Sandbox, error) {
	var resp listSandboxesResponse
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodGet, "/api/v2/sandboxes",
		httpcl.QueryParam("org_id", orgID),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, fmt.Errorf("list sandboxes: %w", err)
	}
	return resp.Sandboxes, nil
}

func (c *Client) CreateSandbox(ctx context.Context, orgID, name, image string) (*Sandbox, error) {
	body := CreateSandboxRequest{
		OrganizationID: orgID,
		Name:           name,
		Image:          image,
	}
	var resp Sandbox
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodPost, "/api/v2/sandboxes",
		httpcl.Body(body),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, fmt.Errorf("create sandbox: %w", err)
	}
	return &resp, nil
}

func (c *Client) CreateAccessToken(ctx context.Context, sandboxID string) (string, error) {
	var resp AccessTokenResponse
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v2/sandboxes/%s/access_token", sandboxID),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return "", fmt.Errorf("create access token: %w", err)
	}
	return resp.AccessToken, nil
}

// AddSshKey calls the add-key endpoint using Bearer auth with the given token.
func (c *Client) AddSshKey(ctx context.Context, bearerToken, publicKey string) (*AddSshKeyResponse, error) {
	var resp AddSshKeyResponse
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodPost, "/api/v2/sandboxes/ssh/add-key",
		httpcl.Body(AddSshKeyRequest{PublicKey: publicKey}),
		httpcl.Header("Authorization", "Bearer "+bearerToken),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, fmt.Errorf("add ssh key: %w", err)
	}
	return &resp, nil
}

// Exec calls the exec endpoint using Bearer auth with the given token.
func (c *Client) Exec(ctx context.Context, bearerToken, sandboxID, command string, args []string) (*ExecResponse, error) {
	var resp ExecResponse
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodPost, "/api/v2/sandboxes/exec",
		httpcl.Body(ExecRequest{
			SandboxID: sandboxID,
			Command:   command,
			Args:      args,
		}),
		httpcl.Header("Authorization", "Bearer "+bearerToken),
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
