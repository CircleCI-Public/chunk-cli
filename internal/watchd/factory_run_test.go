package watchd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/factory"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/review"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// newFactoryDaemon builds a daemon tracking a project with two review prompts
// and one validation command, whose factory runs are run by run in place of
// factory.Run.
func newFactoryDaemon(t *testing.T, run func(context.Context, factory.RunOptions) (factory.Report, error)) (*daemon, string) {
	t.Helper()
	d, root := newSessionDaemon(t, &fakeBackend{})
	d.rcfg.RunFactory = run
	cfg := `{"orgID":"org-1","commands":[{"name":"test","run":"go test ./..."}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(root, ".chunk", "config.json"), []byte(cfg), 0o644))
	return d, root
}

// scriptedRun plays one round as factory.Run reports it: an implementer turn,
// both reviews on their own sidecars, with "bugs" submitting a command, the
// validation command, and the round's checks.
func scriptedRun(result factory.Result) func(context.Context, factory.RunOptions) (factory.Report, error) {
	return func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		wt := factory.Worktree{Path: "/data/factory/run-1", Branch: "chunk/factory/run-1", Baseline: "base", Head: "head"}
		opts.OnStart("run-1", wt)
		opts.Status(iostream.LevelStep, "Preparing an implementer sidecar and 2 reviewer sidecar(s)...")

		opts.OnEvent(factory.Event{Kind: factory.EventImplementing, Round: 1, Prompt: opts.Prompt})
		opts.OnEvent(factory.Event{Kind: factory.EventImplemented, Round: 1, Turn: factory.Turn{Summary: "added it", Duration: time.Minute, CostUSD: 0.5}})
		opts.OnEvent(factory.Event{Kind: factory.EventCollected, Round: 1, Change: factory.Change{Stat: "1 file changed", Fingerprint: "f"}})
		opts.OnEvent(factory.Event{Kind: factory.EventChecking, Round: 1})

		opts.OnReviewProgress(review.ProgressEvent{Prompt: "bugs", SidecarID: "sc-2", State: review.StateRunning})
		opts.OnReviewProgress(review.ProgressEvent{Prompt: "style", SidecarID: "sc-3", State: review.StateRunning})
		if _, err := opts.Exec(ctx, &sidecar.PoolEntry{ID: "sc-2"}, "claude -p", nil, func(string, []byte) {}, nil); err != nil {
			return factory.Report{}, err
		}
		// A bookkeeping script on the implementer is not a review's.
		if _, err := opts.Exec(ctx, &sidecar.PoolEntry{ID: "sc-1"}, "git add -N .", nil, func(string, []byte) {}, nil); err != nil {
			return factory.Report{}, err
		}
		opts.OnReviewProgress(review.ProgressEvent{Prompt: "bugs", SidecarID: "sc-2", State: review.StateDone, Duration: time.Second})
		opts.OnReviewProgress(review.ProgressEvent{Prompt: "style", SidecarID: "sc-3", State: review.StateDone, Duration: time.Second})
		opts.OnCheck(factory.Check{Name: "test", Kind: factory.KindValidate, Status: factory.StatusPassed, SidecarID: "sc-1", Duration: 2 * time.Second})
		opts.OnCheck(factory.Check{Name: "lint", Kind: factory.KindValidate, Status: factory.StatusFailed, SidecarID: "sc-1", Output: "main.go:3: unused"})

		bug := review.Finding{File: "main.go", Line: 3, Severity: "high", Body: "nil deref"}
		checks := []factory.Check{
			{Name: "bugs", Kind: factory.KindReview, Status: factory.StatusFailed, SidecarID: "sc-2", Findings: []review.Finding{bug}},
			{Name: "style", Kind: factory.KindReview, Status: factory.StatusPassed, SidecarID: "sc-3"},
			{Name: "test", Kind: factory.KindValidate, Status: factory.StatusPassed, SidecarID: "sc-1"},
		}
		opts.OnEvent(factory.Event{Kind: factory.EventChecked, Round: 1, Checks: checks})

		return factory.Report{
			RunID: "run-1", Worktree: wt, Started: true, Committed: true,
			Outcome: factory.Outcome{Result: result, Rounds: 1, Checks: checks},
		}, nil
	}
}

func TestFactorySessionRecordsTheRun(t *testing.T) {
	var got factory.RunOptions
	run := scriptedRun(factory.ResultPassed)
	d, root := newFactoryDaemon(t, func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		got = opts
		return run(ctx, opts)
	})

	sess, err := d.startFactory(FactoryRequest{
		ProjectRoot: root, Prompt: "  add a --verbose flag  ", Reviewers: 1,
		ImplementerInstructions: "keep the change small",
	})
	assert.NilError(t, err)
	detail := waitForSessionEnd(t, d, sess.ID)

	// The run was asked for what the request and the project say.
	assert.Equal(t, got.Prompt, "add a --verbose flag")
	assert.Equal(t, got.Attempts, DefaultAttempts)
	assert.Equal(t, got.Reviewers, 1)
	assert.Equal(t, got.OrgID, "org-1")
	assert.Equal(t, len(got.Prompts), 2)
	assert.Equal(t, len(got.Commands), 1)
	assert.Equal(t, got.Credential.Value, testSecret)
	assert.Equal(t, got.ImplementerInstructions, "keep the change small")

	assert.Equal(t, detail.State, SessionDone)
	assert.Equal(t, detail.Stages[0].ID, StageFactoryLoop)
	assert.Equal(t, detail.Stages[0].State, StageDone)
	assert.Equal(t, detail.Stages[0].Note, "all checks passed after 1 round(s)")
	assert.Equal(t, detail.Branch, "chunk/factory/run-1")
	assert.DeepEqual(t, *detail.Factory, FactoryRun{
		Prompt: "add a --verbose flag", Attempts: DefaultAttempts, RunID: "run-1",
		Worktree: "/data/factory/run-1", Branch: "chunk/factory/run-1", Baseline: "base", Head: "head",
		Result: "passed", Rounds: 1, Committed: true,
		Progress: Feed{Lines: []FeedLine{{Level: FeedStep, Text: "Preparing an implementer sidecar and 2 reviewer sidecar(s)..."}}, Total: 1},
	})

	assert.Equal(t, len(detail.Rounds), 1)
	round := detail.Rounds[0]
	assert.Equal(t, round.State, RoundDone)
	assert.Equal(t, round.Note, "2 of 3 checks passed")
	assert.DeepEqual(t, *round.Implement, RoundImplement{
		State: ImplementApplied, DurationMS: 60000, CostUSD: 0.5, Summary: "added it", Stat: "1 file changed",
	})
	assert.DeepEqual(t, round.Checks, []RoundCheck{
		{Name: "test", Status: "passed", SidecarID: "sc-1", DurationMS: 2000},
		{Name: "lint", Status: "failed", SidecarID: "sc-1", Output: "main.go:3: unused"},
	})
	assert.Equal(t, round.Findings, 1)
	assert.Equal(t, round.Worth, 1)
	byName := map[string]ReviewPrompt{}
	for _, p := range round.Reviews {
		byName[p.Name] = p
	}
	assert.Equal(t, byName["bugs"].State, PromptDone)
	assert.Equal(t, byName["bugs"].Findings, 1)
	assert.Assert(t, byName["bugs"].CommandID != "", "the review's command was not tied to it")
	assert.Equal(t, byName["style"].CommandID, "")
	assert.Equal(t, len(detail.Details[0].Results), 2)
	for _, res := range detail.Details[0].Results {
		want := map[string]string{"bugs": "failed", "style": "passed"}[res.Prompt]
		assert.Equal(t, res.Status, want, res.Prompt)
	}

	// Only the review's command is in the output store, named for its review.
	cmds := d.out.commandsFor(root)
	assert.Equal(t, len(cmds), 1)
	assert.Equal(t, cmds[0].Name, "round 1 review: bugs")
}

func TestFactorySessionWhoseChecksStillFailIsDoneWithItsStageFailed(t *testing.T) {
	d, root := newFactoryDaemon(t, scriptedRun(factory.ResultExhausted))

	sess, err := d.startFactory(FactoryRequest{ProjectRoot: root, Prompt: "add a flag", Attempts: 1})
	assert.NilError(t, err)
	detail := waitForSessionEnd(t, d, sess.ID)

	assert.Equal(t, detail.State, SessionDone)
	assert.Equal(t, detail.Factory.Result, "exhausted")
	assert.Equal(t, detail.Stages[0].State, StageFailed)
	assert.Equal(t, detail.Stages[0].Note, "checks still failed after 1 round(s)")
}

// A run that stops because the implementer changed nothing new ends on a
// round it never checked: that round is done, not failed, and the run's
// rounds are the ones checked.
func TestFactorySessionThatGetsStuckClosesItsUncheckedRound(t *testing.T) {
	run := scriptedRun(factory.ResultStuck)
	d, root := newFactoryDaemon(t, func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		rep, err := run(ctx, opts)
		if err != nil {
			return rep, err
		}
		opts.OnEvent(factory.Event{Kind: factory.EventImplementing, Round: 2, Prompt: opts.Prompt})
		opts.OnEvent(factory.Event{Kind: factory.EventImplemented, Round: 2, Turn: factory.Turn{Summary: "nothing new"}})
		opts.OnEvent(factory.Event{Kind: factory.EventCollected, Round: 2, Change: factory.Change{Stat: "1 file changed", Fingerprint: "f"}})
		return rep, nil
	})

	sess, err := d.startFactory(FactoryRequest{ProjectRoot: root, Prompt: "add a flag"})
	assert.NilError(t, err)
	detail := waitForSessionEnd(t, d, sess.ID)

	assert.Equal(t, detail.State, SessionDone)
	assert.Equal(t, detail.Stages[0].State, StageFailed)
	assert.Equal(t, detail.Factory.Rounds, 1)
	assert.Equal(t, len(detail.Rounds), 2)
	assert.Equal(t, detail.Rounds[0].State, RoundDone)
	last := detail.Rounds[1]
	assert.Equal(t, last.State, RoundDone)
	assert.Equal(t, last.Note, "not checked: the implementer stopped changing the code after round 1, with checks still failing")
	assert.Equal(t, len(last.Reviews), 0)
}

func TestFactorySessionThatCannotStartSaysWhichStepFailed(t *testing.T) {
	d, root := newFactoryDaemon(t, func(context.Context, factory.RunOptions) (factory.Report, error) {
		return factory.Report{}, fmt.Errorf("create the run's sidecars: %w", errors.New("quota exceeded"))
	})

	sess, err := d.startFactory(FactoryRequest{ProjectRoot: root, Prompt: "add a flag"})
	assert.NilError(t, err)
	detail := waitForSessionEnd(t, d, sess.ID)

	assert.Equal(t, detail.State, SessionFailed)
	assert.Equal(t, detail.Error, "create the run's sidecars: quota exceeded")
	assert.Equal(t, detail.Stages[0].State, StageFailed)
	assert.Equal(t, detail.Factory.Result, "")
}

func TestFactorySessionWhoseImplementerFailsRecordsTheTurnFailed(t *testing.T) {
	d, root := newFactoryDaemon(t, func(_ context.Context, opts factory.RunOptions) (factory.Report, error) {
		opts.OnEvent(factory.Event{Kind: factory.EventImplementing, Round: 1, Prompt: opts.Prompt})
		return factory.Report{Started: true}, errors.New("round 1: implement: implementer: claude exited 1")
	})

	sess, err := d.startFactory(FactoryRequest{ProjectRoot: root, Prompt: "add a flag"})
	assert.NilError(t, err)
	detail := waitForSessionEnd(t, d, sess.ID)

	assert.Equal(t, detail.State, SessionFailed)
	round := detail.Rounds[0]
	assert.Equal(t, round.State, RoundFailed)
	assert.Equal(t, round.Implement.State, ImplementFailed)
	assert.Assert(t, strings.Contains(round.Implement.Error, "claude exited 1"), round.Implement.Error)
}

func TestFactorySessionCanBeCancelled(t *testing.T) {
	d, root := newFactoryDaemon(t, func(ctx context.Context, _ factory.RunOptions) (factory.Report, error) {
		<-ctx.Done()
		return factory.Report{}, ctx.Err()
	})

	sess, err := d.startFactory(FactoryRequest{ProjectRoot: root, Prompt: "add a flag"})
	assert.NilError(t, err)
	found, active := d.sessions.cancelSession(sess.ID)
	assert.Assert(t, found && active)
	assert.Equal(t, waitForSessionEnd(t, d, sess.ID).State, SessionCancelled)
}

// One session at a time per project.
func TestFactorySessionsTakeTurns(t *testing.T) {
	s := newSessionStore(context.Background())
	_, _, busy := s.add(Session{ProjectRoot: "/p", Factory: &FactoryRun{}})
	assert.Equal(t, busy, "")
	_, _, busy = s.add(Session{ProjectRoot: "/p", Factory: &FactoryRun{}})
	assert.Assert(t, busy != "", "two factory runs ran at once")
}

// TestFactorySessionKeepsItsLog guards --log and --verbose on the daemon: the
// run is asked for them, and the record says where the log is.
func TestFactorySessionKeepsItsLog(t *testing.T) {
	var got factory.RunOptions
	run := scriptedRun(factory.ResultPassed)
	d, root := newFactoryDaemon(t, func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		got = opts
		rep, err := run(ctx, opts)
		rep.Log = "/logs/run-1.log"
		return rep, err
	})

	sess, err := d.startFactory(FactoryRequest{ProjectRoot: root, Prompt: "add a flag", Log: "/logs/run-1.log", Verbose: true})
	assert.NilError(t, err)
	detail := waitForSessionEnd(t, d, sess.ID)

	assert.Equal(t, got.Log, "/logs/run-1.log")
	assert.Assert(t, got.Verbose)
	assert.Equal(t, detail.Factory.Log, "/logs/run-1.log")
}

func TestStartFactoryRefusesABadRequest(t *testing.T) {
	d, root := newFactoryDaemon(t, scriptedRun(factory.ResultPassed))

	for name, tc := range map[string]struct {
		req    FactoryRequest
		status int
		msg    string
	}{
		"no prompt":       {FactoryRequest{ProjectRoot: root, Prompt: "  "}, http.StatusBadRequest, "prompt required"},
		"unknown project": {FactoryRequest{ProjectRoot: "/nowhere", Prompt: "x"}, http.StatusNotFound, "not tracking"},
		"escaping dir":    {FactoryRequest{ProjectRoot: root, Prompt: "x", ReviewsDir: "../elsewhere"}, http.StatusBadRequest, "inside the project"},
		"missing dir":     {FactoryRequest{ProjectRoot: root, Prompt: "x", ReviewsDir: "nope"}, http.StatusBadRequest, "nope"},
		"relative log":    {FactoryRequest{ProjectRoot: root, Prompt: "x", Log: "run.log"}, http.StatusBadRequest, "absolute path"},
		"reviews alone": {
			FactoryRequest{ProjectRoot: root, Prompt: "x", ReviewsDir: ".chunk/reviews", NoValidate: true},
			0, "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := d.startFactory(tc.req)
			if tc.status == 0 {
				assert.NilError(t, err, "reviews alone are enough")
				return
			}
			var ae *apiError
			assert.Assert(t, errors.As(err, &ae), "got %v", err)
			assert.Equal(t, ae.status, tc.status)
			assert.ErrorContains(t, ae, tc.msg)
		})
	}
	d.sessions.stopAll()

	// Neither reviews nor validation commands: the loop would pass anything.
	assert.NilError(t, os.RemoveAll(filepath.Join(root, ".chunk", "reviews")))
	_, err := d.startFactory(FactoryRequest{ProjectRoot: root, Prompt: "x", NoValidate: true})
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae), "got %v", err)
	assert.ErrorContains(t, ae, "nothing to check")
}

func TestFeedKeepsTheLatestLinesAndCountsThemAll(t *testing.T) {
	var f Feed
	for i := range maxFeedLines + 5 {
		f.add(iostream.LevelInfo, fmt.Sprint(i))
	}
	assert.Equal(t, f.Total, maxFeedLines+5)
	assert.Equal(t, len(f.Lines), maxFeedLines)
	assert.Equal(t, f.Lines[0].Text, "5")

	// A follower that has seen all but two gets those two.
	got := f.Since(f.Total - 2)
	assert.Equal(t, len(got), 2)
	assert.Equal(t, got[1].Text, fmt.Sprint(maxFeedLines+4))
	// One that fell behind gets what is still kept, and one that is ahead
	// of the feed nothing.
	assert.Equal(t, len(f.Since(0)), maxFeedLines)
	assert.Equal(t, len(f.Since(f.Total+1)), 0)
}
