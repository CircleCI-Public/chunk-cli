package watchd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
)

// SessionRefused is the daemon's refusal of a session request: it was reachable
// and understood the request, and said no. The message is the daemon's own and
// is meant to be shown as is; Status tells a caller which kind of no it was
// (409 a session is already active, 503 the daemon lacks a credential, ...).
type SessionRefused struct {
	Status  int
	Message string
}

func (e *SessionRefused) Error() string { return e.Message }

// sessionCall performs one request against the session API and decodes a JSON
// answer into out (skipped when nil). A transport failure is wrapped in
// ErrDaemonUnavailable; a daemon that answered with an error is a
// SessionRefused.
func sessionCall(method, path string, in, out any) error {
	client, err := daemonClient()
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "http://watchd"+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDaemonUnavailable, requestError(err))
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		msg := strings.TrimSpace(string(raw))
		// The router's own generic 404 means a daemon from a build without the
		// session API; the handlers' 404s always say something more specific.
		if resp.StatusCode == http.StatusNotFound && strings.HasPrefix(msg, "404 page not found") {
			return fmt.Errorf("%w: daemon has no session API", ErrDaemonUnavailable)
		}
		return &SessionRefused{Status: resp.StatusCode, Message: msg}
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// StartFactory asks the daemon to start a factory session and returns its ID
// without waiting for it. It is followed and cancelled like any other session.
func StartFactory(req FactoryRequest) (string, error) {
	var resp SessionStartResponse
	if err := sessionCall(http.MethodPost, "/factory", req, &resp); err != nil {
		return "", err
	}
	return resp.ID, nil
}

// FetchSession returns one session with the text of its reviews.
func FetchSession(id string) (SessionDetail, error) {
	var detail SessionDetail
	if err := sessionCall(http.MethodGet, "/session/"+neturl.PathEscape(id), nil, &detail); err != nil {
		return SessionDetail{}, err
	}
	return detail, nil
}

// ListSessions returns the daemon's sessions, newest first per project. An empty
// root lists every project's.
func ListSessions(root string) ([]Session, error) {
	path := "/session"
	if root != "" {
		path += "?root=" + neturl.QueryEscape(root)
	}
	var list SessionList
	if err := sessionCall(http.MethodGet, path, nil, &list); err != nil {
		return nil, err
	}
	return list.Sessions, nil
}

// CancelSession stops a session. It is the only thing that does: a viewer that
// quits merely detaches.
func CancelSession(id string) error {
	return sessionCall(http.MethodPost, "/session/"+neturl.PathEscape(id)+"/cancel", nil, nil)
}
