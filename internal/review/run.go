package review

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/claudecode"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// DefaultTimeout bounds one review. Claude explores the repo before answering,
// so this is minutes rather than the seconds a single API call would take.
const DefaultTimeout = 15 * time.Minute

// fatal reports whether an error fails every review the same way, so the pass
// should stop rather than run the rest and report identical failures. Every
// sidecar in a pool shares one image and one credential.
func fatal(err error) bool {
	return errors.Is(err, claudecode.ErrMissing) || errors.Is(err, claudecode.ErrCredentialRejected)
}

// allowedTools limits reviewers to reading the repository. A review reports
// findings; it has no business editing the tree the next pass reviews.
var allowedTools = []string{
	"Read", "Grep", "Glob",
	"Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)", "Bash(git status:*)",
}

// EditTools is the tool set of a run that fixes code rather than reviewing it:
// everything a review can do plus Edit and Write. It is used only for the apply
// pass, whose result is a diff that a person reads before anything leaves the
// daemon, and never for a review.
var EditTools = []string{
	"Read", "Grep", "Glob", "Edit", "Write",
	"Bash(git diff:*)", "Bash(git status:*)",
}

// PromptState is the lifecycle state of one review in a pass.
type PromptState int

// Prompt lifecycle states.
const (
	StateQueued  PromptState = iota // waiting for a sidecar
	StateRunning                    // executing on a sidecar
	StateDone                       // completed successfully
	StateFailed                     // completed with error
)

// ProgressEvent reports a state change for one prompt.
type ProgressEvent struct {
	Prompt    string
	SidecarID string
	State     PromptState
	Duration  time.Duration
	Error     string
}

// Options configures one review pass.
type Options struct {
	Credential claudecode.Credential
	// BaseURL is forwarded to claude when it is not Anthropic's own, so a
	// credential issued by a gateway is sent to that gateway.
	BaseURL    string
	Model      string        // optional; claude's default when empty
	Timeout    time.Duration // per review; DefaultTimeout when zero
	StatusFn   iostream.StatusFunc
	ProgressFn func(ProgressEvent) // optional; called on each prompt state change
	// StructuredFindings runs each review with FindingsSchema as its
	// --json-schema, parses the findings into Result.Parsed and puts the prose
	// in Result.Output. A review whose answer has no structured output fails.
	// Off, Result.Parsed stays empty.
	StructuredFindings bool
	// AllowedTools overrides the read-only tool set. Empty means read-only, which
	// is what every review uses.
	AllowedTools []string
	// OnSubmitted is called once per prompt with the remote command ID, as soon
	// as the exec is accepted and before its output is streamed. It is how a
	// caller registers the run for output replay, which has to happen while the
	// command is still in flight.
	OnSubmitted func(entry *sidecar.PoolEntry, prompt, commandID string)
}

// Result is the outcome of one prompt in one pass. Output and Error are not
// exclusive: a review that fails partway keeps what it produced.
type Result struct {
	Prompt    string
	SidecarID string
	Output    string
	Error     string
	Duration  time.Duration
	// Parsed holds the structured findings when Options.StructuredFindings is
	// set.
	Parsed Parsed
}

// RunPass runs every prompt once, each on a sidecar checked out with acquire
// and returned with release (the pool's Acquire and Release), and returns
// results in prompt order for every prompt that started. Per-prompt failures
// are recorded in Result.Error; the returned error is for failures that stop
// the pass itself.
//
// A missing claude binary or a rejected credential stops the pass rather than
// being recorded per prompt: every sidecar in a pool shares one image and one
// credential, so either would fail every review the same way.
func RunPass(ctx context.Context, acquire func(context.Context) (*sidecar.PoolEntry, error), release func(*sidecar.PoolEntry), exec sidecar.Execer, prompts []Prompt, opts Options) ([]Result, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	status := opts.StatusFn
	if status == nil {
		status = func(iostream.Level, string) {}
	}

	progress := opts.ProgressFn
	if progress == nil {
		progress = func(ProgressEvent) {}
	}
	// Emit queued state for all prompts so callers have a complete initial list.
	for _, p := range prompts {
		progress(ProgressEvent{Prompt: p.Name, State: StateQueued})
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// The first fatal error, so reviews canceled because of it report it
	// rather than a bare "context canceled".
	var fatalErr atomic.Pointer[error]

	results := make([]runResult, len(prompts))
	var wg sync.WaitGroup
	var acquireErr error
	for i, p := range prompts {
		entry, err := acquire(ctx)
		if err != nil {
			acquireErr = fmt.Errorf("acquire sidecar for %s: %w", p.Name, err)
			break
		}
		wg.Add(1)
		go func(i int, p Prompt, entry *sidecar.PoolEntry) {
			defer wg.Done()
			defer release(entry)
			status(iostream.LevelInfo, fmt.Sprintf("reviewing %s on %s", p.Name, entry.ID))
			progress(ProgressEvent{Prompt: p.Name, SidecarID: entry.ID, State: StateRunning})
			r := runOne(ctx, exec, entry, p, opts)
			// If context was canceled because another review hit a fatal error,
			// attribute this result to the same root cause so no spurious
			// "context canceled" failures are shown alongside the real error.
			if r.err != nil && !fatal(r.err) && errors.Is(r.err, context.Canceled) {
				if ferr := fatalErr.Load(); ferr != nil {
					r = r.fail(*ferr)
				}
			}
			results[i] = r
			switch {
			case fatal(r.err):
				fatalErr.CompareAndSwap(nil, &r.err)
				cancel()
			case r.err != nil:
				progress(ProgressEvent{Prompt: p.Name, SidecarID: entry.ID, State: StateFailed, Duration: r.Duration, Error: r.Error})
				status(iostream.LevelWarn, fmt.Sprintf("%s: %s", p.Name, r.Error))
			default:
				progress(ProgressEvent{Prompt: p.Name, SidecarID: entry.ID, State: StateDone, Duration: r.Duration})
				status(iostream.LevelDone, fmt.Sprintf("%s reviewed in %s", p.Name, r.Duration.Round(time.Second)))
			}
		}(i, p, entry)
	}
	wg.Wait()

	out := make([]Result, 0, len(results))
	for _, r := range results {
		if fatal(r.err) {
			return nil, r.err
		}
		// Prompts never started, because acquiring a sidecar failed, have no
		// result to report.
		if r.Prompt != "" {
			out = append(out, r.Result)
		}
	}
	return out, acquireErr
}

type runResult struct {
	Result
	err error
}

func (r runResult) fail(err error) runResult {
	r.err = err
	r.Error = err.Error()
	return r
}

func runOne(ctx context.Context, exec sidecar.Execer, entry *sidecar.PoolEntry, p Prompt, opts Options) runResult {
	r := runResult{Result: Result{Prompt: p.Name, SidecarID: entry.ID}}
	tools := opts.AllowedTools
	if len(tools) == 0 {
		tools = allowedTools
	}
	run := claudecode.Options{
		Credential: opts.Credential,
		BaseURL:    opts.BaseURL,
		Model:      opts.Model,
		Timeout:    opts.Timeout,
		Tools:      tools,
	}
	if opts.StructuredFindings {
		run.Schema = FindingsSchema
	}
	if opts.OnSubmitted != nil {
		run.OnSubmitted = func(commandID string) { opts.OnSubmitted(entry, p.Name, commandID) }
	}
	turn, err := claudecode.Run(ctx, exec, entry, p.Body, run)
	r.Output = turn.Output
	r.Duration = turn.Duration
	if err != nil {
		return r.fail(err)
	}
	if opts.StructuredFindings {
		parsed, err := ParseFindings(r.Output)
		if err != nil {
			return r.fail(err)
		}
		r.Parsed = parsed
		r.Output = parsed.Prose
	}
	return r
}

// maxStderrBytes caps the stderr kept from a bookkeeping script; only its tail
// is reported.
const maxStderrBytes = 256 * 1024

// stderrTail is how much of stderr a failed command reports.
const stderrTail = 2000

// Tail keeps the last n bytes of s, marking the cut. The end is kept because
// that is where compilers, test runners and claude say what went wrong.
func Tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
