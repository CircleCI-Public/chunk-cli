package watchd

import (
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/review"
)

// DefaultRounds is how many rounds of checks and fixes a session runs unless
// asked for a different number.
const DefaultRounds = 3

// MaxRounds is how many rounds a session may be asked to run.
const MaxRounds = 10

// SessionState is where a whole session stands.
type SessionState string

// Session states.
const (
	SessionRunning   SessionState = "running"
	SessionDone      SessionState = "done"
	SessionFailed    SessionState = "failed"
	SessionCancelled SessionState = "cancelled"
)

// Finished reports whether the session has ended.
func (s SessionState) Finished() bool {
	return s != SessionRunning
}

// Outcome is how a session's loop ended, once it has.
type Outcome string

// Outcomes.
const (
	// OutcomePassed means the last round's checks all ran and found nothing
	// worth changing.
	OutcomePassed Outcome = "passed"
	// OutcomeExhausted means rounds ran out with something still to change.
	OutcomeExhausted Outcome = "exhausted"
	// OutcomeStuck means the implementer made no changes for the findings it was
	// given, so another round would check the same code again.
	OutcomeStuck Outcome = "stuck"
	// OutcomeNoChange means the implementer's first turn changed nothing.
	OutcomeNoChange Outcome = "no_change"
)

// StageID names one stage of the whole flow.
type StageID string

// The stages, in order. Only the implementer's turn and the loop are built; the
// rest are on the record from the start so the dashboard can show where the
// flow is going, and so building one is filling in its Stage and not changing
// the record's shape.
const (
	StageImplement  StageID = "implement"
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

// RoundState is where one round of checks and fixes stands.
type RoundState string

// Round states.
const (
	RoundReviewing RoundState = "reviewing"
	RoundFixing    RoundState = "fixing"
	RoundApplying  RoundState = "applying"
	RoundDone      RoundState = "done"
	RoundFailed    RoundState = "failed"
)

// FixState is where an implementer turn stands.
type FixState string

// Fix states.
const (
	FixRunning FixState = "running"
	FixApplied FixState = "applied"
	// FixEmpty means the agent made no changes.
	FixEmpty  FixState = "empty"
	FixFailed FixState = "failed"
)

// FileChange is one file an implementer turn changed.
type FileChange struct {
	Path       string `json:"path"`
	Insertions int    `json:"insertions"`
	Deletions  int    `json:"deletions"`
}

// RoundFix is one implementer turn and what it did to the work: the first
// turn, which implements the task, or a round's, which fixes what the checks
// found.
type RoundFix struct {
	State     FixState `json:"state"`
	SidecarID string   `json:"sidecar_id,omitempty"`
	// CommandID names the buffered output of the turn's Claude run.
	CommandID string `json:"command_id,omitempty"`
	// Activity is the latest thing the implementer did, such as "Edit main.go",
	// while the turn runs; Summary is what it said it did when it finished.
	Activity   string       `json:"activity,omitempty"`
	Summary    string       `json:"summary,omitempty"`
	Files      []FileChange `json:"files,omitempty"`
	Insertions int          `json:"insertions,omitempty"`
	Deletions  int          `json:"deletions,omitempty"`
	// FindingIDs are the findings the implementer was asked to fix.
	FindingIDs []string `json:"finding_ids,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// CheckKind tells a round's checks apart: reviews, and the project's
// validation commands.
type CheckKind string

// CheckValidate marks a validation command. A command that exits non-zero is
// done, with one finding: its output. One that could not run at all failed.
const CheckValidate CheckKind = "validate"

// ReviewPrompt is one check's progress inside a round: a review, or a
// validation command. It carries state only: what the check said is fetched on
// demand through SessionDetail, the way command output is fetched through
// /output, so a snapshot stays small however much was written.
type ReviewPrompt struct {
	Name string `json:"name"`
	// Kind is CheckValidate for a validation command; empty means a review.
	Kind      CheckKind      `json:"kind,omitempty"`
	State     PromptRunState `json:"state"`
	SidecarID string         `json:"sidecar_id,omitempty"`
	// CommandID names the buffered output of this check's run, readable
	// through /output while it runs and after.
	CommandID  string `json:"command_id,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
	// Findings counts the findings the check gave.
	Findings int `json:"findings,omitempty"`
}

// PromptRunState is where one check of a round stands. The values match
// review.PromptState one for one, spelled out so they read in JSON and survive
// the enum being reordered.
type PromptRunState string

// Check states.
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

// Round is one pass of checking the work and fixing what the checks found.
type Round struct {
	Number  int            `json:"number"`
	State   RoundState     `json:"state"`
	Reviews []ReviewPrompt `json:"reviews"`
	// Findings counts every finding the round's checks reported; Worth counts
	// the ones worth changing (severity high or medium, or a failed command).
	Findings int       `json:"findings"`
	Worth    int       `json:"worth"`
	Fix      *RoundFix `json:"fix,omitempty"`
	// Note says why the round ended the way it did, such as why the loop stopped.
	Note      string     `json:"note,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// Session is the record of one session: an implementer on a sidecar writes
// the task in a worktree of its own, rounds of review, validation and fixes
// follow, and the work is committed to a branch. It holds state only; text is
// in SessionDetail.
type Session struct {
	ID          string `json:"id"`
	ProjectRoot string `json:"project_root"`
	// Branch and HeadSHA are where the user's checkout was when the session
	// started: what the work is based on.
	Branch  string `json:"branch,omitempty"`
	HeadSHA string `json:"head_sha,omitempty"`

	// Task is what the session was asked to implement.
	Task string `json:"task"`
	// WorkDir is the worktree the session works in, while it exists.
	WorkDir string `json:"work_dir,omitempty"`
	// WorkBranch is the branch the session's work is committed to. It is
	// cleared if the session ends with nothing to keep.
	WorkBranch string `json:"work_branch,omitempty"`
	// WorkCommit is the commit holding the work, once made.
	WorkCommit string `json:"work_commit,omitempty"`

	State SessionState `json:"state"`
	Error string       `json:"error,omitempty"`
	// Outcome is how the loop ended, once it has.
	Outcome Outcome `json:"outcome,omitempty"`

	// Stages is always the full flow, in order.
	Stages []Stage `json:"stages"`
	// Implement is the implementer's first turn.
	Implement *RoundFix `json:"implement,omitempty"`
	Rounds    []Round   `json:"rounds"`

	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// newStages returns the full flow for a session that is starting: the
// implementer's turn running and every later stage waiting or not built.
func newStages() []Stage {
	return []Stage{
		{ID: StageImplement, State: StageRunning},
		{ID: StageReviewLoop, State: StagePending},
		{ID: StageRebase, State: StageNotBuilt},
		{ID: StageCI, State: StageNotBuilt},
		{ID: StageApproval, State: StageNotBuilt},
		{ID: StagePR, State: StageNotBuilt},
	}
}

// ReviewResult is one check's full output.
type ReviewResult struct {
	Prompt string `json:"prompt"`
	// Kind is CheckValidate for a validation command; empty means a review.
	Kind      CheckKind `json:"kind,omitempty"`
	SidecarID string    `json:"sidecar_id,omitempty"`
	// Output is Claude's prose, or the tail of a command's output, capped at
	// maxReviewOutput.
	Output     string `json:"output,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	// Findings are the structured findings the check gave, each with an ID
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

// SessionDetail is a session plus the text of its checks.
type SessionDetail struct {
	Session
	Details []RoundDetail `json:"details,omitempty"`
}

// SessionRequest starts a session.
type SessionRequest struct {
	// ProjectRoot is a project the daemon tracks. Required.
	ProjectRoot string `json:"project_root"`
	// Task is what to implement. Required.
	Task string `json:"task"`
	// PromptsDir is a directory of review prompts relative to the project root;
	// empty means .chunk/reviews, which may be missing when validation commands
	// are enough to check the work. Absolute paths and paths that leave the
	// project are refused.
	PromptsDir  string `json:"prompts_dir,omitempty"`
	Parallelism int    `json:"parallelism,omitempty"`
	Model       string `json:"model,omitempty"`
	// Image overrides the project's configured sidecar image.
	Image string `json:"image,omitempty"`
	// TimeoutSeconds bounds each review; zero means review.DefaultTimeout.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// ImplementTimeoutSeconds bounds each implementer turn; zero means
	// review.DefaultImplementTimeout.
	ImplementTimeoutSeconds int `json:"implement_timeout_seconds,omitempty"`
	// MaxRounds is the most rounds of checks to run; zero means DefaultRounds.
	// More than MaxRounds is refused.
	MaxRounds int `json:"max_rounds,omitempty"`
	// NoValidate leaves the project's validation commands out of the checks.
	NoValidate bool `json:"no_validate,omitempty"`
}

// SessionStartResponse answers an accepted POST /session.
type SessionStartResponse struct {
	ID string `json:"id"`
}

// SessionList answers GET /session.
type SessionList struct {
	Sessions []Session `json:"sessions"`
}
