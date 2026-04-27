package circleci

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
	"github.com/CircleCI-Public/chunk-cli/internal/version"
)

// ErrTokenNotFound indicates no CircleCI token was found in env or config.
var ErrTokenNotFound = errors.New("api token not found")

// ErrNotAuthorized indicates the request was rejected (401/403).
var ErrNotAuthorized = errors.New("not authorized")

// StatusError is an alias for the shared httpcl.StatusError type.
type StatusError = hc.StatusError

type Config struct {
	Token   string
	BaseURL string
}

type Client struct {
	cl *hc.Client
}

func NewClient(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, ErrTokenNotFound
	}
	cl := hc.New(hc.Config{
		BaseURL:    cfg.BaseURL,
		AuthToken:  cfg.Token,
		AuthHeader: "Circle-Token",
		UserAgent:  version.UserAgent(),
	})
	return &Client{cl: cl}, nil
}

// GetCurrentUser calls GET /api/v2/me to validate the token.
func (c *Client) GetCurrentUser(ctx context.Context) error {
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v2/me"))
	if err != nil {
		return mapErr("get current user", err)
	}
	return nil
}

func (c *Client) ListSidecars(ctx context.Context, orgID string) ([]Sidecar, error) {
	var resp listSidecarsResponse
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v2/sidecar/instances",
		hc.QueryParam("org_id", orgID),
		hc.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("list sidecars", err)
	}
	return resp.Items, nil
}

func (c *Client) CreateSidecar(ctx context.Context, orgID, name, provider, image string) (*Sidecar, error) {
	body := CreateSidecarRequest{
		OrgID:    orgID,
		Name:     name,
		Provider: provider,
		Image:    image,
	}
	var resp Sidecar
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v2/sidecar/instances",
		hc.Body(body),
		hc.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("create sidecar", err)
	}
	return &resp, nil
}

func (c *Client) AddSSHKey(ctx context.Context, sidecarID, publicKey string) (*AddSSHKeyResponse, error) {
	var resp AddSSHKeyResponse
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v2/sidecar/instances/%s/ssh/add-key",
		hc.RouteParams(sidecarID),
		hc.Body(AddSSHKeyRequest{PublicKey: publicKey}),
		hc.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("add ssh key", err)
	}
	return &resp, nil
}

func (c *Client) Exec(ctx context.Context, sidecarID, command string, args []string) (*ExecResponse, error) {
	var resp ExecResponse
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v2/sidecar/instances/%s/exec",
		hc.RouteParams(sidecarID),
		hc.Body(ExecRequest{
			Command: command,
			Args:    args,
		}),
		hc.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("exec", err)
	}
	return &resp, nil
}

func (c *Client) CreateSnapshot(ctx context.Context, sidecarID, name string) (*Snapshot, error) {
	var resp Snapshot
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v2/sidecar/snapshots",
		hc.Body(CreateSnapshotRequest{SidecarID: sidecarID, Name: name}),
		hc.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("create snapshot", err)
	}
	return &resp, nil
}

func (c *Client) GetSnapshot(ctx context.Context, id string) (*Snapshot, error) {
	var resp Snapshot
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v2/sidecar/snapshots/%s",
		hc.RouteParams(id),
		hc.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("get snapshot", err)
	}
	return &resp, nil
}

func (c *Client) TriggerRun(ctx context.Context, orgID, projectID string, body TriggerRunRequest) (*RunResponse, error) {
	var resp RunResponse
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v2/agents/org/%s/project/%s/runs",
		hc.RouteParams(orgID, projectID),
		hc.Body(body),
		hc.JSONDecoder(&resp),
	))
	if err != nil {
		return nil, mapErr("trigger run", err)
	}
	return &resp, nil
}

func mapErr(op string, err error) error {
	var he *hc.HTTPError
	if !errors.As(err, &he) {
		return fmt.Errorf("%s: %w", op, err)
	}
	if he.StatusCode == http.StatusUnauthorized || he.StatusCode == http.StatusForbidden {
		return fmt.Errorf("%s: %w", op, ErrNotAuthorized)
	}
	return &StatusError{Op: op, StatusCode: he.StatusCode}
}
