package mutate

import (
	"context"
	"encoding/base64"
	"fmt"
	"sync"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// DefaultTestTimeout caps how long a single mutation's test run may stream
// output before the mutation is marked as an infrastructure failure.
const DefaultTestTimeout = 3 * time.Minute

const cleanupTimeout = 30 * time.Second

// maxOutputBytes bounds the amount of test output retained per mutation.
// The mutate runner may execute many tests concurrently, so this needs to stay
// small enough that N parallel mutations do not exhaust local memory.
const maxOutputBytes = 256 << 10 // 256 KB

// Result is the outcome of running a single mutation against the test suite.
type Result struct {
	Mutation Mutation
	Killed   bool   // true if the test suite caught the mutation
	Output   string // combined test output (may be truncated)
	Error    string // non-empty if setup failed (patch apply, reset, etc.)
}

// Run computes a tiny per-mutation patch for each candidate, then dispatches
// test runs to the sidecar pool in parallel.
func Run(
	ctx context.Context,
	pool *sidecar.Pool,
	mutations []Mutation,
	workDir, testCmd string,
	testTimeout time.Duration,
	status iostream.StatusFunc,
) ([]Result, error) {
	return runWith(ctx, mutations, workDir, testCmd, testTimeout, status, runnerFuncs{
		acquire:  pool.Acquire,
		release:  pool.Release,
		replace:  pool.Replace,
		baseline: validateBaseline,
		patch:    MutationPatch,
		run:      runOnSidecar,
	})
}

type runnerFuncs struct {
	acquire  func(context.Context) (*sidecar.PoolEntry, error)
	release  func(*sidecar.PoolEntry)
	replace  func(context.Context, *sidecar.PoolEntry, iostream.StatusFunc) error
	baseline func(context.Context, *sidecar.PoolEntry, string, time.Duration) error
	patch    func(string, Mutation) ([]byte, error)
	run      func(context.Context, *sidecar.PoolEntry, []byte, string, time.Duration) (bool, string, string, bool)
}

func runWith(
	ctx context.Context,
	mutations []Mutation,
	workDir, testCmd string,
	testTimeout time.Duration,
	status iostream.StatusFunc,
	fn runnerFuncs,
) ([]Result, error) {
	if testTimeout <= 0 {
		testTimeout = DefaultTestTimeout
	}

	status(iostream.LevelInfo, "verifying unmodified test suite...")
	entry, err := fn.acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire sidecar for baseline: %w", err)
	}
	baselineErr := fn.baseline(ctx, entry, testCmd, testTimeout)
	fn.release(entry)
	if baselineErr != nil {
		return nil, baselineErr
	}

	type indexedResult struct {
		index  int
		result Result
	}
	resultsCh := make(chan indexedResult, len(mutations))
	var wg sync.WaitGroup
	var statusMu sync.Mutex
	var scheduleErr error
	usedSidecars := make(map[string]struct{})
	report := func(level iostream.Level, message string) {
		statusMu.Lock()
		defer statusMu.Unlock()
		status(level, message)
	}

	for index, m := range mutations {
		if err := ctx.Err(); err != nil {
			scheduleErr = err
			break
		}

		patch, err := fn.patch(workDir, m)
		if err != nil {
			report(iostream.LevelWarn, fmt.Sprintf("skip %s: %v", m.ID, err))
			resultsCh <- indexedResult{index: index, result: Result{Mutation: m, Error: err.Error()}}
			continue
		}

		entry, err := fn.acquire(ctx)
		if err != nil {
			scheduleErr = fmt.Errorf("acquire sidecar: %w", err)
			break
		}
		if _, used := usedSidecars[entry.ID]; !used {
			usedSidecars[entry.ID] = struct{}{}
			workerCount := len(usedSidecars)
			if workerCount <= 5 || workerCount%10 == 0 {
				noun := "sidecars"
				if workerCount == 1 {
					noun = "sidecar"
				}
				report(iostream.LevelInfo, fmt.Sprintf("mutation workers: %d %s assigned (latest %s)", workerCount, noun, shortSidecarID(entry.ID)))
			}
		}

		wg.Add(1)
		go func(index int, m Mutation, entry *sidecar.PoolEntry, patch []byte) {
			defer wg.Done()
			killed, output, runErr, reusable := fn.run(ctx, entry, patch, testCmd, testTimeout)
			if reusable {
				fn.release(entry)
			} else {
				replaceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
				replaceErr := fn.replace(replaceCtx, entry, report)
				cancel()
				if replaceErr != nil {
					runErr = fmt.Sprintf("%s; replace sidecar: %v", runErr, replaceErr)
				}
			}
			if runErr == "" && killed {
				output = ""
			}
			resultsCh <- indexedResult{index: index, result: Result{Mutation: m, Killed: killed, Output: output, Error: runErr}}
		}(index, m, entry, patch)
	}

	wg.Wait()
	if len(usedSidecars) > 0 {
		report(iostream.LevelDone, fmt.Sprintf("Used %d sidecars for mutation tests", len(usedSidecars)))
	}
	close(resultsCh)

	ordered := make([]Result, len(mutations))
	completed := make([]bool, len(mutations))
	for r := range resultsCh {
		ordered[r.index] = r.result
		completed[r.index] = true
	}
	results := make([]Result, 0, len(mutations))
	for index, result := range ordered {
		if completed[index] {
			results = append(results, result)
		}
	}
	return results, scheduleErr
}

func shortSidecarID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:8]
}

func validateBaseline(ctx context.Context, entry *sidecar.PoolEntry, testCmd string, testTimeout time.Duration) error {
	testCtx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()

	code, output, err := execOnSidecar(testCtx, entry, fmt.Sprintf("cd %s && %s", sidecar.ShellEscape(entry.RepoPath), testCmd))
	if err != nil {
		if testCtx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("baseline test timed out after %s", testTimeout)
		}
		return fmt.Errorf("run baseline test: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("baseline test failed (exit %d): %s", code, output)
	}
	return nil
}

type tailBuffer struct {
	buf     []byte
	maxSize int
	dropped int
}

func newTailBuffer(maxSize int) *tailBuffer {
	return &tailBuffer{maxSize: maxSize}
}

func (b *tailBuffer) Write(data []byte) (int, error) {
	n := len(data)
	if len(b.buf)+n > b.maxSize {
		keep := b.maxSize - n
		if keep < 0 {
			keep = 0
		}
		if keep < len(b.buf) {
			b.dropped += len(b.buf) - keep
			b.buf = append(b.buf[:0], b.buf[len(b.buf)-keep:]...)
		}
		if n > b.maxSize {
			b.dropped += n - b.maxSize
			data = data[n-b.maxSize:]
		}
	}
	b.buf = append(b.buf, data...)
	return n, nil
}

func (b *tailBuffer) String() string {
	if b.dropped == 0 {
		return string(b.buf)
	}
	return fmt.Sprintf("[%d bytes truncated]\n%s", b.dropped, b.buf)
}

// runOnSidecar applies the mutation patch to the sidecar workspace, runs the
// test suite under a timeout, then reverses the patch so the sidecar is ready
// for the next mutation.
func runOnSidecar(ctx context.Context, entry *sidecar.PoolEntry, patch []byte, testCmd string, testTimeout time.Duration) (killed bool, output, errMsg string, reusable bool) {
	return runMutation(ctx, entry.RepoPath, patch, testCmd, testTimeout, func(ctx context.Context, script string) (int, string, error) {
		return execOnSidecar(ctx, entry, script)
	})
}

func runMutation(ctx context.Context, repoPath string, patch []byte, testCmd string, testTimeout time.Duration, exec func(context.Context, string) (int, string, error)) (killed bool, output, errMsg string, reusable bool) {
	escaped := sidecar.ShellEscape(repoPath)
	encoded := sidecar.ShellEscape(base64.StdEncoding.EncodeToString(patch))
	applyCmd := fmt.Sprintf("echo %s | base64 -d | git -C %s apply", encoded, escaped)
	reverseCmd := fmt.Sprintf("echo %s | base64 -d | git -C %s apply --reverse", encoded, escaped)

	if code, out, err := exec(ctx, applyCmd); err != nil {
		return false, "", fmt.Sprintf("apply patch: %v", err), false
	} else if code != 0 {
		return false, "", fmt.Sprintf("apply patch (exit %d): %s", code, out), true
	}

	testCtx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	code, out, err := exec(testCtx, fmt.Sprintf("cd %s && %s", escaped, testCmd))

	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	cleanupCode, cleanupOut, cleanupErr := exec(cleanupCtx, reverseCmd)
	cleanupCancel()
	if cleanupErr != nil {
		return false, out, fmt.Sprintf("reverse patch: %v", cleanupErr), false
	}
	if cleanupCode != 0 {
		return false, out, fmt.Sprintf("reverse patch (exit %d): %s", cleanupCode, cleanupOut), false
	}
	if err != nil {
		if testCtx.Err() == context.DeadlineExceeded {
			return false, out, fmt.Sprintf("run test: timed out after %s", testTimeout), true
		}
		return false, out, fmt.Sprintf("run test: %v", err), true
	}
	return code != 0, out, "", true
}

func execOnSidecar(ctx context.Context, entry *sidecar.PoolEntry, script string) (int, string, error) {
	buf := newTailBuffer(maxOutputBytes)
	result, err := entry.Client.Exec(ctx, entry.ID, "sh", []string{"-c", script}, nil, func(_ string, data []byte) {
		_, _ = buf.Write(data)
	})
	if err != nil {
		return 0, buf.String(), err
	}
	return result.ExitCode, buf.String(), nil
}
