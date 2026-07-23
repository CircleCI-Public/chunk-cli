package circleci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

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
	// OnWarn, when non-nil, is called with a plain-text deprecation warning.
	// See httpcl.Config.OnWarn for details.
	OnWarn func(msg string)
}

type Client struct {
	cl *hc.Client
}

func NewClient(cfg Config) (*Client, error) {
	if cfg.Token == "" {
		return nil, ErrTokenNotFound
	}
	cl := hc.New(hc.Config{
		BaseURL:          cfg.BaseURL,
		AuthToken:        cfg.Token,
		AuthHeader:       "Circle-Token",
		UserAgent:        version.UserAgent(),
		RetryOn429Budget: 30 * time.Second,
		OnWarn:           cfg.OnWarn,
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

// V3 wire types — mirrors backplane-go DataEntity/envelope pattern.

type v3Ref struct {
	ID string `json:"id"`
}

type v3DataEntity struct {
	Attributes any    `json:"attributes"`
	ID         string `json:"id,omitempty"`
	References any    `json:"references,omitempty"`
}

type v3Envelope struct {
	Data v3DataEntity `json:"data"`
}

type v3Collection struct {
	Data []v3DataEntity `json:"data"`
}

type sidecarAttrs struct {
	Name  string `json:"name"`
	Image string `json:"image,omitempty"`
}

type orgUserRefs struct {
	Org  v3Ref `json:"org"`
	User v3Ref `json:"user"`
}

type orgRefs struct {
	Org v3Ref `json:"org"`
}

func (c *Client) ListSidecars(ctx context.Context, orgID string, all bool) ([]Sidecar, error) {
	var coll v3Collection
	allVal := "false"
	if all {
		allVal = "true"
	}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/sidecar/instances",
		hc.QueryParam("org_id", orgID),
		hc.QueryParam("all", allVal),
		hc.JSONDecoder(&coll),
	))
	if err != nil {
		return nil, mapErr("list sidecars", err)
	}
	sidecars := make([]Sidecar, 0, len(coll.Data))
	for _, item := range coll.Data {
		sc := Sidecar{ID: item.ID}
		if attrs, ok := item.Attributes.(map[string]any); ok {
			if name, ok := attrs["name"].(string); ok {
				sc.Name = name
			}
			if image, ok := attrs["image"].(string); ok {
				sc.Image = image
			}
		}
		if refs, ok := item.References.(map[string]any); ok {
			if org, ok := refs["org"].(map[string]any); ok {
				if id, ok := org["id"].(string); ok {
					sc.OrgID = id
				}
			}
		}
		sidecars = append(sidecars, sc)
	}
	return sidecars, nil
}

func (c *Client) CreateSidecar(ctx context.Context, orgID, name, image string) (*Sidecar, error) {
	var attrs sidecarAttrs
	var refs orgUserRefs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs, References: &refs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/sidecar/instances",
		hc.Body(v3Envelope{Data: v3DataEntity{
			Attributes: sidecarAttrs{Name: name, Image: image},
			References: orgRefs{Org: v3Ref{ID: orgID}},
		}}),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("create sidecar", err)
	}
	return &Sidecar{
		ID:    env.Data.ID,
		Name:  attrs.Name,
		Image: attrs.Image,
		OrgID: refs.Org.ID,
	}, nil
}

func (c *Client) DeleteSidecar(ctx context.Context, sidecarID string) error {
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodDelete, "/api/v3/sidecar/instances/%s",
		hc.RouteParams(sidecarID),
	))
	if err != nil {
		return mapErr("delete sidecar", err)
	}
	return nil
}

type addKeyAttrs struct {
	URL string `json:"url"`
}

func (c *Client) AddSSHKey(ctx context.Context, sidecarID, publicKey string) (*AddSSHKeyResponse, error) {
	var attrs addKeyAttrs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/sidecar/instances/%s/ssh/add-key",
		hc.RouteParams(sidecarID),
		hc.Body(AddSSHKeyRequest{PublicKey: publicKey}),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("add ssh key", err)
	}
	return &AddSSHKeyResponse{URL: attrs.URL}, nil
}

type execAttrs struct {
	ExitCode int    `json:"exit_code"`
	PID      int    `json:"pid"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

func (c *Client) Exec(ctx context.Context, sidecarID, command string, args []string) (*ExecResponse, error) {
	var attrs execAttrs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/sidecar/instances/%s/exec",
		hc.RouteParams(sidecarID),
		hc.Body(ExecRequest{
			Command: command,
			Args:    args,
		}),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("exec", err)
	}
	return &ExecResponse{
		CommandID: env.Data.ID,
		PID:       attrs.PID,
		Stdout:    attrs.Stdout,
		Stderr:    attrs.Stderr,
		ExitCode:  attrs.ExitCode,
	}, nil
}

// ExecAsync starts a command on a sidecar asynchronously and returns its command ID.
// The caller should use StreamCommandOutput and GetCommand to observe progress and result.
func (c *Client) ExecAsync(ctx context.Context, sidecarID, command string, args []string, envVars map[string]string, workingDir string) (string, error) {
	var resp v3Envelope
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/sidecar/instances/%s/exec",
		hc.RouteParams(sidecarID),
		hc.Body(AsyncExecRequest{
			Command:    command,
			Args:       args,
			Env:        envVars,
			WorkingDir: workingDir,
		}),
		hc.JSONDecoder(&resp),
	))
	if err != nil {
		return "", mapErr("exec async", err)
	}
	return resp.Data.ID, nil
}

// StreamCommandOutput opens the JSONL output stream for a command starting at the given line offset.
// The caller must close the returned ReadCloser when done or on reconnect.
func (c *Client) StreamCommandOutput(ctx context.Context, commandID string, offset int) (io.ReadCloser, error) {
	body, err := c.cl.Stream(ctx, hc.NewRequest(http.MethodGet, "/api/v3/sidecar/commands/%s/output",
		hc.RouteParams(commandID),
		hc.QueryParam("offset", fmt.Sprintf("%d", offset)),
	))
	if err != nil {
		return nil, mapErr("stream command output", err)
	}
	return body, nil
}

type commandAttrs struct {
	CreatedAt string  `json:"created_at"`
	EndedAt   *string `json:"ended_at,omitempty"`
	ExitCode  *int    `json:"exit_code,omitempty"`
	Outcome   *string `json:"outcome,omitempty"`
	Phase     string  `json:"phase"`
}

type instanceRefs struct {
	SidecarInstance v3Ref `json:"sidecar_instance"`
}

func (c *Client) GetCommand(ctx context.Context, commandID string) (*Command, error) {
	var attrs commandAttrs
	var refs instanceRefs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs, References: &refs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/sidecar/commands/%s",
		hc.RouteParams(commandID),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("get command", err)
	}
	return &Command{
		ID:                env.Data.ID,
		CreatedAt:         attrs.CreatedAt,
		EndedAt:           attrs.EndedAt,
		ExitCode:          attrs.ExitCode,
		Outcome:           attrs.Outcome,
		Phase:             attrs.Phase,
		SidecarInstanceID: refs.SidecarInstance.ID,
	}, nil
}

type snapshotAttrs struct {
	Name string `json:"name"`
	Tag  string `json:"tag,omitempty"`
}

func (c *Client) CreateSnapshot(ctx context.Context, sidecarID, name string) (*Snapshot, error) {
	var attrs snapshotAttrs
	var refs orgRefs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs, References: &refs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/sidecar/snapshots",
		hc.Body(v3Envelope{Data: v3DataEntity{
			Attributes: snapshotAttrs{Name: name},
			References: instanceRefs{SidecarInstance: v3Ref{ID: sidecarID}},
		}}),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("create snapshot", err)
	}
	return &Snapshot{
		ID:    env.Data.ID,
		OrgID: refs.Org.ID,
		Name:  attrs.Name,
		Tag:   attrs.Tag,
	}, nil
}

func (c *Client) GetSnapshot(ctx context.Context, id string) (*Snapshot, error) {
	var attrs snapshotAttrs
	var refs orgRefs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs, References: &refs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/sidecar/snapshots/%s",
		hc.RouteParams(id),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return nil, mapErr("get snapshot", err)
	}
	return &Snapshot{
		ID:    env.Data.ID,
		OrgID: refs.Org.ID,
		Name:  attrs.Name,
		Tag:   attrs.Tag,
	}, nil
}

func (c *Client) ListSnapshots(ctx context.Context, orgID string) ([]Snapshot, error) {
	var coll v3Collection
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/sidecar/snapshots",
		hc.QueryParam("org_id", orgID),
		hc.JSONDecoder(&coll),
	))
	if err != nil {
		return nil, mapErr("list snapshots", err)
	}
	snapshots := make([]Snapshot, 0, len(coll.Data))
	for _, item := range coll.Data {
		s := Snapshot{ID: item.ID}
		if attrs, ok := item.Attributes.(map[string]any); ok {
			if name, ok := attrs["name"].(string); ok {
				s.Name = name
			}
			if tag, ok := attrs["tag"].(string); ok {
				s.Tag = tag
			}
		}
		if refs, ok := item.References.(map[string]any); ok {
			if org, ok := refs["org"].(map[string]any); ok {
				if id, ok := org["id"].(string); ok {
					s.OrgID = id
				}
			}
		}
		snapshots = append(snapshots, s)
	}
	return snapshots, nil
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
	se := &StatusError{Op: op, StatusCode: he.StatusCode}
	if he.StatusCode == http.StatusGone {
		se.ServerMessage = extractServerMessage(he.Body)
	}
	return se
}

// extractServerMessage tries to pull a human-readable message from a JSON
// error body. Falls back to the raw body string if JSON parsing fails.
func extractServerMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		if payload.Error != "" {
			return payload.Error
		}
		if payload.Message != "" {
			return payload.Message
		}
	}
	return string(body)
}
