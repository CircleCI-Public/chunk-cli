package watchd

import (
	"slices"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
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
	// StageFactoryLoop is a factory run's loop, which takes the review loop's
	// place: implement, then review and validate until the checks pass.
	StageFactoryLoop StageID = "factory_loop"
	StageRebase      StageID = "rebase"
	StageCI          StageID = "ci"
	StageApproval    StageID = "approval"
	StagePR          StageID = "pr"
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

// SessionKind is what a session runs.
type SessionKind string

// Session kinds.
const (
	// KindReview reviews the user's files and applies the fixes to them. A
	// session with no kind is one of these.
	KindReview SessionKind = "review"
	// KindFactory is a factory run: an implementer works in a worktree of its
	// own, and reviews and validation check the work until it passes.
	KindFactory SessionKind = "factory"
)

// RoundState is where one review-and-fix round stands.
type RoundState string

// Round states.
const (
	RoundReviewing RoundState = "reviewing"
	// RoundImplementing and RoundChecking are a factory round's halves: the
	// implementer's turn, then the reviews and validation commands.
	RoundImplementing RoundState = "implementing"
	RoundChecking     RoundState = "checking"
	RoundFixing       RoundState = "fixing"
	RoundApplying     RoundState = "applying"
	RoundDone         RoundState = "done"
	RoundFailed       RoundState = "failed"
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

// RoundImplement is a factory round's implementer turn.
type RoundImplement struct {
	State      FixState `json:"state"`
	DurationMS int64    `json:"duration_ms,omitempty"`
	CostUSD    float64  `json:"cost_usd,omitempty"`
	// Summary is the implementer's own account of the turn.
	Summary string `json:"summary,omitempty"`
	// Stat summarizes the work so far against the run's baseline.
	Stat  string `json:"stat,omitempty"`
	Error string `json:"error,omitempty"`
	// Activity is the tools the implementer used in the turn, latest last.
	Activity Feed `json:"activity,omitzero"`
}

// Feed is the latest lines of a stream that can run long, such as a run's
// progress, and how many lines there have been, so a follower can tell which
// it has not seen. Only the last maxFeedLines are kept.
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

// maxFeedLines is how many lines a Feed keeps.
const maxFeedLines = 100

// feedLevel spells out an iostream level.
func feedLevel(l iostream.Level) FeedLevel {
	switch l {
	case iostream.LevelStep:
		return FeedStep
	case iostream.LevelWarn:
		return FeedWarn
	case iostream.LevelDone:
		return FeedDone
	case iostream.LevelError:
		return FeedError
	case iostream.LevelInfo:
	}
	return FeedInfo
}

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

func (f *Feed) add(level iostream.Level, text string) {
	f.Lines = append(f.Lines, FeedLine{Level: feedLevel(level), Text: text})
	if over := len(f.Lines) - maxFeedLines; over > 0 {
		f.Lines = slices.Delete(f.Lines, 0, over)
	}
	f.Total++
}

// Since returns the lines added after the first seen, as many as are still
// kept.
func (f Feed) Since(seen int) []FeedLine {
	n := min(max(f.Total-seen, 0), len(f.Lines))
	return f.Lines[len(f.Lines)-n:]
}

func (f Feed) clone() Feed {
	return Feed{Lines: slices.Clone(f.Lines), Total: f.Total}
}

// RoundCheck is one validation command a factory round ran.
type RoundCheck struct {
	Name string `json:"name"`
	// Status is "passed", "failed" or "errored", as factory.Status.
	Status     string `json:"status"`
	SidecarID  string `json:"sidecar_id,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
	// Output is the end of a failed command's output.
	Output string `json:"output,omitempty"`
}

// FactoryRun is what a factory session works on and where its work is.
type FactoryRun struct {
	Prompt string `json:"prompt"`
	// Attempts is the most rounds the run checks.
	Attempts int    `json:"attempts,omitempty"`
	RunID    string `json:"run_id,omitempty"`
	// Worktree is where the work is, on Branch. Baseline is the commit the
	// branch starts from and Head the user's HEAD when the run started; they
	// differ when the user's uncommitted work was committed as the baseline.
	Worktree string `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Baseline string `json:"baseline,omitempty"`
	Head     string `json:"head,omitempty"`
	// Result is why the loop stopped, as factory.Result, once it has, and
	// Rounds how many rounds were checked, as factory.Outcome.Rounds.
	Result string `json:"result,omitempty"`
	Rounds int    `json:"rounds,omitempty"`
	// Committed reports whether the work was committed on Branch.
	Committed bool `json:"committed,omitempty"`
	// KeptSidecars are the sidecars left running at the user's request.
	KeptSidecars []string `json:"kept_sidecars,omitempty"`
	// Log is the path of the run's log, once the run has ended, if it kept one.
	Log string `json:"log,omitempty"`
	// Progress is what the run said as it went: its sidecars being made
	// ready and synced, and anything that went wrong cleaning up.
	Progress Feed `json:"progress,omitzero"`
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
	// Implement and Checks are a factory round's implementer turn and
	// validation commands. Its reviews are in Reviews.
	Implement *RoundImplement `json:"implement,omitempty"`
	Checks    []RoundCheck    `json:"checks,omitempty"`
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
	ID string `json:"id"`
	// Kind is what the session runs; empty means KindReview.
	Kind        SessionKind `json:"kind,omitempty"`
	ProjectRoot string      `json:"project_root"`
	Branch      string      `json:"branch,omitempty"`
	HeadSHA     string      `json:"head_sha,omitempty"`

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
	// Factory is set on a factory session.
	Factory *FactoryRun `json:"factory,omitempty"`

	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// IsFactory reports whether the session is a factory run.
func (s Session) IsFactory() bool { return s.Kind == KindFactory }

// loopStage is the stage the session's loop is.
func (s Session) loopStage() StageID {
	if s.IsFactory() {
		return StageFactoryLoop
	}
	return StageReviewLoop
}

// newStages returns the full flow for a session that is starting: its loop
// running and every later stage not built.
func newStages(loop StageID) []Stage {
	return []Stage{
		{ID: loop, State: StageRunning},
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
	// Status is how a factory review came out as a check: "passed", "failed"
	// or "errored", as factory.Status.
	Status string `json:"status,omitempty"`
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

// FactoryRequest starts a factory session.
type FactoryRequest struct {
	// ProjectRoot is a project the daemon tracks. Required.
	ProjectRoot string `json:"project_root"`
	// Prompt is what the implementer is asked to do. Required.
	Prompt string `json:"prompt"`
	// ReviewsDir is a directory of review prompts relative to the project
	// root; empty means .chunk/reviews, which may be missing when validation
	// commands are enough.
	ReviewsDir string `json:"reviews_dir,omitempty"`
	NoValidate bool   `json:"no_validate,omitempty"`
	// Attempts is the most rounds to check; zero means DefaultAttempts.
	Attempts int `json:"attempts,omitempty"`
	// Reviewers is how many reviewer sidecars to run; zero means one per
	// review prompt.
	Reviewers int    `json:"reviewers,omitempty"`
	Model     string `json:"model,omitempty"`
	// Zero timeouts mean the factory's defaults.
	ImplementTimeoutSeconds int  `json:"implement_timeout_seconds,omitempty"`
	ReviewTimeoutSeconds    int  `json:"review_timeout_seconds,omitempty"`
	KeepSidecars            bool `json:"keep_sidecars,omitempty"`
	// OrgID and Image override the project's configured ones.
	OrgID string `json:"org_id,omitempty"`
	Image string `json:"image,omitempty"`
	// Log is where the run keeps a plain-text log of its full context: an
	// absolute path, factory.LogDefault, or empty for none. Verbose adds more
	// to it and implies it; see factory.RunOptions.
	Log     string `json:"log,omitempty"`
	Verbose bool   `json:"verbose,omitempty"`
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
