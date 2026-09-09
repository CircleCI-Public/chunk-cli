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

// ConflictState is the daemon's answer to whether one project's branch still
// merges cleanly into its merge target.
//
// It describes committed history only. The preview merges HEAD against the
// target, so uncommitted work in the tree is invisible to it: a conflict that
// exists solely in unstaged edits is not reported here, and anything presenting
// this to a person has to say so rather than let the silence read as an
// all-clear.
type ConflictState struct {
	// Branch is the branch that was compared, empty when there was none.
	Branch string `json:"branch,omitempty"`
	// Target is the merge target, qualified as the remote names it —
	// "origin/main".
	Target string `json:"target,omitempty"`
	// HeadSHA and TargetSHA are the two commits actually merged. They are what
	// makes a result reusable: neither side moving means the merge would
	// resolve identically.
	HeadSHA   string `json:"head_sha,omitempty"`
	TargetSHA string `json:"target_sha,omitempty"`
	// Conflicted reports that the merge does not resolve automatically. False
	// with an empty Unavailable is a real all-clear; false with Unavailable set
	// means no merge was attempted.
	Conflicted bool `json:"conflicted"`
	// Paths lists the conflicted paths, capped at MaxConflictPaths.
	Paths []string `json:"paths,omitempty"`
	// TotalPaths is how many paths conflicted in total, which exceeds len(Paths)
	// when the list was cut.
	TotalPaths int `json:"total_paths,omitempty"`
	// CheckedAt is when the answer was produced, TargetFetchedAt when the
	// target's remote-tracking ref was last refreshed. The second is the one
	// that decides how much the answer is worth.
	CheckedAt       time.Time `json:"checked_at"`
	TargetFetchedAt time.Time `json:"target_fetched_at"`
	// TargetStale reports that the last refresh of the target ref failed, so
	// the comparison ran against whatever was already on disk. The answer may
	// simply be out of date, which is worth saying rather than implying.
	TargetStale bool `json:"target_stale,omitempty"`
	// Unavailable explains why there is no answer, and is empty when there is
	// one. A detached HEAD, a repo with no recorded default branch, and a
	// branch that is itself the merge target all land here — none of them are
	// faults, and all of them would otherwise look like "no conflicts".
	Unavailable string `json:"unavailable,omitempty"`
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
	// Conflict is nil until the first conflict check for this project has run.
	// Nil is "not known yet", distinct from a ConflictState reporting no
	// conflict, and the two must not be collapsed by a reader.
	Conflict *ConflictState `json:"conflict,omitempty"`
}

// ConflictReport is the response to a conflict query for one project root.
type ConflictReport struct {
	Root string `json:"root"`
	// Conflict is nil when the daemon has no answer for this root — either it
	// does not know the project, or no check has run yet.
	Conflict *ConflictState `json:"conflict,omitempty"`
	// Known reports whether the daemon is tracking the root at all. Without it
	// "unknown project" and "checked, nothing to report" are the same response.
	Known bool `json:"known"`
}

// Snapshot is a point-in-time view of all watched projects.
type Snapshot struct {
	Projects []ProjectSnapshot `json:"projects"`
	// AuthError explains why output streaming is unavailable, when it is. An
	// empty logs pane with no explanation sends people hunting the wrong fault,
	// so the daemon reports this rather than silently serving nothing.
	AuthError string `json:"auth_error,omitempty"`
}
