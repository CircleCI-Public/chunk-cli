package watchd

import (
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// MaxRounds is how many review-and-fix rounds one session runs at most.
const MaxRounds = 3

// SessionState is where a whole session stands.
type SessionState string

// Session states.
const (
	SessionRunning SessionState = "running"
	// SessionPaused means the session stopped on its own and is waiting for a
	// person: the files changed underneath it. PauseReason says what.
	SessionPaused    SessionState = "paused"
	SessionDone      SessionState = "done"
	SessionFailed    SessionState = "failed"
	SessionCancelled SessionState = "cancelled"
)

// Finished reports whether the session has ended for good. A paused session has
// not: it is waiting.
func (s SessionState) Finished() bool {
	return s != SessionRunning && s != SessionPaused
}

// StageID names one stage of the whole pre-PR flow.
type StageID string

// The stages, in order. Only the review loop is built; the rest are on the
// record from the start so the dashboard can show where the flow is going, and
// so building one is filling in its Stage and not changing the record's shape.
const (
	StageReviewLoop StageID = "review_loop"
	StageRebase     StageID = "rebase"
	StageCI         StageID = "ci"
	StageApproval   StageID = "approval"
	StagePR         StageID = "pr"
)

// StageState is where one stage stands.
type StageState string

// Stage states.
const (
	// StageNotBuilt marks a stage the daemon has no implementation of yet. It is
	// shown as "not built yet" and is never run.
	StageNotBuilt StageState = "not_built"
	StagePending  StageState = "pending"
	StageRunning  StageState = "running"
	StagePaused   StageState = "paused"
	StageDone     StageState = "done"
	StageFailed   StageState = "failed"
	StageSkipped  StageState = "skipped"
)

// Stage is one stage of the flow and how it is going.
type Stage struct {
	ID    StageID    `json:"id"`
	State StageState `json:"state"`
	// Note says more about the state: why a loop ended, what failed.
	Note string `json:"note,omitempty"`
}

// RoundState is where one review-and-fix round stands.
type RoundState string

// Round states.
const (
	RoundReviewing RoundState = "reviewing"
	RoundFixing    RoundState = "fixing"
	RoundApplying  RoundState = "applying"
	RoundDone      RoundState = "done"
	RoundFailed    RoundState = "failed"
	// RoundSuperseded marks a round abandoned because the files changed under it;
	// the round is run again against the new files.
	RoundSuperseded RoundState = "superseded"
)

// FixState is where the fix half of a round stands.
type FixState string

// Fix states.
const (
	FixRunning FixState = "running"
	FixApplied FixState = "applied"
	// FixEmpty means the agent made no changes.
	FixEmpty  FixState = "empty"
	FixFailed FixState = "failed"
)

// FileChange is one file a round's fixes changed in the working tree.
type FileChange struct {
	Path       string `json:"path"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
}

// RoundFix is what a round's fixes did to the user's files.
type RoundFix struct {
	State      FixState     `json:"state"`
	Files      []FileChange `json:"files,omitempty"`
	Insertions int          `json:"insertions,omitempty"`
	Deletions  int          `json:"deletions,omitempty"`
	// FindingIDs are the findings the agent was asked to fix.
	FindingIDs []string `json:"finding_ids,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// ReviewPrompt is one review's progress inside a round. It carries state only:
// what the review said is fetched on demand through SessionDetail, the way
// command output is fetched through /output, so a snapshot stays small however
// much Claude wrote.
type ReviewPrompt struct {
	Name      string         `json:"name"`
	State     PromptRunState `json:"state"`
	SidecarID string         `json:"sidecar_id,omitempty"`
	// CommandID names the buffered output of this review's Claude run, readable
	// through /output while it runs and after.
	CommandID  string `json:"command_id,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
	// Findings counts the structured findings parsed from this review's output.
	Findings int `json:"findings,omitempty"`
}

// PromptRunState is where one review of a round stands. The values match
// review.PromptState one for one, spelled out so they read in JSON and survive
// the enum being reordered.
type PromptRunState string

// Review states.
const (
	PromptQueued  PromptRunState = "queued"
	PromptRunning PromptRunState = "running"
	PromptDone    PromptRunState = "done"
	PromptFailed  PromptRunState = "failed"
)

// Progress maps the state onto the review package's own, which is what the row
// renderers shared with `chunk review` are written against. An unknown value
// reads as queued: it comes from a daemon that may be newer than this client.
func (s PromptRunState) Progress() review.PromptState {
	switch s {
	case PromptRunning:
		return review.StateRunning
	case PromptDone:
		return review.StateDone
	case PromptFailed:
		return review.StateFailed
	case PromptQueued:
		return review.StateQueued
	}
	return review.StateQueued
}

// Round is one pass of reviewing the work and fixing what the reviews found.
type Round struct {
	Number  int            `json:"number"`
	State   RoundState     `json:"state"`
	Reviews []ReviewPrompt `json:"reviews"`
	// Findings counts every finding the round's reviews reported; Worth counts
	// the ones worth changing (severity high or medium).
	Findings int       `json:"findings"`
	Worth    int       `json:"worth"`
	Fix      *RoundFix `json:"fix,omitempty"`
	// Note says why the round ended the way it did, such as why the loop stopped.
	Note      string     `json:"note,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// RestorePoint is the saved state a session can be undone to.
type RestorePoint struct {
	// Ref is the git ref that holds the snapshot, so it survives garbage
	// collection and a daemon restart.
	Ref     string    `json:"ref"`
	HeadSHA string    `json:"head_sha"`
	SavedAt time.Time `json:"saved_at"`
	// Paths are the files the session's fixes changed: what a restore puts back.
	Paths    []string `json:"paths,omitempty"`
	Restored bool     `json:"restored,omitempty"`
}

// Session is the record of one pre-PR session: the review loop, and the stages
// that will follow it. It holds state only; text is in SessionDetail.
type Session struct {
	ID          string `json:"id"`
	ProjectRoot string `json:"project_root"`
	Branch      string `json:"branch,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`

	State SessionState `json:"state"`
	// PauseReason says why a paused session is waiting, and PausedPaths which
	// files changed underneath it.
	PauseReason string   `json:"pause_reason,omitempty"`
	PausedPaths []string `json:"paused_paths,omitempty"`
	Error       string   `json:"error,omitempty"`

	// Stages is always the full flow, in order.
	Stages []Stage `json:"stages"`
	Rounds []Round `json:"rounds"`
	// Restore is nil until the first fix is about to change the user's files.
	Restore *RestorePoint `json:"restore,omitempty"`

	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// newStages returns the full flow for a session that is starting: the review
// loop running and every later stage not built.
func newStages() []Stage {
	return []Stage{
		{ID: StageReviewLoop, State: StageRunning},
		{ID: StageRebase, State: StageNotBuilt},
		{ID: StageCI, State: StageNotBuilt},
		{ID: StageApproval, State: StageNotBuilt},
		{ID: StagePR, State: StageNotBuilt},
	}
}

// ReviewResult is one review's full output.
type ReviewResult struct {
	Prompt    string `json:"prompt"`
	SidecarID string `json:"sidecar_id,omitempty"`
	// Output is Claude's prose, capped at maxReviewOutput.
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	// Findings are the structured findings the review gave, each with an ID
	// unique within the round. Empty when it gave none or failed; Error tells
	// the two apart.
	Findings []review.Finding `json:"findings,omitempty"`
	// FindingsDropped counts findings that were unusable or over the cap.
	FindingsDropped int `json:"findings_dropped,omitempty"`
}

// RoundDetail is the text of one round.
type RoundDetail struct {
	Number  int            `json:"number"`
	Results []ReviewResult `json:"results,omitempty"`
}

// SessionDetail is a session plus the text of its reviews.
type SessionDetail struct {
	Session
	Details []RoundDetail `json:"details,omitempty"`
}

// SessionRequest starts a session.
type SessionRequest struct {
	// ProjectRoot is a project the daemon tracks. Required.
	ProjectRoot string `json:"project_root"`
	// PromptsDir is a directory of review prompts relative to the project root;
	// empty means .chunk/reviews. Absolute paths and paths that leave the
	// project are refused.
	PromptsDir  string `json:"prompts_dir,omitempty"`
	Parallelism int    `json:"parallelism,omitempty"`
	Model       string `json:"model,omitempty"`
	// TimeoutSeconds bounds each review; zero means review.DefaultTimeout.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// MaxRounds lowers the number of rounds; zero or more than MaxRounds means
	// MaxRounds.
	MaxRounds int `json:"max_rounds,omitempty"`
}

// RestoreRequest asks to undo a session's changes.
type RestoreRequest struct {
	// Force restores even files that were edited after the session changed them,
	// discarding those edits.
	Force bool `json:"force,omitempty"`
}

// RestoreResult lists the files a restore put back (or removed, if the session
// had created them).
type RestoreResult struct {
	Paths []string `json:"paths"`
}

// SessionStartResponse answers an accepted POST /session.
type SessionStartResponse struct {
	ID string `json:"id"`
}

// SessionList answers GET /session.
type SessionList struct {
	Sessions []Session `json:"sessions"`
}
