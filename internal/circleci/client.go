package circleci

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	hc "github.com/CircleCI-Public/chunk-cli/internal/httpcl"
	"github.com/CircleCI-Public/chunk-cli/internal/sse"
	"github.com/CircleCI-Public/chunk-cli/internal/version"
)

// ErrTokenNotFound indicates no CircleCI token was found in env or config.
var ErrTokenNotFound = errors.New("api token not found")

// ErrNotAuthorized indicates the request was rejected (401/403).
var ErrNotAuthorized = errors.New("not authorized")

// ErrOutputFormatUnsupported indicates the output stream contained no events this
// build understands.
//
// It means the API is *older* than this binary, not newer: the frame vocabulary
// is designed so a newer API only ever adds event types while still emitting
// stdout, stderr and exit. Receiving none of those therefore places the API
// behind the client, which is the opposite of what a naive "upgrade" hint would
// imply.
var ErrOutputFormatUnsupported = errors.New("sidecar output format not supported")

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

// Stream names, matching the wire event names.
const (
	StreamStdout = "stdout"
	StreamStderr = "stderr"
)

// OutputFn receives a run of raw output bytes from one stream, exactly as the
// remote command wrote them. data is only valid for the duration of the call.
type OutputFn func(stream string, data []byte)

// maxOutputFrame caps a single SSE frame. The API batches output at 64KiB, which
// base64-encodes to ~87KiB, so this is generous.
const maxOutputFrame = 1 << 20

// maxStreamStalls bounds consecutive attempts that deliver nothing at all —
// connection refused, a 5xx, an instant close. Those are the only attempts worth
// counting: reconnecting is the normal way this API serves a long command, since
// the server caps each connection by design.
const maxStreamStalls = 5

// maxStreamAttempts is a runaway guard, not a real limit. With connections capped
// at around fifteen seconds a legitimate stream reconnects roughly four times a
// minute, so this allows many hours of output while still bounding a server that
// talks but never terminates the stream.
// A var only so tests can shrink it.
var maxStreamAttempts = 2000

// streamRetryBase is the first delay after a stalled attempt, scaled up per
// consecutive stall. A healthy reconnect does not wait at all.
// A var only so tests can shrink it; not intended to vary in normal use.
var streamRetryBase = 500 * time.Millisecond

type execSubmitAttrs struct {
	Phase string `json:"phase"`
}

// SubmitExec submits a command for execution and returns its command ID without
// consuming any output.
//
// Splitting submission from streaming is what makes a command observable while
// it runs: the ID is the handle to its output stream, and a caller that wants to
// hand that handle to something else — a log tailer, the watch daemon — needs it
// before the command finishes, not after.
func (c *Client) SubmitExec(
	ctx context.Context, sidecarID, command string, args []string, env map[string]string,
) (string, error) {
	var attrs execSubmitAttrs
	envelope := v3Envelope{Data: v3DataEntity{Attributes: &attrs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/sidecar/instances/%s/exec",
		hc.RouteParams(sidecarID),
		hc.Body(ExecRequest{Command: command, Args: args, Env: env}),
		hc.JSONDecoder(&envelope),
	))
	if err != nil {
		return "", mapErr("exec", err)
	}
	return envelope.Data.ID, nil
}

// StreamOutput consumes a command's output stream from cursor to termination,
// reconnecting as needed. An empty cursor starts from the beginning of whatever
// output the server still retains, which is what makes this usable both to tail
// a running command and to replay one that has already exited.
//
// When onOutput is non-nil it receives each run of output bytes as it arrives and
// the returned Stdout/Stderr are left empty — accumulating is then the caller's
// choice, which matters because output can be arbitrarily large.
func (c *Client) StreamOutput(
	ctx context.Context, commandID, cursor string, onOutput OutputFn,
) (*ExecResponse, error) {
	return c.streamCommandOutput(ctx, commandID, cursor, onOutput)
}

// Exec submits a command and collects its output. It is the composition of
// SubmitExec and StreamOutput, retained because most callers want exactly that
// and have no use for the command ID until the command is done.
//
// When onOutput is non-nil it receives each run of output bytes as it arrives
// and Stdout/Stderr are left empty — accumulating is then the caller's choice,
// which matters because output can be arbitrarily large.
func (c *Client) Exec(
	ctx context.Context, sidecarID, command string, args []string, env map[string]string, onOutput OutputFn,
) (*ExecResponse, error) {
	commandID, err := c.SubmitExec(ctx, sidecarID, command, args, env)
	if err != nil {
		return nil, err
	}
	return c.streamCommandOutput(ctx, commandID, "", onOutput)
}

// exitEvent is the payload of a terminal `exit` frame.
type exitEvent struct {
	ExitCode int    `json:"exit_code"`
	Signal   string `json:"signal"`
	PID      int    `json:"pid"`
}

// errorEvent is the payload of a terminal `error` frame.
type errorEvent struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

// streamCommandOutput reads a command's output stream to its end, reconnecting
// from the last cursor whenever the connection drops before a terminal event.
// cursor is where to start; empty means the beginning of retained output.
//
// The API ends a stream with exactly one exit or error event, or with nothing at
// all; nothing at all means the connection was interrupted, which is precisely
// what makes resuming safe rather than a guess.
func (c *Client) streamCommandOutput(
	ctx context.Context, commandID, cursor string, onOutput OutputFn,
) (*ExecResponse, error) {
	result := &ExecResponse{CommandID: commandID}

	var (
		attempts int
		stalls   int
	)

	for {
		outcome, err := c.streamOnce(ctx, commandID, cursor, onOutput, result)
		attempts++

		// Frames arrived but none were intelligible. Retrying cannot help, and
		// reporting it as a stalled stream would send someone hunting a network
		// fault that does not exist.
		if err == nil && outcome.frames > 0 && outcome.known == 0 {
			return nil, ErrOutputFormatUnsupported
		}

		if outcome.cursor != "" {
			cursor = outcome.cursor
		}

		switch {
		case outcome.exited:
			return result, nil
		case outcome.failed != nil:
			if !outcome.failed.Retryable {
				return nil, fmt.Errorf("remote command: %s", outcome.failed.Message)
			}
		case err != nil:
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if !isRetryable(err) {
				return nil, mapErr("stream command output", err)
			}
		}

		// A connection that delivered any frame proves the far end is alive and
		// talking, so it resets the stall count. An empty stream (no frames,
		// no error) means the connection was interrupted before any data
		// arrived — that is a resume trigger, not a failure. Only a genuine
		// connectivity error (5xx, transport failure) counts as a stall.
		if outcome.frames > 0 {
			stalls = 0
		} else if err != nil {
			stalls++
			if stalls > maxStreamStalls {
				return nil, fmt.Errorf(
					"stream command output: gave up after %d attempts that returned nothing", stalls-1)
			}
		}

		if attempts >= maxStreamAttempts {
			return nil, fmt.Errorf(
				"stream command output: gave up after %d reconnects without the command finishing", attempts)
		}

		// Resume immediately after a normal drop or an interrupted empty
		// connection. Only back off after a real connectivity error.
		if err == nil || stalls == 0 {
			continue
		}
		delay := min(time.Duration(stalls)*streamRetryBase, 10*streamRetryBase)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(delay):
		}
	}
}

// streamOutcome is how a single connection to the output stream ended.
type streamOutcome struct {
	// cursor is the last event id seen, for resuming.
	cursor string
	// exited is true if a terminal exit event arrived.
	exited bool
	// failed is set if a terminal error event arrived.
	failed *errorEvent
	// frames counts every frame received, recognised or not.
	frames int
	// known counts frames this client understood. Frames without known means the
	// server is speaking a format this version does not, which is worth saying
	// plainly rather than reporting as a stalled stream.
	known int
}

// streamOnce consumes one connection's worth of the output stream.
func (c *Client) streamOnce(
	ctx context.Context, commandID, cursor string, onOutput OutputFn, result *ExecResponse,
) (streamOutcome, error) {
	var outcome streamOutcome

	decoder := func(r io.Reader) error {
		last, err := sse.Scan(r, maxOutputFrame, func(f sse.Frame) error {
			if !f.Comment {
				outcome.frames++
			}
			switch f.Event {
			case StreamStdout, StreamStderr:
				outcome.known++
				raw, decErr := base64.StdEncoding.DecodeString(string(f.Data))
				if decErr != nil {
					return fmt.Errorf("decoding %s payload: %w", f.Event, decErr)
				}
				if onOutput != nil {
					onOutput(f.Event, raw)
					return nil
				}
				if f.Event == StreamStdout {
					result.Stdout += string(raw)
				} else {
					result.Stderr += string(raw)
				}
			case "exit":
				outcome.known++
				var e exitEvent
				if jsonErr := json.Unmarshal(f.Data, &e); jsonErr != nil {
					return fmt.Errorf("decoding exit event: %w", jsonErr)
				}
				result.ExitCode = e.ExitCode
				result.PID = e.PID
				result.Signal = e.Signal
				outcome.exited = true
			case "error":
				outcome.known++
				var e errorEvent
				if jsonErr := json.Unmarshal(f.Data, &e); jsonErr != nil {
					return fmt.Errorf("decoding error event: %w", jsonErr)
				}
				outcome.failed = &e
			case "start":
				outcome.known++
			default:
				// A future event type: ignored, which is what keeps the wire
				// format extensible.
			}
			return nil
		})
		if last != "" {
			outcome.cursor = last
		}
		return err
	}

	opts := []func(*hc.Request){
		hc.RouteParams(commandID),
		hc.Header("Accept", "text/event-stream"),
		hc.Decoder(decoder),
		hc.NoTimeout(),
	}
	if cursor != "" {
		opts = append(opts, hc.Header("Last-Event-ID", cursor))
	}

	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodGet, "/api/v3/sidecar/commands/%s/output", opts...))
	return outcome, err
}

// isRetryable reports whether a failed stream attempt is worth resuming. A
// transport error is; a definitive rejection from the API is not, since
// reconnecting would only repeat it.
func isRetryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.StatusCode >= 500 && se.StatusCode != http.StatusNotImplemented
	}
	var he *hc.HTTPError
	if errors.As(err, &he) {
		return he.StatusCode >= 500
	}
	// Anything that is not an HTTP status is a transport or decode failure, which
	// resuming from the last cursor is exactly the remedy for.
	return true
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

type pruneScope struct {
	To time.Time `json:"to"`
}

type pruneRequest struct {
	OrgID string      `json:"org_id"`
	Scope *pruneScope `json:"scope,omitempty"`
}

type pruneAttrs struct {
	DeletedCount int `json:"deleted_count"`
}

func (c *Client) PruneSidecars(ctx context.Context, orgID string, before *time.Time) (int, error) {
	req := pruneRequest{OrgID: orgID}
	if before != nil {
		req.Scope = &pruneScope{To: *before}
	}
	var attrs pruneAttrs
	env := v3Envelope{Data: v3DataEntity{Attributes: &attrs}}
	_, err := c.cl.Call(ctx, hc.NewRequest(http.MethodPost, "/api/v3/sidecar/instances/prune",
		hc.Body(req),
		hc.JSONDecoder(&env),
	))
	if err != nil {
		return 0, mapErr("prune sidecars", err)
	}
	return attrs.DeletedCount, nil
}

type snapshotAttrs struct {
	Name     string `json:"name"`
	Tag      string `json:"tag,omitempty"`
	IsSystem bool   `json:"is_system,omitempty"`
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
		ID:       env.Data.ID,
		OrgID:    refs.Org.ID,
		Name:     attrs.Name,
		Tag:      attrs.Tag,
		IsSystem: attrs.IsSystem,
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
			if isSystem, ok := attrs["is_system"].(bool); ok {
				s.IsSystem = isSystem
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
	// Every status carries the server's own explanation, not just 410. The API
	// says things like "sidecar is paused" that no status text can convey, and
	// dropping them left users with a bare "409 Conflict" to interpret.
	return &StatusError{
		Op:            op,
		StatusCode:    he.StatusCode,
		ServerMessage: extractServerMessage(he.Body),
	}
}

// extractServerMessage pulls the human-readable message out of a JSON error
// body, falling back to the raw body only when there is nothing better.
//
// Three shapes are in play. V3 nests the text in an object,
// {"error":{"title":"..."}}, while older endpoints return a bare string as
// either "error" or "message". Decoding "error" as a string — as this used to —
// fails outright on the V3 shape, so every V3 error surfaced to users as a raw
// JSON envelope complete with trace id.
func extractServerMessage(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var payload struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return string(body)
	}

	var v3 struct {
		Title string `json:"title"`
	}
	if json.Unmarshal(payload.Error, &v3) == nil && v3.Title != "" {
		return v3.Title
	}

	var bare string
	if json.Unmarshal(payload.Error, &bare) == nil && bare != "" {
		return bare
	}
	if payload.Message != "" {
		return payload.Message
	}
	return string(body)
}
