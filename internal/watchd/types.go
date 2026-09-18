// Package watchd implements the chunk watch background daemon and its client.
package watchd

import (
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
)

// Resources is one sample of a sidecar's resource usage.
//
// Memory is reported as used-of-limit rather than a percentage so the display
// can show both, and because a limit of zero (unknown) has to be distinguishable
// from a usage of zero.
type Resources struct {
	CPUPercent     float64   `json:"cpu_percent"`
	MemUsedBytes   int64     `json:"mem_used_bytes"`
	MemLimitBytes  int64     `json:"mem_limit_bytes"`
	DiskUsedBytes  int64     `json:"disk_used_bytes"`
	DiskTotalBytes int64     `json:"disk_total_bytes"`
	SampledAt      time.Time `json:"sampled_at"`
}

// SidecarState describes one active sidecar as maintained by the daemon.
type SidecarState struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// SessionID is the agent session that owns this sidecar, empty for state
	// written outside a session or before sessions existed. Sidecars are
	// isolated per session, so two entries for one project and branch are two
	// sessions working in the same tree — this is what tells them apart.
	SessionID    string    `json:"session_id,omitempty"`
	ProjectName  string    `json:"project_name"`
	RepoName     string    `json:"repo_name"`
	SnapshotName string    `json:"snapshot_name"`
	FileMtime    time.Time `json:"file_mtime"`
	// Workspace is the sidecar-side repo path, used to sample disk usage where
	// the work actually happens rather than wherever a shell starts.
	Workspace    string      `json:"workspace,omitempty"`
	LastActivity time.Time   `json:"last_activity"`
	LastOp       eventlog.Op `json:"last_op"`
	LastLevel    string      `json:"last_level"`
	Running      bool        `json:"running"`
	// Resources is the most recent resource sample, or nil when none has
	// arrived — sampling only runs while a dashboard is attached.
	Resources *Resources `json:"resources,omitempty"`
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

// PRCheck is one CI check on a PR's latest commit.
type PRCheck struct {
	Name       string `json:"name"`
	Status     string `json:"status"`     // QUEUED, IN_PROGRESS, COMPLETED
	Conclusion string `json:"conclusion"` // SUCCESS, FAILURE, NEUTRAL, CANCELLED, etc.
}

// PRComment is one review thread comment on a PR.
type PRComment struct {
	Author    string    `json:"author"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	Resolved  bool      `json:"resolved,omitempty"`
}

// PRState is the daemon's advisory view of the open PR for a project's current branch.
// It is nil when no open PR exists for the branch or when GitHub credentials are absent.
type PRState struct {
	Number           int         `json:"number"`
	Title            string      `json:"title"`
	URL              string      `json:"url"`
	UpdatedAt        time.Time   `json:"updated_at,omitempty"`
	CheckState       string      `json:"check_state,omitempty"` // rollup: SUCCESS, FAILURE, PENDING, ERROR, EXPECTED
	Checks           []PRCheck   `json:"checks,omitempty"`
	Comments         []PRComment `json:"comments,omitempty"`
	ChangesRequested bool        `json:"changes_requested,omitempty"`
	FetchedAt        time.Time   `json:"fetched_at"`
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
	// PR is the advisory PR state for the current branch. Nil when no open PR
	// exists, or when GitHub credentials are not configured.
	PR *PRState `json:"pr,omitempty"`
}

// Snapshot is a point-in-time view of all watched projects.
type Snapshot struct {
	Projects []ProjectSnapshot `json:"projects"`
	// AuthError explains why output streaming is unavailable, when it is. An
	// empty logs pane with no explanation sends people hunting the wrong fault,
	// so the daemon reports this rather than silently serving nothing.
	AuthError string `json:"auth_error,omitempty"`
}
