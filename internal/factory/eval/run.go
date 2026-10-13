package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/gitexec"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// gitIdentity commits as chunk whatever the sidecar image's git config says,
// as factory's own baseline commits do.
const gitIdentity = "git -c user.name=chunk -c user.email=chunk@circleci.com -c commit.gpgsign=false"

// maxScriptOutput caps what a setup script may print; they print nothing.
const maxScriptOutput = 64 << 10

// setupPrompt names a case's setup in its errors, which are not any review's.
const setupPrompt = "(setup)"

// Options configures scoring a suite.
type Options struct {
	// Root is the local repository the suite's revisions are in.
	Root    string
	Suite   Suite
	Prompts []review.Prompt
	// Runs is how many times each case is reviewed; one when zero. Reviews
	// vary from run to run, so comparing prompts on one run each compares
	// luck as much as prompts.
	Runs int
	// Entries are the sidecars the reviews run on. Every case is put on all
	// of them, and its reviews share them; one per prompt runs them all at
	// once.
	Entries []*sidecar.PoolEntry
	Exec    sidecar.Execer
	// Sync mirrors the local directory dir into entry's workspace, leaving
	// the workspace's .git alone, as a pool's sync does.
	Sync   func(ctx context.Context, entry *sidecar.PoolEntry, dir string) error
	Review review.Options
	Status iostream.StatusFunc
	// OnCase is called with each case's result as it is scored.
	OnCase func(CaseResult)
}

// Run reviews every case in the suite with the prompts, Runs times over, and
// scores each. A case whose setup or reviews fail is recorded as errored and
// the suite goes on. The returned error is for failures that would fail every
// case the same way, such as a missing claude or a rejected credential.
func Run(ctx context.Context, opts Options) ([]CaseResult, error) {
	if len(opts.Entries) == 0 {
		return nil, errors.New("no sidecars to review on")
	}
	if len(opts.Prompts) == 0 {
		return nil, errors.New("no review prompts")
	}
	runs := max(opts.Runs, 1)
	status := opts.Status
	if status == nil {
		status = func(iostream.Level, string) {}
	}
	onCase := opts.OnCase
	if onCase == nil {
		onCase = func(CaseResult) {}
	}
	opts.Review.StructuredFindings = true

	var out []CaseResult
	for run := 1; run <= runs; run++ {
		for _, c := range opts.Suite.Cases {
			status(iostream.LevelStep, fmt.Sprintf("Case %s (run %d of %d)...", c.Name, run, runs))
			results, err := runCase(ctx, opts, c, status)
			if err != nil && (ctx.Err() != nil || fatal(err)) {
				return out, err
			}
			r := Score(c, run, results)
			if err != nil {
				r.Errors = append(r.Errors, PromptError{Prompt: setupPrompt, Error: err.Error()})
			}
			out = append(out, r)
			onCase(r)
		}
	}
	return out, nil
}

// fatal reports whether err stops the suite: review.RunPass stops a pass for
// the same reasons, since every sidecar shares one image and one credential.
func fatal(err error) bool {
	var stop passStopped
	return errors.As(err, &stop)
}

// passStopped is a review pass that stopped itself rather than record a
// failure per review.
type passStopped struct{ err error }

func (e passStopped) Error() string { return e.err.Error() }
func (e passStopped) Unwrap() error { return e.err }

// runCase puts c on every sidecar and runs the reviews of it: the revision's
// tree is committed as the baseline, and the patch on top of it is the
// uncommitted change under review, as factory leaves a reviewer's workspace.
func runCase(ctx context.Context, opts Options, c Case, status iostream.StatusFunc) ([]review.Result, error) {
	scratch, err := os.MkdirTemp("", "chunk-eval-"+c.Name+"-")
	if err != nil {
		return nil, fmt.Errorf("create scratch directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()
	tree := filepath.Join(scratch, "tree")
	git := gitexec.Runner{Dir: opts.Root}
	if out, err := git.CombinedOutput(ctx, "worktree", "add", "--quiet", "--detach", tree, c.Revision); err != nil {
		return nil, fmt.Errorf("check out %s: %w: %s", c.Revision, err, strings.TrimSpace(string(out)))
	}
	defer func() {
		_ = git.Run(context.WithoutCancel(ctx), "worktree", "remove", "--force", tree)
	}()

	baseline := gitIdentity + ` commit -q --no-verify --allow-empty -m "chunk eval baseline"`
	if err := onEntries(ctx, opts, tree, "git reset -q && git add -A && "+baseline); err != nil {
		return nil, fmt.Errorf("set up the baseline: %w", err)
	}

	patch := filepath.Join(scratch, "patch")
	if err := os.WriteFile(patch, c.patch, 0o600); err != nil {
		return nil, fmt.Errorf("write patch: %w", err)
	}
	if out, err := (gitexec.Runner{Dir: tree}).CombinedOutput(ctx, "apply", patch); err != nil {
		return nil, fmt.Errorf("apply patch: %w: %s", err, strings.TrimSpace(string(out)))
	}
	// New files are marked intent-to-add so `git diff HEAD` shows them.
	if err := onEntries(ctx, opts, tree, "git reset -q && git add -A -N"); err != nil {
		return nil, fmt.Errorf("set up the change: %w", err)
	}

	free := make(chan *sidecar.PoolEntry, len(opts.Entries))
	for _, e := range opts.Entries {
		free <- e
	}
	acquire := func(ctx context.Context) (*sidecar.PoolEntry, error) {
		select {
		case e := <-free:
			return e, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	reviewOpts := opts.Review
	reviewOpts.StatusFn = status
	results, err := review.RunPass(ctx, acquire, func(e *sidecar.PoolEntry) { free <- e }, opts.Exec,
		factory.ReviewPrompts(c.Request, opts.Prompts), reviewOpts)
	if err != nil {
		return results, passStopped{err}
	}
	return results, nil
}

// onEntries syncs dir to every sidecar at once, then runs script in each
// workspace, and reports every sidecar that failed.
func onEntries(ctx context.Context, opts Options, dir, script string) error {
	errs := make([]error, len(opts.Entries))
	var wg sync.WaitGroup
	for i, e := range opts.Entries {
		wg.Add(1)
		go func(i int, e *sidecar.PoolEntry) {
			defer wg.Done()
			if err := opts.Sync(ctx, e, dir); err != nil {
				errs[i] = fmt.Errorf("%s: sync: %w", e.ID, err)
				return
			}
			cmd := "cd " + sidecar.ShellEscape(e.RepoPath) + " && " + script
			if _, err := review.RunScript(ctx, opts.Exec, e, cmd, maxScriptOutput); err != nil {
				errs[i] = fmt.Errorf("%s: %w", e.ID, err)
			}
		}(i, e)
	}
	wg.Wait()
	return errors.Join(errs...)
}
