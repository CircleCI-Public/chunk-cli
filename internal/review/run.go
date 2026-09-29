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

// maxOutputBytes caps what is kept of one review's output. Findings are a few
// kilobytes of JSON, so hitting this means something went wrong, not that the
// review was thorough.
const maxOutputBytes = 256 * 1024

// exitClaudeMissing is the review script's exit code for claude not being on
// PATH. Not the shell's own 127, which claude also exits with when something it
// shelled out to is missing: that is one broken review, not a dead pass.
const exitClaudeMissing = 97

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
}

// Result is the outcome of one prompt in one pass. Summary and Findings are
// set for a review that succeeded. Output is what claude printed, kept only
// when a review fails, so the failure can be diagnosed.
type Result struct {
	Prompt    string
	SidecarID string
	Summary   string
	Findings  []Finding
	Output    string
	Error     string
	Duration  time.Duration
}

// Execer runs a shell script on a sidecar and streams its output.
type Execer func(ctx context.Context, entry *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn) (exitCode int, err error)

// ClientExec runs scripts through the pool entry's CircleCI client.
func ClientExec(ctx context.Context, entry *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn) (int, error) {
	res, err := entry.Client.Exec(ctx, entry.ID, "sh", []string{"-c", script}, env, onOutput)
	if err != nil {
		return 0, err
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
	var truncated bool
	code, err := exec(ctx, entry, claudeScript(entry.RepoPath, p.Body, opts.Model), claudeEnv(opts), func(stream string, data []byte) {
		buf := &stdout
		if stream == circleci.StreamStderr {
			buf = &stderr
		}
		room := maxOutputBytes - buf.Len()
		if len(data) > room && buf == &stdout {
			truncated = true
		}
		buf.Write(data[:min(len(data), max(room, 0))])
	})
	r.Duration = time.Since(start)
	output := strings.TrimSpace(stdout.String())
	// Raw output is kept only to diagnose a failed review.
	fail := func(err error) runResult {
		r.Output = output
		return r.fail(err)
	}
	if err := runErr(ctx, opts.Timeout, code, err, output, stderr.String()); err != nil {
		return fail(err)
	}
	// Cut-off JSON would otherwise surface as a baffling decode error.
	if truncated {
		return fail(fmt.Errorf("output exceeded %d bytes", maxOutputBytes))
	}
	out, err := parseReport(output)
	if err != nil {
		return fail(err)
	}
	r.Summary, r.Findings = out.Summary, out.Findings
	return r
}

// runErr is why a review's claude run failed, or nil when it exited cleanly.
func runErr(ctx context.Context, timeout time.Duration, code int, err error, stdout, stderr string) error {
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return fmt.Errorf("timed out after %s", timeout)
	case err != nil:
		return fmt.Errorf("exec: %w", err)
	case code == exitClaudeMissing:
		return ErrClaudeMissing
	case code != 0 && credentialRejected(stdout, stderr):
		return ErrCredentialRejected
	case code != 0:
		return exitError(code, stdout, stderr)
	}
	return nil
}

// credentialRejected reports whether claude failed to authenticate. Both
// streams are checked: the 401 lands on stdout, while stderr can carry
// unrelated warnings.
func credentialRejected(stdout, stderr string) bool {
	return credentialRejectedRe.MatchString(stdout) || credentialRejectedRe.MatchString(stderr)
}

// errorTail is how much of an error message a failed review reports.
const errorTail = 2000

// exitError describes a claude run that exited non-zero. Claude's JSON output
// reports its own errors, such as an overloaded API, in the result on stdout,
// so that is preferred: stderr can carry unrelated warnings. Stderr is the
// fallback for failures that happen before claude can report anything.
func exitError(code int, stdout, stderr string) error {
	var msg string
	if env, err := parseEnvelope(stdout); err == nil && env.IsError {
		msg = strings.TrimSpace(env.Result)
	}
	if msg == "" {
		msg = strings.TrimSpace(stderr)
	}
	if msg == "" {
		return fmt.Errorf("claude exited %d", code)
	}
	if len(msg) > errorTail {
		msg = "…" + msg[len(msg)-errorTail:]
	}
	return fmt.Errorf("claude exited %d: %s", code, msg)
}

// claudeScript builds the shell script that runs one review, answering with
// findings matching findingsSchema. The prompt is piped in base64-encoded, so
// no quoting in it can reach the shell. Claude Code's native installer puts
// claude in ~/.local/bin, which a non-login sh does not have on PATH.
func claudeScript(repoPath, prompt, model string) string {
	args := []string{
		"claude", "-p", "--output-format", "json", "--json-schema", findingsSchema,
		"--allowedTools", strings.Join(allowedTools, ","),
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(prompt))
	return fmt.Sprintf(`export PATH="$HOME/.local/bin:$PATH"
command -v claude >/dev/null 2>&1 || exit %d
cd %s && echo %s | base64 -d | %s`,
		exitClaudeMissing, sidecar.ShellEscape(repoPath), encoded, sidecar.ShellJoin(args))
}

// defaultBaseURL is where claude sends requests when no base URL is set.
const defaultBaseURL = "https://api.anthropic.com"

// claudeEnv is the environment each review runs with: only the credential, and
// the base URL when it points somewhere other than Anthropic.
func claudeEnv(opts Options) map[string]string {
	env := map[string]string{opts.Credential.EnvVar: opts.Credential.Value}
	if opts.BaseURL != "" && strings.TrimRight(opts.BaseURL, "/") != defaultBaseURL {
		env["ANTHROPIC_BASE_URL"] = opts.BaseURL
	}
	return env
}
