package chunkd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strconv"
	"strings"
)

// SessionRefused is the daemon's refusal of a session request: it was reachable
// and understood the request, and said no. The message is the daemon's own and
// is meant to be shown as is; Status tells a caller which kind of no it was
// (409 the run it continues is in use, 503 the daemon lacks a credential, ...).
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
	return sessionCallWith(client, method, path, in, out)
}

// sessionCallWith is sessionCall over client.
func sessionCallWith(client *http.Client, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "http://chunkd"+path, body)
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

// StartPlanBuild asks the daemon to build an approved factory plan, and
// returns the build's session ID without waiting for it. It is cancelled like
// any other session.
func StartPlanBuild(req PlanBuildRequest) (string, error) {
	var resp SessionStartResponse
	if err := sessionCall(http.MethodPost, "/factory/plans", req, &resp); err != nil {
		return "", err
	}
	return resp.ID, nil
}

// SendChat sends msg to its repository's chat, starting or
// resuming its session, and returns once the session has it. It uses the
// long client: starting a session can outlast the short client's timeout.
func SendChat(msg ChatMessage) (SessionLog, error) {
	client, err := longDaemonClient()
	if err != nil {
		return SessionLog{}, err
	}
	var log SessionLog
	err = sessionCallWith(client, http.MethodPost, "/chat", msg, &log)
	return log, err
}

// FetchChat is root's chat's log from cursor after, the Next
// of an earlier read; after < 0 reads only where it ends.
func FetchChat(root string, after int) (SessionLog, error) {
	path := "/chat?root=" + neturl.QueryEscape(root)
	if after >= 0 {
		path += "&after=" + strconv.Itoa(after)
	}
	var log SessionLog
	err := sessionCall(http.MethodGet, path, nil, &log)
	return log, err
}

// FetchPlanBuild returns plan planID's build session and its feed lines past
// the first after. found is false when the daemon has no build of the plan.
func FetchPlanBuild(planID string, after int) (activity PlanBuildActivity, found bool, err error) {
	path := "/factory/plans/" + neturl.PathEscape(planID) + "/build?after=" + strconv.Itoa(after)
	err = sessionCall(http.MethodGet, path, nil, &activity)
	var refused *SessionRefused
	if errors.As(err, &refused) && refused.Status == http.StatusNotFound {
		return PlanBuildActivity{}, false, nil
	}
	if err != nil {
		return PlanBuildActivity{}, false, err
	}
	return activity, true, nil
}

// ApproveAndBuild has the daemon approve factory plan planID, stop its
// planner and start its build, and returns the build's session ID. It uses
// the long client: stopping and starting can outlast the short client's
// timeout.
func ApproveAndBuild(planID string, req PlanApproveRequest) (string, error) {
	client, err := longDaemonClient()
	if err != nil {
		return "", err
	}
	var resp SessionStartResponse
	if err := sessionCallWith(client, http.MethodPost, "/factory/plans/"+neturl.PathEscape(planID)+"/approve", req, &resp); err != nil {
		return "", err
	}
	return resp.ID, nil
}

// RequestPlanner starts factory plan planID's planner in the daemon on the
// plan's request.
func RequestPlanner(planID string, req PlannerRequest) (SessionLog, error) {
	return plannerCall(planID, "/request", req)
}

// ResumePlanner has plan planID's planner claim the plan again when it
// holds no claim, starting or resuming it.
func ResumePlanner(planID string, req PlannerResume) (SessionLog, error) {
	return plannerCall(planID, "/resume", req)
}

// plannerCall posts to one of plan planID's planner routes. It uses the
// long client: starting a session can outlast the short client's timeout.
func plannerCall(planID, route string, in any) (SessionLog, error) {
	client, err := longDaemonClient()
	if err != nil {
		return SessionLog{}, err
	}
	var log SessionLog
	err = sessionCallWith(client, http.MethodPost, "/factory/plans/"+neturl.PathEscape(planID)+"/planner"+route, in, &log)
	return log, err
}

// FetchPlanner reads plan planID's planner's log after cursor after; a
// negative after reads none, only where it ends.
func FetchPlanner(planID string, after int) (SessionLog, error) {
	path := "/factory/plans/" + neturl.PathEscape(planID) + "/planner"
	if after >= 0 {
		path += "?after=" + strconv.Itoa(after)
	}
	var log SessionLog
	err := sessionCall(http.MethodGet, path, nil, &log)
	return log, err
}

// CancelSession stops a session. It is the only thing that does: a viewer that
// quits merely detaches.
func CancelSession(id string) error {
	return sessionCall(http.MethodPost, "/factory/"+neturl.PathEscape(id)+"/cancel", nil, nil)
}

// DeleteSession forgets a finished session. A session still running is refused;
// it has to be cancelled first.
func DeleteSession(id string) error {
	return sessionCall(http.MethodDelete, "/factory/"+neturl.PathEscape(id), nil, nil)
}
