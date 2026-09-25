package review

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
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

// exitNotFound is the shell's exit code when a command is not on PATH.
const exitNotFound = 127

// ErrClaudeMissing is returned when a sidecar has no claude binary.
var ErrClaudeMissing = errors.New("claude is not installed on the sidecar")

// allowedTools limits reviewers to reading the repository. A review reports
// findings; it has no business editing the tree the next pass reviews.
var allowedTools = []string{
	"Read", "Grep", "Glob",
	"Bash(git diff:*)", "Bash(git log:*)", "Bash(git show:*)", "Bash(git status:*)",
}

// Options configures one review pass.
type Options struct {
	APIKey   string
	Model    string        // optional; claude's default when empty
	Timeout  time.Duration // per review; DefaultTimeout when zero
	StatusFn iostream.StatusFunc
}

// Result is the outcome of one prompt in one pass. Output and Error are not
// exclusive: a review that fails partway keeps what it produced.
type Result struct {
	Prompt    string
	SidecarID string
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

// RunPass runs every prompt once, each on a sidecar checked out from pool, and
// returns results in prompt order for every prompt that started. Per-prompt
// failures are recorded in Result.Error; the returned error is for failures
// that stop the pass itself.
//
// A missing claude binary stops the pass rather than being recorded per
// prompt: every sidecar in a pool comes from one image, so it would fail every
// review the same way.
func RunPass(ctx context.Context, pool Acquirer, exec Execer, prompts []Prompt, opts Options) ([]Result, error) {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	status := opts.StatusFn
	if status == nil {
		status = func(iostream.Level, string) {}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make([]runResult, len(prompts))
	var wg sync.WaitGroup
	var acquireErr error
	for i, p := range prompts {
		entry, err := pool.Acquire(ctx)
		if err != nil {
			acquireErr = fmt.Errorf("acquire sidecar for %s: %w", p.Name, err)
			break
		}
		wg.Add(1)
		go func(i int, p Prompt, entry *sidecar.PoolEntry) {
			defer wg.Done()
			defer pool.Release(entry)
			status(iostream.LevelInfo, fmt.Sprintf("reviewing %s on %s", p.Name, entry.ID))
			r := runOne(ctx, exec, entry, p, opts)
			results[i] = r
			switch {
			case errors.Is(r.err, ErrClaudeMissing):
				cancel()
			case r.err != nil:
				status(iostream.LevelWarn, fmt.Sprintf("%s: %s", p.Name, r.Error))
			default:
				status(iostream.LevelDone, fmt.Sprintf("%s reviewed in %s", p.Name, r.Duration.Round(time.Second)))
			}
		}(i, p, entry)
	}
	wg.Wait()

	out := make([]Result, 0, len(results))
	for _, r := range results {
		if errors.Is(r.err, ErrClaudeMissing) {
			return nil, ErrClaudeMissing
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
	code, err := exec(ctx, entry, claudeScript(entry.RepoPath, p.Body, opts.Model), map[string]string{
		"ANTHROPIC_API_KEY": opts.APIKey,
	}, func(stream string, data []byte) {
		buf := &stdout
		if stream == circleci.StreamStderr {
			buf = &stderr
		}
		if buf.Len() < maxOutputBytes {
			buf.Write(data[:min(len(data), maxOutputBytes-buf.Len())])
		}
	})
	r.Output = strings.TrimSpace(stdout.String())
	r.Duration = time.Since(start)
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		return r.fail(fmt.Errorf("timed out after %s", opts.Timeout))
	case err != nil:
		return r.fail(fmt.Errorf("exec: %w", err))
	case code == exitNotFound:
		return r.fail(ErrClaudeMissing)
	case code != 0:
		return r.fail(exitError(code, stderr.String()))
	}
	return r
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

// claudeScript builds the shell script that runs one review. The prompt is
// piped in base64-encoded, so no quoting in it can reach the shell. Claude
// Code's native installer puts claude in ~/.local/bin, which a non-login sh
// does not have on PATH.
func claudeScript(repoPath, prompt, model string) string {
	args := []string{"claude", "-p", "--output-format", "text", "--allowedTools", strings.Join(allowedTools, ",")}
	if model != "" {
		args = append(args, "--model", model)
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(prompt))
	return fmt.Sprintf(`export PATH="$HOME/.local/bin:$PATH"
command -v claude >/dev/null 2>&1 || exit %d
cd %s && echo %s | base64 -d | %s`,
		exitNotFound, sidecar.ShellEscape(repoPath), encoded, sidecar.ShellJoin(args))
}
