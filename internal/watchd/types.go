// Package watchd implements the chunk watch background daemon and its client.
package watchd

import (
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
)

// SidecarState describes one active sidecar as maintained by the daemon.
type SidecarState struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// SessionID is the agent session that owns this sidecar, empty for state
	// written outside a session or before sessions existed. Sidecars are
	// isolated per session, so two entries for one project and branch are two
	// sessions working in the same tree — this is what tells them apart.
	SessionID    string      `json:"session_id,omitempty"`
	ProjectName  string      `json:"project_name"`
	RepoName     string      `json:"repo_name"`
	SnapshotName string      `json:"snapshot_name"`
	FileMtime    time.Time   `json:"file_mtime"`
	LastActivity time.Time   `json:"last_activity"`
	LastOp       eventlog.Op `json:"last_op"`
	LastLevel    string      `json:"last_level"`
	Running      bool        `json:"running"`
}

// CommandState describes one remote command the daemon is buffering output for.
type CommandState struct {
	CommandID   string     `json:"command_id"`
	SidecarID   string     `json:"sidecar_id"`
	Op          string     `json:"op"`
	Name        string     `json:"name"`
	SubmittedAt time.Time  `json:"submitted_at"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	ExitCode    *int       `json:"exit_code,omitempty"`
	Running     bool       `json:"running"`
	Bytes       int64      `json:"bytes"`
	Truncated   bool       `json:"truncated"`
}

// ProjectSnapshot is the daemon's view of one project at a point in time.
type ProjectSnapshot struct {
	Root     string           `json:"root"`
	Branch   string           `json:"branch"`
	HeadRef  string           `json:"head_ref"`
	RepoName string           `json:"repo_name"`
	Sidecars []SidecarState   `json:"sidecars"`
	Events   []eventlog.Event `json:"events"`
	Commands []CommandState   `json:"commands,omitempty"`
}

// Snapshot is a point-in-time view of all watched projects.
type Snapshot struct {
	Projects []ProjectSnapshot `json:"projects"`
	// AuthError explains why output streaming is unavailable, when it is. An
	// empty logs pane with no explanation sends people hunting the wrong fault,
	// so the daemon reports this rather than silently serving nothing.
	AuthError string `json:"auth_error,omitempty"`
}
