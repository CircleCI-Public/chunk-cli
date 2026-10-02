package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
)

// fakeFactoryConfig is a daemon whose factory runs do one round without
// sidecars: they make the run's worktree for real, commit a file to it, and
// end with result.
func fakeFactoryConfig(t *testing.T, result factory.Result) watchd.ReviewConfig {
	cfg := fakeSessionConfig("fine")
	cfg.RunFactory = func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		wt, err := factory.CreateWorktree(ctx, opts.Root, filepath.Join(t.TempDir(), "wt"), "run-1")
		if err != nil {
			return factory.Report{}, fmt.Errorf("create the run's worktree: %w", err)
		}
		opts.OnStart("run-1", wt)
		opts.OnEvent(factory.Event{Kind: factory.EventImplementing, Round: 1, Prompt: opts.Prompt})
		if err := os.WriteFile(filepath.Join(wt.Path, "flag.go"), []byte("package main\n"), 0o644); err != nil {
			return factory.Report{}, err
		}
		opts.OnEvent(factory.Event{Kind: factory.EventImplemented, Round: 1, Turn: factory.Turn{Summary: "added the flag"}})
		opts.OnEvent(factory.Event{Kind: factory.EventChecking, Round: 1})
		opts.OnCheck(factory.Check{Name: "test", Kind: factory.KindValidate, Status: factory.StatusPassed})
		checks := []factory.Check{{Name: "bugs", Kind: factory.KindReview, Status: factory.StatusPassed}}
		if result != factory.ResultPassed {
			checks[0].Status = factory.StatusFailed
			checks[0].Findings = []review.Finding{{File: "flag.go", Line: 1, Severity: "high", Body: "unused"}}
		}
		opts.OnEvent(factory.Event{Kind: factory.EventChecked, Round: 1, Checks: checks})
		_, err = wt.Commit(ctx, "chunk factory: add a flag")
		return factory.Report{
			RunID: "run-1", Worktree: wt, Started: true, Committed: err == nil,
			Outcome: factory.Outcome{Result: result, Rounds: 1, Checks: checks},
		}, nil
	}
	return cfg
}

// factoryProject is a session project with a config, as the current
// directory.
func factoryProject(t *testing.T) string {
	t.Helper()
	isolateConfig(t)
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
	project := sessionProject(t)
	cfg := `{"orgID":"org-1","commands":[{"name":"test","run":"go test ./..."}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(project, ".chunk", "config.json"), []byte(cfg), 0o644))
	t.Chdir(project)
	return project
}

func runFactoryCmd(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runFactoryCmdCtx(t.Context(), args...)
}

func runFactoryCmdCtx(ctx context.Context, args ...string) (string, string, error) {
	cmd := newFactoryCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs(args)
	cmd.SetContext(ctx)
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

func TestFactoryRunsOnTheDaemonAndReportsTheWork(t *testing.T) {
	project := factoryProject(t)
	startSessionDaemon(t, fakeFactoryConfig(t, factory.ResultPassed))

	_, stderr, err := runFactoryCmd(t, "add a --verbose flag")
	assert.NilError(t, err, stderr)

	for _, want := range []string{
		"started on the watch daemon",
		// The run ends before the first poll, so only where it ended shows.
		"round 1: done",
		"implementer finished",
		"added the flag",
		"test passed",
		"Committed 1 file changed, 1 insertion(+) to chunk/factory/run-1",
		// The project's uncommitted config is the run's baseline.
		"Apply it with: git diff --binary",
		"review bugs: no findings",
		"All checks passed after 1 round(s).",
	} {
		assert.Assert(t, bytes.Contains([]byte(stderr), []byte(want)), "missing %q in:\n%s", want, stderr)
	}
	// The developer's checkout is untouched; the work is on the run's branch.
	_, err = os.Stat(filepath.Join(project, "flag.go"))
	assert.Assert(t, os.IsNotExist(err))
}

func TestFactoryWhoseChecksStillFailExitsWithAnError(t *testing.T) {
	factoryProject(t)
	startSessionDaemon(t, fakeFactoryConfig(t, factory.ResultExhausted))

	_, stderr, err := runFactoryCmd(t, "--attempts", "1", "add a --verbose flag")
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), "Checks still failed after 1 round(s).")
	assert.Assert(t, bytes.Contains([]byte(stderr), []byte("[high] flag.go:1 unused")), stderr)
}

// --json prints the run's record, and the exit code still says whether its
// checks passed.
func TestFactoryJSONWhoseChecksStillFailExitsWithAnError(t *testing.T) {
	factoryProject(t)
	startSessionDaemon(t, fakeFactoryConfig(t, factory.ResultExhausted))

	stdout, stderr, err := runFactoryCmd(t, "--json", "--attempts", "1", "add a --verbose flag")
	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), "Checks still failed after 1 round(s).")
	var detail watchd.SessionDetail
	assert.NilError(t, json.Unmarshal([]byte(stdout), &detail), stdout)
	assert.Equal(t, detail.Factory.Result, "exhausted")
	assert.Assert(t, !bytes.Contains([]byte(stderr), []byte("[high] flag.go:1 unused")), stderr)
}

// Ctrl-C stops the run rather than leaving it running, and the work done so
// far is still committed and reported.
func TestFactoryInterruptCancelsTheRunAndReportsTheWork(t *testing.T) {
	factoryProject(t)
	started := make(chan struct{})
	cfg := fakeSessionConfig("fine")
	cfg.RunFactory = func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		wt, err := factory.CreateWorktree(ctx, opts.Root, filepath.Join(t.TempDir(), "wt"), "run-1")
		if err != nil {
			return factory.Report{}, fmt.Errorf("create the run's worktree: %w", err)
		}
		opts.OnStart("run-1", wt)
		opts.OnEvent(factory.Event{Kind: factory.EventImplementing, Round: 1, Prompt: opts.Prompt})
		if err := os.WriteFile(filepath.Join(wt.Path, "flag.go"), []byte("package main\n"), 0o644); err != nil {
			return factory.Report{}, err
		}
		close(started)
		<-ctx.Done()
		_, err = wt.Commit(context.WithoutCancel(ctx), "chunk factory: add a flag")
		return factory.Report{RunID: "run-1", Worktree: wt, Started: true, Committed: err == nil}, ctx.Err()
	}
	startSessionDaemon(t, cfg)

	ctx, interrupt := context.WithCancel(t.Context())
	defer interrupt()
	go func() {
		<-started
		interrupt()
	}()
	_, stderr, err := runFactoryCmdCtx(ctx, "add a --verbose flag")

	var ue *userError
	assert.Assert(t, errors.As(err, &ue), "got %v", err)
	assert.Equal(t, ue.UserMessage(), "The factory run was cancelled.")
	for _, want := range []string{"Stopping", "Committed 1 file changed, 1 insertion(+) to chunk/factory/run-1"} {
		assert.Assert(t, bytes.Contains([]byte(stderr), []byte(want)), "missing %q in:\n%s", want, stderr)
	}
}

func TestFactoryRefusesReviewsOutsideTheProject(t *testing.T) {
	factoryProject(t)
	outside := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(outside, "bugs.md"), []byte("find bugs"), 0o644))

	_, _, err := runFactoryCmd(t, "--reviews", outside, "add a --verbose flag")
	assert.ErrorContains(t, err, "--reviews must be a directory inside the project.")
}
