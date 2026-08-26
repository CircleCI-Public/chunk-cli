package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

const jsonKeyMsg = "msg"

// sshDialer can open network connections over an SSH connection.
type sshDialer interface {
	Dial(network, addr string) (net.Conn, error)
}

// Client reports events to the daemon REST API and can subscribe to the SSE stream.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient creates a daemon client. baseURL is e.g. "http://localhost:7777".
func NewClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 5 * time.Second},
	}
}

// NewLocalClient creates a daemon client for use inside the sidecar, where the
// daemon is reachable directly at baseURL (e.g. "http://localhost:7777").
// Unlike NewClient, it carries no request timeout so SSE subscriptions stay open.
func NewLocalClient(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http:    &http.Client{},
	}
}

// NewClientOverSSH creates a daemon client that routes HTTP through an SSH
// connection, dialing remoteAddr on the remote host.
func NewClientOverSSH(conn sshDialer, remoteAddr string) *Client {
	transport := &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return conn.Dial("tcp", remoteAddr)
		},
	}
	return &Client{
		baseURL: "http://chunk-daemon",
		http:    &http.Client{Transport: transport},
	}
}

func (c *Client) post(ctx context.Context, path string, body any) (err error) {
	data, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		return fmt.Errorf("marshal: %w", marshalErr)
	}
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(data))
	if reqErr != nil {
		return reqErr
	}
	req.Header.Set("Content-Type", "application/json")
	resp, doErr := c.http.Do(req)
	if doErr != nil {
		return fmt.Errorf("post %s: %w", path, doErr)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("post %s: status %d", path, resp.StatusCode)
	}
	return nil
}

func (c *Client) put(ctx context.Context, path string, body any) (err error) {
	data, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		return fmt.Errorf("marshal: %w", marshalErr)
	}
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPut, c.baseURL+path, bytes.NewReader(data))
	if reqErr != nil {
		return reqErr
	}
	req.Header.Set("Content-Type", "application/json")
	resp, doErr := c.http.Do(req)
	if doErr != nil {
		return fmt.Errorf("put %s: %w", path, doErr)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("put %s: status %d", path, resp.StatusCode)
	}
	return nil
}

// StartInvocation registers a new invocation and returns its ID.
func (c *Client) StartInvocation(ctx context.Context, sidecarID, op, branch string) (_ string, err error) {
	data, _ := json.Marshal(map[string]string{"sidecar_id": sidecarID, "op": op, "branch": branch})
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/invocations", bytes.NewReader(data))
	if reqErr != nil {
		return "", reqErr
	}
	req.Header.Set("Content-Type", "application/json")
	resp, doErr := c.http.Do(req)
	if doErr != nil {
		return "", fmt.Errorf("start invocation: %w", doErr)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	var out struct {
		InvocationID string `json:"invocation_id"`
	}
	if decErr := json.NewDecoder(resp.Body).Decode(&out); decErr != nil {
		return "", fmt.Errorf("decode: %w", decErr)
	}
	return out.InvocationID, nil
}

// Step reports a step to an active invocation.
func (c *Client) Step(ctx context.Context, invocationID, level, msg string) error {
	return c.post(ctx, "/api/invocations/"+invocationID+"/steps",
		map[string]string{"level": level, jsonKeyMsg: msg})
}

// Finish completes an invocation.
func (c *Client) Finish(ctx context.Context, invocationID string, passed, total int, durationMs int64, ok bool, msg string) error {
	return c.post(ctx, "/api/invocations/"+invocationID+"/finish", map[string]any{
		"passed": passed, "total": total, "duration_ms": durationMs, "ok": ok, "msg": msg,
	})
}

// UpsertSidecar updates sidecar metadata in the daemon.
func (c *Client) UpsertSidecar(ctx context.Context, id, name string, syncState SyncState, lastSyncedRef string) error {
	return c.put(ctx, "/api/sidecars/"+id, map[string]any{
		"name": name, "sync_state": syncState, "last_synced_ref": lastSyncedRef,
	})
}

// SSEEvent is a parsed server-sent event from the daemon stream.
type SSEEvent struct {
	Type string
	Data json.RawMessage
}

// Subscribe connects to /events and calls fn for each event until ctx is
// cancelled or the connection closes.
func (c *Client) Subscribe(ctx context.Context, fn func(SSEEvent)) (err error) {
	req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/events", nil)
	if reqErr != nil {
		return reqErr
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, doErr := c.http.Do(req)
	if doErr != nil {
		return fmt.Errorf("connect to events: %w", doErr)
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()

	var eventType string
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			fn(SSEEvent{Type: eventType, Data: json.RawMessage(data)})
			eventType = ""
		}
	}
	return scanner.Err()
}
