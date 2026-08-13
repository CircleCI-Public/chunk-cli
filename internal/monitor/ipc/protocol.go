// Package ipc defines the wire protocol shared between monitor daemons.
package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Cmd identifies the type of IPC request.
type Cmd string

// IPC command constants.
const (
	CmdEvent         Cmd = "event"
	CmdListSessions  Cmd = "list_sessions"
	CmdGetEvents     Cmd = "get_events"
	CmdSetValidation Cmd = "set_validation"
	CmdGetSession    Cmd = "get_session"
	CmdPing          Cmd = "ping"
)

// EventType classifies a session lifecycle event.
type EventType string

// Session lifecycle event type constants.
const (
	EventSessionStart EventType = "session_start"
	EventSessionEnd   EventType = "session_end"
	EventToolUse      EventType = "tool_use"
	EventHeartbeat    EventType = "heartbeat"
)

// Session represents a coding agent session.
type Session struct {
	ID               string    `json:"id"`
	ProjectDir       string    `json:"project_dir,omitempty"`
	StartedAt        time.Time `json:"started_at"`
	LastSeenAt       time.Time `json:"last_seen_at"`
	Status           string    `json:"status"` // "active" | "stale" | "ended"
	ValidationStatus string    `json:"validation_status,omitempty"`
	ToolUseCount     int       `json:"tool_use_count,omitempty"`
	// GitStatus is set by the server's background checker.
	// Format: "" unknown, "clean", "dirty", "↑N" ahead, "↓N" behind, "↑N↓M" diverged.
	GitStatus string `json:"git_status,omitempty"`
	// ResolutionBranch is set when the server has created a conflict-resolution branch.
	ResolutionBranch string `json:"resolution_branch,omitempty"`
}

// Event is a single recorded event within a session.
type Event struct {
	ID         int64     `json:"id"`
	SessionID  string    `json:"session_id"`
	EventType  string    `json:"event_type"`
	ToolName   string    `json:"tool_name,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

// Request is sent from client to daemon over the Unix socket.
type Request struct {
	Cmd        Cmd            `json:"cmd"`
	SessionID  string         `json:"session_id,omitempty"`
	EventType  EventType      `json:"event_type,omitempty"`
	ToolName   string         `json:"tool_name,omitempty"`
	ProjectDir string         `json:"project_dir,omitempty"`
	Timestamp  time.Time      `json:"ts"`
	Payload    map[string]any `json:"payload,omitempty"`
}

// Response is returned by the daemon for each request.
type Response struct {
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Sessions []Session `json:"sessions,omitempty"`
	Events   []Event   `json:"events,omitempty"`
}

// Send encodes req as newline-delimited JSON and writes it to conn.
func Send(conn net.Conn, req Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

// Receive reads one newline-delimited JSON request from conn.
func Receive(conn net.Conn) (Request, error) {
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Request{}, err
		}
		return Request{}, fmt.Errorf("connection closed")
	}
	var req Request
	if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
		return Request{}, fmt.Errorf("unmarshal request: %w", err)
	}
	return req, nil
}

// SendResponse encodes resp as newline-delimited JSON and writes it to conn.
func SendResponse(conn net.Conn, resp Response) error {
	data, err := json.Marshal(resp)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

// ReceiveResponse reads one newline-delimited JSON response from conn.
func ReceiveResponse(conn net.Conn) (Response, error) {
	scanner := bufio.NewScanner(conn)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return Response{}, err
		}
		return Response{}, fmt.Errorf("connection closed")
	}
	var resp Response
	if err := json.Unmarshal(scanner.Bytes(), &resp); err != nil {
		return Response{}, fmt.Errorf("unmarshal response: %w", err)
	}
	return resp, nil
}
