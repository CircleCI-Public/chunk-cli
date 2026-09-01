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

// ProjectSnapshot is the daemon's view of one project at a point in time.
type ProjectSnapshot struct {
	Root     string           `json:"root"`
	Branch   string           `json:"branch"`
	HeadRef  string           `json:"head_ref"`
	RepoName string           `json:"repo_name"`
	Sidecars []SidecarState   `json:"sidecars"`
	Events   []eventlog.Event `json:"events"`
}

// Snapshot is a point-in-time view of all watched projects.
type Snapshot struct {
	Projects []ProjectSnapshot `json:"projects"`
}
