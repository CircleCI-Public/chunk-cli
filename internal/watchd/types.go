package watchd

import (
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
)

// SidecarState describes an active sidecar as returned by the watch daemon.
type SidecarState struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	ProjectName   string      `json:"project_name"`
	SnapshotName  string      `json:"snapshot_name"`
	FileMtime     time.Time   `json:"file_mtime"`
	LastSyncedRef string      `json:"last_synced_ref"`
	InSync        bool        `json:"in_sync"`
	LastActivity  time.Time   `json:"last_activity"`
	LastOp        eventlog.Op `json:"last_op"`
	Running       bool        `json:"running"`
}

// ProjectSnapshot is the daemon's view of one project at a point in time.
type ProjectSnapshot struct {
	Root     string           `json:"root"`
	Branch   string           `json:"branch"`
	HeadRef  string           `json:"head_ref"`
	Sidecars []SidecarState   `json:"sidecars"`
	Events   []eventlog.Event `json:"events"`
}

// Snapshot is a point-in-time view of all watched projects.
type Snapshot struct {
	Projects []ProjectSnapshot `json:"projects"`
}
