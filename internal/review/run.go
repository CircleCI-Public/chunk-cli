package review

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// DefaultTimeout bounds one review. Claude explores the repo before answering,
// so this is minutes rather than the seconds a single API call would take.
const DefaultTimeout = 15 * time.Minute

// maxOutputBytes caps what is kept of one review's output. A review is prose,
// so hitting this means something went wrong, not that the review was thorough.
const maxOutputBytes = 256 * 1024

// maxStructuredOutputBytes caps claude's JSON result when structured findings
// are wanted. It is larger than maxOutputBytes because the result carries the
// answer twice, as text and as structured_output, and up to MaxFindings patches;
// a truncated result would not decode at all.
const maxStructuredOutputBytes = 8 << 20

// ExitClaudeMissing is the exit code a script running claude on a sidecar uses
// for claude not being on PATH, so every caller reports it the same way. Not
// the shell's own 127, which claude also exits with when something it shelled
// out to is missing: that is one broken review, not a dead pass.
const ExitClaudeMissing = 97

// ErrClaudeMissing is returned when a sidecar has no claude binary.
var ErrClaudeMissing = errors.New("claude is not installed on the sidecar")

// ErrCredentialRejected is returned when Anthropic rejects the credential.
var ErrCredentialRejected = errors.New("anthropic rejected the credential")

// credentialRejectedRe matches claude's own authentication failure, which reads
// the same for a revoked API key and a stale subscription token.
var credentialRejectedRe = regexp.MustCompile(`(?i)failed to authenticate.*\b401\b`)

// fatal reports whether an error fails every review the same way, so the pass
// should stop rather than run the rest and report identical failures. Every
// sidecar in a pool shares one image and one credential.
func fatal(err error) bool {
	return errors.Is(err, ErrClaudeMissing) || errors.Is(err, ErrCredentialRejected)
}

// Credential is the Claude credential a review authenticates with. EnvVar is
// the variable claude reads it from, so only one of the two is ever sent and a
// stale one cannot shadow the other.
type Credential struct {
	EnvVar string
	Value  string
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
	Credential Credential
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

// Execer runs a shell script on a sidecar and streams its output. onSubmitted,
// when non-nil, is called with the command ID between submission and streaming.
type Execer func(ctx context.Context, entry *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(commandID string)) (exitCode int, err error)

// ClientExec runs scripts through the pool entry's CircleCI client. Submit and
// stream are kept apart so onSubmitted sees the command ID: the caller needs it
// before the command ends, not after.
func ClientExec(ctx context.Context, entry *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(string)) (int, error) {
	commandID, err := entry.Client.SubmitExec(ctx, entry.ID, "sh", []string{"-c", script}, env)
	if err != nil {
		return 0, fmt.Errorf("submit: %w", err)
	}
	if onSubmitted != nil {
		onSubmitted(commandID)
	}
	res, err := entry.Client.StreamOutput(ctx, commandID, "", onOutput)
	if err != nil {
		return 0, fmt.Errorf("stream output: %w", err)
	}
	return res.ExitCode, nil
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
func RunPass(ctx context.Context, acquire func(context.Context) (*sidecar.PoolEntry, error), release func(*sidecar.PoolEntry), exec Execer, prompts []Prompt, opts Options) ([]Result, error) {
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

func runOne(ctx context.Context, exec Execer, entry *sidecar.PoolEntry, p Prompt, opts Options) runResult {
	r := runResult{Result: Result{Prompt: p.Name, SidecarID: entry.ID}}
	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	// Stdout is the review. Stderr is kept only to explain a failure, so
	// claude's progress noise never lands in the review text.
	var stdout, stderr strings.Builder
	// Only stdout carries the JSON result, so only it gets the larger cap; stderr
	// is read for a short tail and stays small.
	stdoutLimit := maxOutputBytes
	if opts.StructuredFindings {
		stdoutLimit = maxStructuredOutputBytes
	}
	stdoutCut := false
	tools := opts.AllowedTools
	if len(tools) == 0 {
		tools = allowedTools
	}
	onOutput := func(stream string, data []byte) {
		buf, limit := &stdout, stdoutLimit
		if stream == circleci.StreamStderr {
			buf, limit = &stderr, maxOutputBytes
		}
		room := max(limit-buf.Len(), 0)
		if len(data) > room && buf == &stdout {
			stdoutCut = true
		}
		buf.Write(data[:min(len(data), room)])
	}
	var onSubmitted func(string)
	if opts.OnSubmitted != nil {
		onSubmitted = func(commandID string) { opts.OnSubmitted(entry, p.Name, commandID) }
	}
	code, err := exec(ctx, entry, claudeScriptWithTools(entry.RepoPath, p.Body, opts.Model, tools, opts.StructuredFindings), Env(opts.Credential, opts.BaseURL), onOutput, onSubmitted)
	r.Output = strings.TrimSpace(stdout.String())
	r.Duration = time.Since(start)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return r.fail(fmt.Errorf("timed out after %s", opts.Timeout))
	case err != nil:
		return r.fail(fmt.Errorf("exec: %w", err))
	case code == ExitClaudeMissing:
		return r.fail(ErrClaudeMissing)
	case code != 0 && CredentialRejected(r.Output, stderr.String()):
		return r.fail(ErrCredentialRejected)
	case code != 0:
		return r.fail(exitError(code, stderr.String()))
	}
	if opts.StructuredFindings {
		// A result cut at the cap cannot decode; say why instead of reporting
		// malformed JSON.
		if stdoutCut {
			return r.fail(fmt.Errorf("claude's result is over %d bytes", stdoutLimit))
		}
		parsed, err := ParseFindings(r.Output)
		if err != nil {
			return r.fail(err)
		}
		r.Parsed = parsed
		r.Output = parsed.Prose
	}
	return r
}

// CredentialRejected reports whether claude failed to authenticate. Both
// streams are checked: the 401 lands on stdout, while stderr can carry
// unrelated warnings.
func CredentialRejected(stdout, stderr string) bool {
	return credentialRejectedRe.MatchString(stdout) || credentialRejectedRe.MatchString(stderr)
}

// stderrTail is how much of stderr a failed review reports.
const stderrTail = 2000

func exitError(code int, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return fmt.Errorf("claude exited %d", code)
	}
	if len(stderr) > stderrTail {
		stderr = "…" + stderr[len(stderr)-stderrTail:]
	}
	return fmt.Errorf("claude exited %d: %s", code, stderr)
}

// claudeScriptWithTools builds the shell script that runs one review, with an
// explicit tool allowlist and, when structured is set, claude's JSON result
// constrained by FindingsSchema. The prompt is piped in base64-encoded, so no
// quoting in it can reach the shell. Claude Code's native installer puts claude
// in ~/.local/bin, which a non-login sh does not have on PATH.
func claudeScriptWithTools(repoPath, prompt, model string, tools []string, structured bool) string {
	format := "text"
	if structured {
		format = "json"
	}
	args := []string{"claude", "-p", "--output-format", format, "--allowedTools", strings.Join(tools, ",")}
	if structured {
		args = append(args, "--json-schema", FindingsSchema)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(prompt))
	return fmt.Sprintf(`export PATH="$HOME/.local/bin:$PATH"
command -v claude >/dev/null 2>&1 || exit %d
cd %s && echo %s | base64 -d | %s`,
		ExitClaudeMissing, sidecar.ShellEscape(repoPath), encoded, sidecar.ShellJoin(args))
}

// defaultBaseURL is where claude sends requests when no base URL is set.
const defaultBaseURL = "https://api.anthropic.com"

// Env is the environment claude runs with on a sidecar: only the credential,
// and the base URL when it points somewhere other than Anthropic.
func Env(cred Credential, baseURL string) map[string]string {
	env := map[string]string{cred.EnvVar: cred.Value}
	if baseURL != "" && strings.TrimRight(baseURL, "/") != defaultBaseURL {
		env["ANTHROPIC_BASE_URL"] = baseURL
	}
	return env
}
