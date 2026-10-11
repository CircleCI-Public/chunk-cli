package server

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/smeltdev/smelt/internal/factory"
	"github.com/smeltdev/smelt/internal/harness"
	"github.com/smeltdev/smelt/internal/harness/status"
	"github.com/smeltdev/smelt/internal/review"
	"github.com/smeltdev/smelt/internal/smeltd"
)

func codexAuth(value string) HarnessAuth {
	return HarnessAuth{Credential: review.Credential{EnvVar: "SMELT_CODEX_AUTH_JSON", Value: value}}
}

func TestFactoryRunUsesTheRequestedHarnessAndItsOwnCredential(t *testing.T) {
	var got factory.RunOptions
	run := scriptedRun(factory.ResultPassed)
	d, root := newFactoryDaemon(t, func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		got = opts
		return run(ctx, opts)
	})
	d.rcfg.Codex = codexAuth("codex-secret")
	cfg := `{"orgID":"org-1","factory":{"models":{"codex":"gpt-5-codex","claude":"opus"}},"commands":[{"name":"test","run":"go test ./..."}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(root, ".smelt", "config.json"), []byte(cfg), 0o644))

	sess, err := d.startFactory(smeltd.FactoryRequest{Backend: smeltd.BackendCircleCI, ProjectRoot: root, Prompt: "go", Harness: "codex"})
	assert.NilError(t, err)
	detail := waitForSessionEnd(t, d, sess.ID)

	assert.Equal(t, got.Harness.Name(), harness.Codex)
	assert.Equal(t, got.Credential.Value, "codex-secret")
	assert.Equal(t, got.Model, "gpt-5-codex")
	assert.Equal(t, detail.Factory.Harness, "codex")
	assert.Equal(t, detail.Factory.Model, "gpt-5-codex")

	// An explicit model wins over the harness's configured one.
	sess, err = d.startFactory(smeltd.FactoryRequest{Backend: smeltd.BackendCircleCI, ProjectRoot: root, Prompt: "go", Model: "o3"})
	assert.NilError(t, err)
	waitForSessionEnd(t, d, sess.ID)
	assert.Equal(t, got.Harness.Name(), harness.Claude)
	assert.Equal(t, got.Model, "o3")
}

func TestFactoryRunRefusesAHarnessWithoutACredential(t *testing.T) {
	d, root := newFactoryDaemon(t, scriptedRun(factory.ResultPassed))
	_, err := d.startFactory(smeltd.FactoryRequest{Backend: smeltd.BackendCircleCI, ProjectRoot: root, Prompt: "go", Harness: "codex"})
	var ae *apiError
	assert.Assert(t, errors.As(err, &ae))
	assert.Equal(t, ae.status, http.StatusServiceUnavailable)
	assert.Check(t, cmp.Contains(ae.msg, "smelt auth login codex"))

	_, err = d.startFactory(smeltd.FactoryRequest{Backend: smeltd.BackendCircleCI, ProjectRoot: root, Prompt: "go", Harness: "gemini"})
	assert.Assert(t, errors.As(err, &ae))
	assert.Equal(t, ae.status, http.StatusBadRequest)
}

func TestFactoryRunRenewsTheCredentialBeforeStarting(t *testing.T) {
	var got factory.RunOptions
	run := scriptedRun(factory.ResultPassed)
	d, root := newFactoryDaemon(t, func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		got = opts
		return run(ctx, opts)
	})
	d.rcfg.Codex = HarnessAuth{Refresh: func(context.Context) (review.Credential, error) {
		return review.Credential{EnvVar: "SMELT_CODEX_AUTH_JSON", Value: "renewed"}, nil
	}}
	sess, err := d.startFactory(smeltd.FactoryRequest{Backend: smeltd.BackendCircleCI, ProjectRoot: root, Prompt: "go", Harness: "codex"})
	assert.NilError(t, err)
	waitForSessionEnd(t, d, sess.ID)
	assert.Equal(t, got.Credential.Value, "renewed")

	d.rcfg.Codex.Refresh = func(context.Context) (review.Credential, error) {
		return review.Credential{}, errors.New("your Codex login expires soon; run `smelt refresh codex`")
	}
	_, err = d.startFactory(smeltd.FactoryRequest{Backend: smeltd.BackendCircleCI, ProjectRoot: root, Prompt: "go", Harness: "codex"})
	assert.ErrorContains(t, err, "smelt refresh codex")
}

func TestFactoryStatusReportsTokensAndTheRateLimitSignal(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	resets := time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC)
	pct := 80
	run := func(ctx context.Context, opts factory.RunOptions) (factory.Report, error) {
		opts.OnStart("run-1", factory.Worktree{Path: "/w", Branch: "smelt/run-1"})
		opts.OnEvent(factory.Event{Kind: factory.EventImplementing, Round: 1})
		opts.OnEvent(factory.Event{Kind: factory.EventImplemented, Round: 1, Turn: factory.Turn{
			Harness: harness.Codex, Model: "gpt-5-codex", InputTokens: 100, CachedTokens: 40, OutputTokens: 7,
			RateLimit: &harness.RateLimit{Status: "warning", Bucket: "5h", Utilization: &pct, ResetsAt: &resets},
		}})
		return factory.Report{RunID: "run-1", Outcome: factory.Outcome{Result: factory.ResultPassed, Rounds: 1}}, nil
	}
	d, root := newFactoryDaemon(t, run)
	d.rcfg.Codex = codexAuth("codex-secret")
	sess, err := d.startFactory(smeltd.FactoryRequest{Backend: smeltd.BackendCircleCI, ProjectRoot: root, Prompt: "go", Harness: "codex"})
	assert.NilError(t, err)
	detail := waitForSessionEnd(t, d, sess.ID)

	impl := detail.Rounds[0].Implement
	assert.Equal(t, impl.Harness, "codex")
	assert.Equal(t, impl.Model, "gpt-5-codex")
	assert.DeepEqual(t, []int{impl.InputTokens, impl.CachedTokens, impl.OutputTokens}, []int{100, 40, 7})
	assert.Equal(t, impl.RateLimit.Status, "warning")

	dir, err := status.Dir()
	assert.NilError(t, err)
	seen, ok := status.LastSignal(dir, harness.Codex)
	assert.Assert(t, ok)
	assert.Equal(t, seen.Bucket, "5h")
}
