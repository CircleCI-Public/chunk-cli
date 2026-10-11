package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/factory"
)

func runCobra(t *testing.T, cmd *cobra.Command, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(""))
	cmd.SetArgs(args)
	cmd.SetContext(t.Context())
	err := cmd.Execute()
	return out.String(), errOut.String(), err
}

// writeRecord leaves the record a finished run would, which the fake factory
// does not.
func writeRecord(t *testing.T, root string, rec factory.Record) {
	t.Helper()
	dir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)
	assert.NilError(t, os.MkdirAll(filepath.Join(dir, "factory"), 0o755))
	b, err := json.Marshal(rec)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "factory", rec.RunID+".json"), b, 0o644))
}

// blockingFactoryConfig is a daemon whose run makes its worktree and then
// waits to be stopped, committing what it did as it ends.
func blockingFactoryConfig(started chan<- struct{}) func(context.Context, factory.RunOptions) (factory.Report, error) {
	return func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		wt, err := factory.CreateWorktree(ctx, factory.WorktreeOptions{Root: opts.Root, Path: filepath.Join(os.TempDir(), fmt.Sprintf("wt-%d", os.Getpid())), RunID: "run-1", Prompt: opts.Prompt})
		if err != nil {
			return factory.Report{}, err
		}
		opts.OnStart("run-1", wt)
		started <- struct{}{}
		<-ctx.Done()
		return factory.Report{RunID: "run-1", Worktree: wt, Started: true}, ctx.Err()
	}
}

func TestFactoryDetachReturnsWhileTheRunGoesOn(t *testing.T) {
	factoryProject(t)
	started := make(chan struct{}, 1)
	cfg := fakeSessionConfig()
	cfg.RunFactory = blockingFactoryConfig(started)
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Join(os.TempDir(), fmt.Sprintf("wt-%d", os.Getpid()))) })
	startSessionDaemon(t, cfg)

	out, stderr, err := runFactoryBuildCmd(t, "--detach", "--json", "add a flag")
	assert.NilError(t, err, stderr)
	var run detachedFactoryRun
	assert.NilError(t, json.Unmarshal([]byte(out), &run), out)
	assert.Equal(t, run.RunID, "run-1")
	assert.Equal(t, run.Branch, "smelt/add-a-flag/run-1")
	assert.Assert(t, run.SessionID != "" && run.Worktree != "")
	<-started

	statusOut, _, err := runCobra(t, newFactoryStatusCmd(), "run-1", "--json")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(statusOut, `"state": "running"`), statusOut)

	stopOut, _, err := runCobra(t, newFactoryStopCmd(), run.Branch)
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(stopOut, "Stopping run run-1"), stopOut)
}

func TestFactoryDetachRefusesPublishing(t *testing.T) {
	factoryProject(t)
	for _, flag := range []string{"--pr", "--push"} {
		_, _, err := runFactoryBuildCmd(t, "--detach", flag, "add a flag")
		assert.ErrorContains(t, err, "--detach cannot be combined")
	}
}

func TestFactoryStatusStopAndPublishOfAFinishedRun(t *testing.T) {
	project := factoryProject(t)
	startSessionDaemon(t, fakeFactoryConfig(t, factory.ResultPassed))
	_, stderr, err := runFactoryBuildCmd(t, "add a flag")
	assert.NilError(t, err, stderr)

	// The daemon still has the session.
	out, _, err := runCobra(t, newFactoryStatusCmd(), "--json")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, `"state": "done"`) && strings.Contains(out, `"rounds"`), out)

	out, _, err = runCobra(t, newFactoryStopCmd(), "run-1")
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(out, "already finished"), out)

	_, _, err = runCobra(t, newFactoryStatusCmd(), "nosuch")
	assert.ErrorContains(t, err, "No factory run nosuch")
	_, _, err = runCobra(t, newFactoryStopCmd(), "nosuch")
	assert.ErrorContains(t, err, "No factory run nosuch")

	// A publish needs the record the real run leaves, and a remote to push to.
	f, origin := publishProjectAt(t, project)
	writeRecord(t, project, factory.Record{
		RunID: "run-1", Prompt: "add a flag", Worktree: f.Worktree, Branch: f.Branch,
		DefaultBranch: "main", Result: factory.ResultPassed, Rounds: 1,
	})
	_, _, err = runCobra(t, newFactoryPublishCmd(), "run-1")
	assert.ErrorContains(t, err, "--push or --pr")
	_, _, err = runCobra(t, newFactoryPublishCmd(), "run-1", "--push")
	assert.NilError(t, err)
	assert.Assert(t, pushed(t, origin, f.Branch))
}

func TestFactoryPublishRefusesANonFactoryBranch(t *testing.T) {
	project := factoryProject(t)
	startSessionDaemon(t, fakeFactoryConfig(t, factory.ResultPassed))
	f, origin := publishProjectAt(t, project)
	writeRecord(t, project, factory.Record{
		RunID: "run-2", Prompt: "p", Worktree: f.Worktree, Branch: "feature/x",
		DefaultBranch: "main", Result: factory.ResultPassed, Rounds: 1,
	})
	_, _, err := runCobra(t, newFactoryPublishCmd(), "run-2", "--push")
	assert.ErrorContains(t, err, "refusing to publish feature/x")
	assert.Assert(t, !pushed(t, origin, "feature/x"))
}

func TestFactoryStatusOfARunTheDaemonForgotShowsItsRecord(t *testing.T) {
	project := factoryProject(t)
	startSessionDaemon(t, fakeSessionConfig())
	writeRecord(t, project, factory.Record{RunID: "run-7", Prompt: "p", Branch: "smelt/p/run-7", Result: factory.ResultPassed, Rounds: 2})
	out, _, err := runCobra(t, newFactoryStatusCmd(), "smelt/p/run-7", "--json")
	assert.NilError(t, err)
	var got map[string]any
	assert.NilError(t, json.Unmarshal([]byte(out), &got), out)
	assert.Equal(t, got["state"], "done")
	assert.Equal(t, got["run_id"], "run-7")
}

// publishProjectAt gives the project at root a bare origin and a worktree on a
// factory branch, as publishProject does for a fresh repository.
func publishProjectAt(t *testing.T, root string) (factoryWorktree, string) {
	t.Helper()
	origin := filepath.Join(t.TempDir(), "origin.git")
	runGit(t, root, "init", "--bare", origin)
	runGit(t, root, "remote", "set-url", "origin", origin)
	runGit(t, root, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, root, "fetch", "origin")
	wt, err := factory.CreateWorktree(context.Background(), factory.WorktreeOptions{Root: root, Path: filepath.Join(t.TempDir(), "wt"), RunID: "run-9", Prompt: "publish me"})
	assert.NilError(t, err)
	return factoryWorktree{Worktree: wt.Path, Branch: wt.Branch}, origin
}

type factoryWorktree struct{ Worktree, Branch string }
