package chunkd

import (
	"fmt"
	"strings"
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
	// StageFactoryLoop is a factory run's loop: implement, then review and
	// validate until the checks pass.
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
	// Note says more about the state: why a loop ended, what failed.
	Note string `json:"note,omitempty"`
}

// RoundState is where one round stands.
type RoundState string

// Round states.
const (
	// RoundStarted is a round that has begun and not yet said which half it
	// is in.
	RoundStarted RoundState = "started"
	// RoundImplementing and RoundChecking are a factory round's halves: the
	// implementer's turn, then the reviews and validation commands.
	RoundImplementing RoundState = "implementing"
	RoundChecking     RoundState = "checking"
	RoundDone         RoundState = "done"
	RoundFailed       RoundState = "failed"
)

// ImplementState is where a factory round's implementer turn stands.
type ImplementState string

// Implement states.
const (
	ImplementRunning ImplementState = "running"
	ImplementApplied ImplementState = "applied"
	// ImplementEmpty means the agent made no changes.
	ImplementEmpty  ImplementState = "empty"
	ImplementFailed ImplementState = "failed"
)

// RoundImplement is a factory round's implementer turn.
type RoundImplement struct {
	State      ImplementState `json:"state"`
	DurationMS int64          `json:"duration_ms,omitempty"`
	CostUSD    float64        `json:"cost_usd,omitempty"`
	// Summary is the implementer's own account of the turn.
	Summary string `json:"summary,omitempty"`
	// Stat summarizes the work so far against the run's baseline.
	Stat  string `json:"stat,omitempty"`
	Error string `json:"error,omitempty"`
	// Progress is what the turn has done so far, for a display to show while
	// it runs.
	Progress ImplementProgress `json:"progress,omitzero"`
}

// ImplementProgress is what an implementer turn has done so far. Each field
// holds only the latest of its kind, so it stays the same size however long
// the turn runs; the run's log keeps everything.
type ImplementProgress struct {
	// Doing is the last thing the implementer did, said short: a command's
	// own description of itself, or the tool and what it acted on.
	Doing string `json:"doing,omitempty"`
	// Said is the first line of the last thing the implementer wrote.
	Said string `json:"said,omitempty"`
	// Actions counts the tools the implementer has used.
	Actions int `json:"actions,omitempty"`
	// At is when the implementer last did or said anything.
	At time.Time `json:"at,omitzero"`
}

// Feed is the latest lines of a stream that can run long, such as a run's
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

// RoundCheck is one validation command a factory round ran.
type RoundCheck struct {
	Name string `json:"name"`
	// Status is one of the Check values.
	Status     string `json:"status"`
	SidecarID  string `json:"sidecar_id,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Error      string `json:"error,omitempty"`
	// Output is the end of a failed command's output.
	Output string `json:"output,omitempty"`
}

// FactoryRun is what a factory session works on and where its work is.
type FactoryRun struct {
	// Prompt is the request. A continued run's is the earlier run's, and
	// Guidance is what the user added to it, if anything.
	Prompt   string `json:"prompt"`
	Guidance string `json:"guidance,omitempty"`
	// ContinuesRunID is the run whose work this one picked up, if it did.
	ContinuesRunID string `json:"continues_run_id,omitempty"`
	// SpecOrigin is the path of the file Prompt was read from, when it was a
	// file rather than literal text given on the command line or stdin, or
	// "factory plan ID" for a plan sent from the planning screen.
	SpecOrigin string `json:"spec_origin,omitempty"`
	// Attempts is the most rounds the run checks.
	Attempts int    `json:"attempts,omitempty"`
	RunID    string `json:"run_id,omitempty"`
	// PlanID is set for a plan build: a session that builds factory plan
	// PlanID through factory's own tasks, in a build session of Claude Code,
	// rather than in rounds of its own. Such a session has no rounds; its
	// Progress is what the build session does, and PlanState where factory
	// has the plan.
	PlanID    string `json:"plan_id,omitempty"`
	PlanState string `json:"plan_state,omitempty"`
	// SidecarIDs are the live or retained members of this run's pool. The
	// daemon derives ownership from its pool state so clients do not have to
	// reconstruct it from sidecar names.
	SidecarIDs []string `json:"sidecar_ids,omitempty"`
	// Worktree is where the work is, on Branch. Baseline is the commit the
	// branch starts from and Head the user's HEAD when the run started; they
	// differ when the user's uncommitted work was committed as the baseline.
	Worktree string `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Baseline string `json:"baseline,omitempty"`
	Head     string `json:"head,omitempty"`
	// Result is why the loop stopped, one of the Result values, once it has,
	// and Rounds how many rounds were checked.
	Result string `json:"result,omitempty"`
	Rounds int    `json:"rounds,omitempty"`
	// Committed reports whether the work was committed on Branch.
	Committed bool `json:"committed,omitempty"`
	// Stat is the committed work's diff stat against Baseline, "" when there
	// were no changes. StatError says why it could not be worked out instead.
	Stat      string `json:"stat,omitempty"`
	StatError string `json:"stat_error,omitempty"`
	// WorktreeRemoved reports that the run ended before the implementer started
	// and its worktree, holding nothing, was removed.
	WorktreeRemoved bool `json:"worktree_removed,omitempty"`
	// KeptSidecars are the sidecars left running at the user's request.
	KeptSidecars []string `json:"kept_sidecars,omitempty"`
	// Log is the path of the run's log, once the run has ended, if it kept one.
	Log string `json:"log,omitempty"`
	// Progress is what the run said as it went: its sidecars being made
	// ready and synced, and anything that went wrong cleaning up.
	Progress Feed `json:"progress,omitzero"`
	// PullRequest is the pull request the run opens once its checks pass, nil
	// only when it was told not to open one.
	PullRequest *PullRequest `json:"pull_request,omitempty"`
}

// PullRequest is where a run's pull request has got to.
type PullRequest struct {
	// State is one of the PR constants.
	State string `json:"state"`
	// Draft is whether it is opened as a draft.
	Draft bool `json:"draft,omitempty"`
	// Base is the branch it targets, once known.
	Base string `json:"base,omitempty"`
	// URL and Number name the pull request once it is open.
	URL    string `json:"url,omitempty"`
	Number int    `json:"number,omitempty"`
	// Message says why no pull request was tried (PRSkipped) or why it could
	// not be opened (PRFailed).
	Message string `json:"message,omitempty"`
	// Status is where the open pull request stands with its reviewers, one of
	// the PRStatus constants, "" until the daemon has looked it up. It is not
	// State: that is how far opening it got, this is what became of it.
	Status string `json:"status,omitempty"`
}

// Opened reports whether the pull request is open, which is what makes its URL
// and number worth showing.
func (pr *PullRequest) Opened() bool {
	return pr != nil && pr.State == PROpened && pr.URL != ""
}

// Where an open pull request stands, as PullRequest.Status.
const (
	// PRStatusUp means it is open and nobody has reviewed it or been asked to.
	PRStatusUp = "up"
	// PRStatusInReview means a review has been asked for or has begun.
	PRStatusInReview = "in_review"
	// PRStatusFeedback means a reviewer asked for changes or left comments
	// still to be resolved.
	PRStatusFeedback = "feedback"
	// PRStatusChecksFailing means its checks on GitHub are failing.
	PRStatusChecksFailing = "checks_failing"
	// PRStatusMerged means it was merged.
	PRStatusMerged = "merged"
	// PRStatusClosed means it was closed without being merged.
	PRStatusClosed = "closed"
)

// Settled reports whether the pull request's status can no longer change, so
// there is no point asking GitHub about it again.
func (pr *PullRequest) Settled() bool {
	return pr != nil && (pr.Status == PRStatusMerged || pr.Status == PRStatusClosed)
}

// How far a run's pull request has got, as PullRequest.State.
const (
	// PRPending means the run has not ended, so nothing has been pushed yet.
	PRPending = "pending"
	// PRPushing means the run's branch is being pushed.
	PRPushing = "pushing"
	// PROpening means the branch is pushed and the pull request is being opened.
	PROpening = "opening"
	// PROpened means the pull request is open.
	PROpened = "opened"
	// PRFailed means the run passed but the pull request could not be opened.
	// Its work is still committed, and pushed if it got that far.
	PRFailed = "failed"
	// PRSkipped means the run did not end in a way that earns a pull request,
	// or the project has nothing to open one with.
	PRSkipped = "skipped"
)

// What a run does with its branch once every check has passed, as
// FactoryRequest.PullRequest.
const (
	// PullRequestAuto, the default, opens one when the project can. A project
	// with no GitHub token or an origin that is not on GitHub still runs, and
	// says why it opened none: the work is worth doing either way.
	PullRequestAuto = ""
	// PullRequestRequired opens one too, and refuses a run that could not,
	// before it starts. It is what someone who asked for a pull request in so
	// many words means.
	PullRequestRequired = "required"
	// PullRequestNone leaves the branch alone: nothing is pushed.
	PullRequestNone = "none"
)

// Why a factory run's loop stopped, as FactoryRun.Result.
const (
	// ResultPassed means every check passed.
	ResultPassed = "passed"
	// ResultExhausted means checks still failed when attempts ran out.
	ResultExhausted = "exhausted"
	// ResultStuck means the implementer stopped changing the code with checks
	// still failing.
	ResultStuck = "stuck"
	// ResultNoChange means the implementer's work is empty.
	ResultNoChange = "no_change"
)

// Continuable reports whether the run stopped with committed work that has
// not passed its checks, which a later run can pick up: checks still failing
// when the loop ended, or a run that errored or was cancelled part way, after
// its implementer had made changes. Committed is only set once the run has
// ended, so a run still going is never continuable.
func (f *FactoryRun) Continuable() bool {
	unfinished := f.Result != ResultPassed && f.Result != ResultNoChange
	// A run that stopped with no result may have committed nothing: Committed
	// is set even when there was nothing to commit. A stat that could not be
	// worked out may still hide work.
	if f.Result == "" && f.Stat == "" && f.StatError == "" {
		return false
	}
	return unfinished && f.Committed && f.Branch != ""
}

// ContinueID is the run a continued run reads the record of. The branch ends
// in the ID of the first run in a chain of them.
func (f *FactoryRun) ContinueID() string {
	id := f.Branch
	if i := strings.LastIndexByte(id, '/'); i >= 0 {
		id = id[i+1:]
	}
	return id
}

// ContinueCommand is the command that carries the run on.
func (f *FactoryRun) ContinueCommand() string {
	return "chunk factory build --continue " + f.ContinueID()
}

// How a check came out, as ReviewResult.Status and RoundCheck.Status.
const (
	// CheckPassed means the check ran and found nothing to fix.
	CheckPassed = "passed"
	// CheckFailed means the check ran and found something to fix.
	CheckFailed = "failed"
	// CheckErrored means the check could not run.
	CheckErrored = "errored"
	// CheckQueued and CheckRunning are a RoundCheck's states before it has an
	// outcome: its round has not reached it yet, or it is running now.
	CheckQueued  = "queued"
	CheckRunning = "running"
)

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
	// Findings counts the structured findings parsed from this review's output,
	// and Worth the ones worth changing (severity high or medium).
	Findings int `json:"findings,omitempty"`
	Worth    int `json:"worth,omitempty"`
	// Status is how the review came out as a check, one of the Check values:
	// passed unless it found something worth changing, failed if it did,
	// errored if it could not run. Empty until it is known.
	Status string `json:"status,omitempty"`
}

// PromptRunState is where one review of a round stands. The values are
// spelled out so they read in JSON and survive the daemon's own enum being
// reordered.
type PromptRunState string

// Review states.
const (
	PromptQueued  PromptRunState = "queued"
	PromptRunning PromptRunState = "running"
	PromptDone    PromptRunState = "done"
	PromptFailed  PromptRunState = "failed"
)

// Round is one pass of implementing the work and checking it.
type Round struct {
	Number  int            `json:"number"`
	State   RoundState     `json:"state"`
	Reviews []ReviewPrompt `json:"reviews"`
	// Findings counts every finding the round's reviews reported; Worth counts
	// the ones worth changing (severity high or medium).
	Findings int `json:"findings"`
	Worth    int `json:"worth"`
	// Implement and Checks are a factory round's implementer turn and
	// validation commands. Its reviews are in Reviews.
	Implement *RoundImplement `json:"implement,omitempty"`
	Checks    []RoundCheck    `json:"checks,omitempty"`
	// Note says why the round ended the way it did, such as why the loop stopped.
	Note      string     `json:"note,omitempty"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// Session is the record of one factory run. It holds state only; text is in
// SessionDetail.
type Session struct {
	ID          string `json:"id"`
	ProjectRoot string `json:"project_root"`
	Branch      string `json:"branch,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`

	State SessionState `json:"state"`
	Error string       `json:"error,omitempty"`

	// Stages is the session's stages, in order. The only one is its factory loop.
	Stages []Stage `json:"stages"`
	Rounds []Round `json:"rounds"`
	// Factory is what the run works on and where its work is.
	Factory *FactoryRun `json:"factory,omitempty"`

	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// ReviewResult is what one review found.
type ReviewResult struct {
	Prompt     string `json:"prompt"`
	SidecarID  string `json:"sidecar_id,omitempty"`
	Error      string `json:"error,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	// Findings are the structured findings the review gave, each with an ID
	// unique within the round. Empty when it gave none or failed; Error tells
	// the two apart.
	Findings []Finding `json:"findings,omitempty"`
	// Status is how a factory review came out as a check, one of the Check
	// values.
	Status string `json:"status,omitempty"`
}

// Finding is one problem a review reported.
type Finding struct {
	// ID is unique within a round.
	ID string `json:"id,omitempty"`
	// Prompt names the review that reported it.
	Prompt string `json:"prompt,omitempty"`
	// File is a repository-relative path with forward slashes.
	File string `json:"file"`
	// Line is the 1-based line the finding is about; zero means the file as a
	// whole or a line the reviewer did not give.
	Line     int    `json:"line,omitempty"`
	Severity string `json:"severity"`
	Body     string `json:"body"`
	// Patch is an optional unified diff that would fix the finding.
	Patch string `json:"patch,omitempty"`
}

// Location is where the finding is, as file:line, or the file alone when the
// finding has no line.
func (f Finding) Location() string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
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

// FactoryRequest starts a factory session.
type FactoryRequest struct {
	// ProjectRoot is a project the daemon tracks. Required.
	ProjectRoot string `json:"project_root"`
	// Prompt is what the implementer is asked to do. Required unless Continue
	// is set, when it is optional guidance added to the earlier run's request.
	Prompt string `json:"prompt"`
	// SpecOrigin is the path Prompt was read from, when the prompt argument
	// named a file rather than being literal text, or "factory plan ID" for a
	// plan sent from the planning screen.
	SpecOrigin string `json:"spec_origin,omitempty"`
	// Continue is the ID of an earlier run to pick up the work of, or empty to
	// start from the project's files.
	Continue string `json:"continue,omitempty"`
	// ReviewsDir is a directory of review prompts relative to the project
	// root; empty means .chunk/reviews, which may be missing when validation
	// commands are enough.
	ReviewsDir string `json:"reviews_dir,omitempty"`
	NoValidate bool   `json:"no_validate,omitempty"`
	// Attempts is the most rounds to check; zero means DefaultAttempts.
	Attempts int `json:"attempts,omitempty"`
	// Reviewers is how many reviewer sidecars to run; zero means one per
	// review prompt.
	Reviewers               int    `json:"reviewers,omitempty"`
	Model                   string `json:"model,omitempty"`
	ImplementerInstructions string `json:"implementer_instructions,omitempty"`
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
	// PullRequest says what to do with the run's branch once every check has
	// passed: one of PullRequestAuto, PullRequestRequired or PullRequestNone.
	// Draft opens the pull request as a draft.
	PullRequest string `json:"pull_request,omitempty"`
	Draft       bool   `json:"draft,omitempty"`
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

// WarmRequest asks the daemon to make the sidecars a factory run continuing
// RunID would use, before that run is started, so it can start without
// waiting for them.
type WarmRequest struct {
	ProjectRoot string `json:"project_root"`
	RunID       string `json:"run_id"`
}

// SessionStartResponse answers an accepted POST /factory.
type SessionStartResponse struct {
	ID string `json:"id"`
}

// SessionList answers GET /factory.
type SessionList struct {
	Sessions []Session `json:"sessions"`
}
