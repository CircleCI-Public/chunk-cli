package circleci

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/CircleCI-Public/chunk-cli/internal/httpcl"
)

// ErrTokenNotFound indicates no CircleCI token was found in env or config.
var ErrTokenNotFound = errors.New("api token not found")

// ErrNotAuthorized indicates the request was rejected (401/403).
var ErrNotAuthorized = errors.New("not authorized")

// StatusError represents an HTTP error from the CircleCI API without exposing httpcl internals.
type StatusError struct {
	Op         string
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: %d %s", e.Op, e.StatusCode, http.StatusText(e.StatusCode))
}

func mapErr(op string, err error) error {
	var he *httpcl.HTTPError
	if !errors.As(err, &he) {
		return fmt.Errorf("%s: %w", op, err)
	}
	if he.StatusCode == http.StatusUnauthorized || he.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%s: %w", op, ErrNotAuthorized)
	}
	return &StatusError{Op: op, StatusCode: he.StatusCode}
}

type Config struct {
	Token   string
	BaseURL string
}

type Client struct {
	cl *httpcl.Client
}

func NewClient(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, ErrTokenNotFound
	}
	cl := httpcl.New(httpcl.Config{
		BaseURL:    cfg.BaseURL,
		AuthToken:  cfg.Token,
		AuthHeader: "Circle-Token",
	})
	return &Client{cl: cl}, nil
}

// GetCurrentUser calls GET /api/v2/me to validate the token.
func (c *Client) GetCurrentUser(ctx context.Context) error {
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodGet, "/api/v2/me"))
	if err != nil {
		return mapErr("get current user", err)
	}
	return nil
}

func (c *Client) ListSandboxes(ctx context.Context, orgID string) ([]Sandbox, error) {
	var resp listSandboxesResponse
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodGet, "/api/v2/sandbox/instances",
		httpcl.QueryParam("org_id", orgID),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("list sandboxes", err)
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
		return nil, mapErr("create sandbox", err)
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
		return nil, mapErr("add ssh key", err)
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
		return nil, mapErr("exec", err)
	}
	return &resp, nil
}

func (c *Client) CreateSnapshot(ctx context.Context, sandboxID, name string) (*Snapshot, error) {
	var resp Snapshot
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodPost, "/api/v2/sandbox/snapshots",
		httpcl.Body(CreateSnapshotRequest{SandboxID: sandboxID, Name: name}),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("create snapshot", err)
	}
	return &resp, nil
}

func (c *Client) GetSnapshot(ctx context.Context, id string) (*Snapshot, error) {
	var resp Snapshot
	_, err := c.cl.Call(ctx, httpcl.NewRequest(http.MethodGet,
		fmt.Sprintf("/api/v2/sandbox/snapshots/%s", id),
		httpcl.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("get snapshot", err)
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
		return nil, mapErr("trigger run", err)
	}
	return &resp, nil
}
