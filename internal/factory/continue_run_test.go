package factory

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/circleci"
	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/iostream"
	"github.com/smeltdev/smelt/internal/review"
	"github.com/smeltdev/smelt/internal/sidecar"
)

// localBackend provisions sidecars that are directories on this machine. A
// sidecar's workspace is a directory of its own, standing in for the repoPath
// the pool asks for; rsync copies files in and out and leaves .git alone, as
// the real syncs do.
type localBackend struct {
	sidecar.Backend // the methods a run does not reach stay unimplemented
	dir             string
}

func (b *localBackend) workspace(id string) string { return filepath.Join(b.dir, id) }

func (b *localBackend) Create(_ context.Context, name, _, _ string) (sidecar.Instance, error) {
	return sidecar.Instance{ID: name}, os.MkdirAll(b.workspace(name), 0o755)
}

func (b *localBackend) Delete(_ context.Context, id string) error {
	return os.RemoveAll(b.workspace(id))
}

func (b *localBackend) IsStale(context.Context, string) bool { return false }

func (b *localBackend) Sync(ctx context.Context, id, _, cwd string, _ bool, _ iostream.StatusFunc) error {
	dst := b.workspace(id)
	if out, err := exec.CommandContext(ctx, "rsync", "-a", "--delete", "--exclude=.git", cwd+"/", dst+"/").CombinedOutput(); err != nil {
		return fmt.Errorf("rsync: %w: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); err == nil {
		return nil
	}
	if out, err := exec.CommandContext(ctx, "git", "-C", dst, "init", "-q").CombinedOutput(); err != nil {
		return fmt.Errorf("git init: %w: %s", err, out)
	}
	return nil
}

func (b *localBackend) Pull(ctx context.Context, id, _, localDir string, _ iostream.StatusFunc) error {
	if out, err := exec.CommandContext(ctx, "rsync", "-a", "--delete", "--exclude=.git", b.workspace(id)+"/", localDir+"/").CombinedOutput(); err != nil {
		return fmt.Errorf("rsync: %w: %s", err, out)
	}
	return nil
}

// inWorkspace runs scripts in the local directory that stands for a sidecar's
// workspace.
func (b *localBackend) inWorkspace(run review.Execer) review.Execer {
	return func(ctx context.Context, e *sidecar.PoolEntry, script string, env map[string]string, onOutput circleci.OutputFn, onSubmitted func(string)) (int, error) {
		script = strings.ReplaceAll(script, e.RepoPath, b.workspace(e.ID))
		return run(ctx, e, script, env, onOutput, onSubmitted)
	}
}

// A continued run whose branch holds no work implements the request: the
// implementer is sent it with the context line, the run ends with a real
// result, and the status and the saved record say where it started from.
func TestRunContinuingAnEmptyBranchImplementsTheRequest(t *testing.T) {
	for _, bin := range []string{"rsync", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			t.Skipf("%s is not installed", bin)
		}
	}
	t.Setenv(config.EnvHome, t.TempDir())
	ctx := context.Background()
	root := newProject(t)
	wt, err := CreateWorktree(ctx, WorktreeOptions{Root: root, Path: filepath.Join(t.TempDir(), "wt"), RunID: "run-1", Prompt: "add a flag"})
	assert.NilError(t, err)
	rec := Record{
		RunID: "run-1", Prompt: "add a flag",
		Worktree: wt.Path, Branch: wt.Branch, Baseline: wt.Baseline, Head: wt.Head,
		BaseBranch: wt.BaseBranch, DefaultBranch: "main", Result: ResultNoChange,
	}
	assert.Equal(t, gitOutput(t, root, "rev-parse", rec.Branch), rec.Baseline, "the branch holds work")

	home := t.TempDir()
	installFakeClaude(t, home, "echo 'package main' > flag.go\n"+streamOK)
	backend := &localBackend{dir: t.TempDir()}
	var (
		mu       sync.Mutex
		statuses []string
	)
	rep, err := Run(ctx, RunOptions{
		Root:       root,
		Continue:   &Continuation{From: rec},
		Attempts:   2,
		Backend:    backend,
		Exec:       backend.inWorkspace(withoutPkill(localExec(home))),
		Credential: review.Credential{EnvVar: "ANTHROPIC_API_KEY", Value: "k"},
		Status: func(_ iostream.Level, msg string) {
			mu.Lock()
			defer mu.Unlock()
			statuses = append(statuses, msg)
		},
	})
	assert.NilError(t, err)

	assert.Assert(t, rep.Started)
	assert.Equal(t, rep.Outcome.Result, ResultPassed)
	assert.Assert(t, rep.Outcome.FromEmpty)
	prompts := readFile(t, home, "stdin")
	assert.Equal(t, rep.Outcome.Rounds, 1)
	assert.Equal(t, strings.Count(prompts, "start from the request."), 1, "one implementer turn with the request")
	assert.Assert(t, strings.Contains(prompts, "add a flag\n\nAn earlier run on this branch produced no work; start from the request."), prompts)
	assert.Assert(t, !strings.Contains(prompts, "Build on it"), prompts)
	assert.Assert(t, strings.Contains(strings.Join(statuses, "\n"), "continuing with no prior work: implementing from the request"), "%v", statuses)

	saved, err := LoadRecord(root, rep.RunID)
	assert.NilError(t, err)
	assert.Assert(t, saved.ContinuedFromEmpty)
	assert.Equal(t, saved.Result, ResultPassed)
	assert.Equal(t, gitOutput(t, root, "show", "--format=", "--name-only", rec.Branch), "flag.go")
}
