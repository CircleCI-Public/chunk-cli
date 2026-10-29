package chunkd

import (
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

// SessionState is where a whole session stands.
type SessionState string

// Session states.
const (
	SessionRunning   SessionState = "running"
	SessionDone      SessionState = "done"
	SessionFailed    SessionState = "failed"
	SessionCancelled SessionState = "cancelled"
)

// Finished reports whether the session has ended for good.
func (s SessionState) Finished() bool {
	return s != SessionRunning
}

// StageID names one stage of a session.
type StageID string

// The stages.
const (
	// StageFactoryLoop is a plan build's whole run: its build session, from
	// start until the plan or the session ends.
	StageFactoryLoop StageID = "factory_loop"
)

// StageState is where one stage stands.
type StageState string

// Stage states.
const (
	StageRunning StageState = "running"
	StageDone    StageState = "done"
	StageFailed  StageState = "failed"
)

// Stage is one stage of a session and how it is going.
type Stage struct {
	ID    StageID    `json:"id"`
	State StageState `json:"state"`
	// Note says more about the state: why a build ended, what failed.
	Note string `json:"note,omitempty"`
}

// Feed is the latest lines of a stream that can run long, such as a build's
// progress, and how many lines there have been, so a follower can tell which
// it has not seen. Only the latest lines are kept.
type Feed struct {
	Lines []FeedLine `json:"lines,omitempty"`
	Total int        `json:"total,omitempty"`
}

// FeedLine is one line of a Feed.
type FeedLine struct {
	Level FeedLevel `json:"level"`
	Text  string    `json:"text"`
}

// FeedLevel is how a feed line reads, as iostream.Level, spelled out so it
// reads in JSON.
type FeedLevel string

// Feed levels.
const (
	FeedStep  FeedLevel = "step"
	FeedInfo  FeedLevel = "info"
	FeedWarn  FeedLevel = "warn"
	FeedDone  FeedLevel = "done"
	FeedError FeedLevel = "error"
)

// Level is the line's iostream level. An unknown value reads as info: it
// comes from a daemon that may be newer than this client.
func (l FeedLevel) Level() iostream.Level {
	switch l {
	case FeedStep:
		return iostream.LevelStep
	case FeedWarn:
		return iostream.LevelWarn
	case FeedDone:
		return iostream.LevelDone
	case FeedError:
		return iostream.LevelError
	case FeedInfo:
	}
	return iostream.LevelInfo
}

// Since returns the lines added after the first seen, as many as are still
// kept.
func (f Feed) Since(seen int) []FeedLine {
	n := min(max(f.Total-seen, 0), len(f.Lines))
	return f.Lines[len(f.Lines)-n:]
}

// FactoryRun is the factory plan a plan build builds, and where its work is.
type FactoryRun struct {
	// PlanID is the factory plan being built, through factory's own tasks, in
	// a build session of Claude Code. PlanState is where factory has the plan.
	PlanID    string `json:"plan_id,omitempty"`
	PlanState string `json:"plan_state,omitempty"`
	// Prompt is the plan's text when the build started.
	Prompt string `json:"prompt"`
	// SpecOrigin is "factory plan ID".
	SpecOrigin string `json:"spec_origin,omitempty"`
	// Worktree is where the work is, on Branch.
	Worktree string `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	// Progress is what the build session said and did as it went.
	Progress Feed `json:"progress,omitzero"`
}

// Session is the record of one plan build. It holds state only.
type Session struct {
	ID          string `json:"id"`
	ProjectRoot string `json:"project_root"`
	Branch      string `json:"branch,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`

	State SessionState `json:"state"`
	Error string       `json:"error,omitempty"`

	// Stages is the session's stages, in order. The only one is its build.
	Stages []Stage `json:"stages"`
	// Factory is the plan being built and where its work is.
	Factory *FactoryRun `json:"factory,omitempty"`

	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// PlanBuildRequest starts a plan build: a build session of Claude Code that
// builds factory plan PlanID, approved in factory, through factory's tasks.
type PlanBuildRequest struct {
	// ProjectRoot is a project the daemon tracks, or the top of a git
	// repository. Required.
	ProjectRoot string `json:"project_root"`
	// PlanID is the approved plan. Required.
	PlanID string `json:"plan_id"`
	Model  string `json:"model,omitempty"`
}

// SessionStartResponse answers an accepted POST /factory/plans.
type SessionStartResponse struct {
	ID string `json:"id"`
}
