package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/bootstrap"
	"github.com/CircleCI-Public/chunk-cli/internal/chunkd"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// fakeSetup stands in for bootstrap.Setup: each call runs script with its
// number, from 1.
type fakeSetup struct {
	mu     sync.Mutex
	calls  int
	script func(ctx context.Context, r bootstrap.Run, call int) (bootstrap.Result, error)
}

func (f *fakeSetup) run(ctx context.Context, r bootstrap.Run) (bootstrap.Result, error) {
	f.mu.Lock()
	f.calls++
	call := f.calls
	f.mu.Unlock()
	return f.script(ctx, r, call)
}

func (f *fakeSetup) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// sidecarAPI is a fake CircleCI that records the sidecars deleted from it.
type sidecarAPI struct {
	mu      sync.Mutex
	deleted []string
}

func (a *sidecarAPI) deletes() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.deleted...)
}

// setupDaemon is a test daemon with a CircleCI client, whose Setup runs are
// f's, stopped when the test ends.
func setupDaemon(t *testing.T, f *fakeSetup) (*daemon, *sidecarAPI) {
	t.Helper()
	api := &sidecarAPI{}
	cci := fakes.NewFakeCircleCI()
	const prefix = "/api/v3/sidecar/instances/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, prefix) {
			api.mu.Lock()
			api.deleted = append(api.deleted, strings.TrimPrefix(r.URL.Path, prefix))
			api.mu.Unlock()
		}
		cci.ServeHTTP(w, r)
	}))
	t.Cleanup(srv.Close)
	d := newTestDaemon()
	d.client = newTestClient(t, srv.URL)
	d.setups.run = f.run
	t.Cleanup(d.setups.stopAll)
	return d, api
}

// writeChecks writes root's config with n validation commands.
func writeChecks(t *testing.T, root string, n int) {
	t.Helper()
	cfg := &config.ProjectConfig{}
	for range n {
		cfg.Commands = append(cfg.Commands, config.Command{Name: "test", Run: "go test ./..."})
	}
	err := config.SaveProjectConfig(root, cfg)
	assert.NilError(t, err)
}

func startSetup(t *testing.T, d *daemon, root string) (int, chunkd.SetupActivity) {
	t.Helper()
	body, err := json.Marshal(chunkd.SetupRequest{
		ProjectRoot: root,
		OrgID:       "org-1",
	})
	assert.NilError(t, err)
	rec := serve(d, httptest.NewRequest(http.MethodPost, "/factory/setup", bytes.NewReader(body)))
	var act chunkd.SetupActivity
	if rec.Code < 300 {
		err := json.Unmarshal(rec.Body.Bytes(), &act)
		assert.NilError(t, err)
	}
	return rec.Code, act
}

func readSetup(t *testing.T, d *daemon, root string, after int) (int, chunkd.SetupActivity) {
	t.Helper()
	path := "/factory/setup?root=" + url.QueryEscape(root) + "&after=" + strconv.Itoa(after)
	rec := serve(d, httptest.NewRequest(http.MethodGet, path, nil))
	var act chunkd.SetupActivity
	if rec.Code == http.StatusOK {
		err := json.Unmarshal(rec.Body.Bytes(), &act)
		assert.NilError(t, err)
	}
	return rec.Code, act
}

func continueSetup(t *testing.T, d *daemon, root string) int {
	t.Helper()
	body, err := json.Marshal(chunkd.SetupRequest{ProjectRoot: root})
	assert.NilError(t, err)
	return serve(d, httptest.NewRequest(http.MethodPost, "/factory/setup/continue", bytes.NewReader(body))).Code
}

// waitSetup waits for root's run to reach state, and returns it.
func waitSetup(t *testing.T, d *daemon, root string, state chunkd.SetupState) chunkd.SetupActivity {
	t.Helper()
	var act chunkd.SetupActivity
	waitFor(t, func() bool {
		_, act = readSetup(t, d, root, 0)
		return act.State == state
	}, "setup never became "+string(state))
	return act
}

func feedTexts(act chunkd.SetupActivity) []string {
	var out []string
	for _, l := range act.Lines {
		out = append(out, l.Text)
	}
	return out
}

func event(r bootstrap.Run, step bootstrap.Step, text string) {
	r.OnEvent(bootstrap.Event{
		Step:  step,
		Level: iostream.LevelStep,
		Text:  text,
	})
}

func TestSetupRoutes(t *testing.T) {
	t.Run("start then read", func(t *testing.T) {
		release := make(chan struct{})
		f := &fakeSetup{script: func(_ context.Context, r bootstrap.Run, _ int) (bootstrap.Result, error) {
			event(r, bootstrap.StepConfig, "Detecting validation commands")
			writeChecks(t, r.WorkDir, 1)
			event(r, bootstrap.StepEnvironment, "Creating a sidecar")
			_, _ = r.Output.Write([]byte("step one\nstep two\n"))
			<-release
			return bootstrap.Result{Image: "snap-1"}, nil
		}}
		d, _ := setupDaemon(t, f)
		root := gitRepo(t)

		code, act := startSetup(t, d, root)
		assert.Check(t, cmp.Equal(code, http.StatusAccepted))
		assert.Check(t, cmp.Equal(act.State, chunkd.SetupRunning))

		waitFor(t, func() bool {
			_, act = readSetup(t, d, root, 0)
			return act.Total == 4
		}, "the feed never had its lines")
		assert.Check(t, cmp.DeepEqual(feedTexts(act), []string{"Detecting validation commands", "Creating a sidecar", "step one", "step two"}))
		assert.Check(t, cmp.Equal(act.Step, string(bootstrap.StepEnvironment)))
		assert.Check(t, cmp.Equal(act.Checks, 1))
		_, later := readSetup(t, d, root, 2)
		assert.Check(t, cmp.Equal(later.First, 2))
		assert.Check(t, cmp.DeepEqual(feedTexts(later), []string{"step one", "step two"}))

		close(release)
		act = waitSetup(t, d, root, chunkd.SetupOK)
		assert.Check(t, cmp.Equal(act.Image, "snap-1"))
	})

	t.Run("a second start returns the same run", func(t *testing.T) {
		release := make(chan struct{})
		f := &fakeSetup{script: func(_ context.Context, r bootstrap.Run, _ int) (bootstrap.Result, error) {
			writeChecks(t, r.WorkDir, 1)
			<-release
			return bootstrap.Result{}, nil
		}}
		d, _ := setupDaemon(t, f)
		root := gitRepo(t)

		_, first := startSetup(t, d, root)
		code, second := startSetup(t, d, root)
		assert.Check(t, cmp.Equal(code, http.StatusAccepted))
		assert.Check(t, cmp.Equal(first.State, chunkd.SetupRunning))
		assert.Check(t, cmp.Equal(second.State, chunkd.SetupRunning))
		close(release)
		waitSetup(t, d, root, chunkd.SetupOK)
		_, _ = startSetup(t, d, root)
		calls := f.count()
		assert.Check(t, cmp.Equal(calls, 1))
	})

	t.Run("continue", func(t *testing.T) {
		f := &fakeSetup{script: func(_ context.Context, r bootstrap.Run, _ int) (bootstrap.Result, error) {
			writeChecks(t, r.WorkDir, 1)
			return bootstrap.Result{}, &bootstrap.StepError{
				Step: bootstrap.StepEnvironment,
				Err:  errors.New("npm ci failed"),
			}
		}}
		d, _ := setupDaemon(t, f)
		root := gitRepo(t)

		_, _ = startSetup(t, d, root)
		act := waitSetup(t, d, root, chunkd.SetupStuck)
		assert.Check(t, cmp.Equal(act.Summary, "environment: npm ci failed"))
		code := continueSetup(t, d, root)
		assert.Check(t, cmp.Equal(code, http.StatusOK))
		_, act = readSetup(t, d, root, 0)
		assert.Check(t, cmp.Equal(act.State, chunkd.SetupContinued))
		err := d.setups.get(config.CanonicalProjectRoot(root)).wait(t.Context())
		assert.Check(t, cmp.Nil(err))

		ok := gitRepo(t)
		f.script = func(_ context.Context, r bootstrap.Run, _ int) (bootstrap.Result, error) {
			writeChecks(t, r.WorkDir, 1)
			return bootstrap.Result{Image: "snap-1"}, nil
		}
		_, _ = startSetup(t, d, ok)
		waitSetup(t, d, ok, chunkd.SetupOK)
		code = continueSetup(t, d, ok)
		assert.Check(t, cmp.Equal(code, http.StatusConflict))
	})

	t.Run("retry deletes the old sidecar", func(t *testing.T) {
		f := &fakeSetup{script: func(_ context.Context, r bootstrap.Run, call int) (bootstrap.Result, error) {
			writeChecks(t, r.WorkDir, 1)
			if call == 1 {
				r.OnSidecar("sc-1")
				return bootstrap.Result{}, &bootstrap.StepError{
					Step: bootstrap.StepSnapshot,
					Err:  errors.New("snapshot failed"),
				}
			}
			return bootstrap.Result{Image: "snap-2"}, nil
		}}
		d, api := setupDaemon(t, f)
		root := gitRepo(t)

		_, _ = startSetup(t, d, root)
		waitSetup(t, d, root, chunkd.SetupStuck)
		deleted := api.deletes()
		assert.Check(t, cmp.Len(deleted, 0))

		code, _ := startSetup(t, d, root)
		assert.Check(t, cmp.Equal(code, http.StatusAccepted))
		act := waitSetup(t, d, root, chunkd.SetupOK)
		assert.Check(t, cmp.Equal(act.Image, "snap-2"))
		deleted = api.deletes()
		assert.Check(t, cmp.DeepEqual(deleted, []string{"sc-1"}))
		calls := f.count()
		assert.Check(t, cmp.Equal(calls, 2))
	})

	t.Run("no commands, then a failed run starts a new attempt", func(t *testing.T) {
		release := make(chan struct{})
		var provisioned bool
		f := &fakeSetup{script: func(ctx context.Context, r bootstrap.Run, call int) (bootstrap.Result, error) {
			if call == 1 {
				event(r, bootstrap.StepConfig, "Detecting validation commands")
				writeChecks(t, r.WorkDir, 0)
				event(r, bootstrap.StepEnvironment, "Creating a sidecar")
				if ctx.Err() != nil {
					return bootstrap.Result{}, &bootstrap.StepError{
						Step: bootstrap.StepEnvironment,
						Err:  ctx.Err(),
					}
				}
				provisioned = true
				return bootstrap.Result{Image: "snap-1"}, nil
			}
			event(r, bootstrap.StepEnvironment, "Creating a sidecar")
			<-release
			return bootstrap.Result{Image: "snap-2"}, nil
		}}
		d, _ := setupDaemon(t, f)
		root := gitRepo(t)

		_, _ = startSetup(t, d, root)
		act := waitSetup(t, d, root, chunkd.SetupFailed)
		assert.Check(t, !provisioned, "provisioning was not skipped")
		assert.Check(t, cmp.Equal(act.Summary, "Add a validation command or a review prompt"))
		assert.Check(t, cmp.Equal(act.Checks, 0))
		err := d.setups.get(config.CanonicalProjectRoot(root)).wait(t.Context())
		assert.Check(t, cmp.ErrorContains(err, "Add a validation command"))

		// start on a failed run starts a new attempt
		writeChecks(t, root, 1)
		code, act := startSetup(t, d, root)
		assert.Check(t, cmp.Equal(code, http.StatusAccepted))
		assert.Check(t, cmp.Equal(act.State, chunkd.SetupRunning))
		assert.Check(t, cmp.Equal(act.Checks, -1))
		close(release)
		act = waitSetup(t, d, root, chunkd.SetupOK)
		calls := f.count()
		assert.Check(t, cmp.Equal(calls, 2))
		assert.Check(t, cmp.Equal(act.Checks, 1))
		assert.Check(t, cmp.Contains(feedTexts(act), "Retrying setup"))
	})

	t.Run("no run", func(t *testing.T) {
		d, _ := setupDaemon(t, &fakeSetup{})
		code, _ := readSetup(t, d, gitRepo(t), 0)
		assert.Check(t, cmp.Equal(code, http.StatusNotFound))
		code = continueSetup(t, d, gitRepo(t))
		assert.Check(t, cmp.Equal(code, http.StatusNotFound))
	})

	t.Run("no client", func(t *testing.T) {
		d := newTestDaemon()
		t.Cleanup(d.setups.stopAll)
		code, _ := startSetup(t, d, gitRepo(t))
		assert.Check(t, cmp.Equal(code, http.StatusServiceUnavailable))
	})

	t.Run("no org", func(t *testing.T) {
		d, _ := setupDaemon(t, &fakeSetup{})
		body, err := json.Marshal(chunkd.SetupRequest{ProjectRoot: gitRepo(t)})
		assert.NilError(t, err)
		rec := serve(d, httptest.NewRequest(http.MethodPost, "/factory/setup", bytes.NewReader(body)))
		assert.Check(t, cmp.Equal(rec.Code, http.StatusBadRequest))
	})

	t.Run("not a repository", func(t *testing.T) {
		d, _ := setupDaemon(t, &fakeSetup{})
		code, _ := startSetup(t, d, t.TempDir())
		assert.Check(t, cmp.Equal(code, http.StatusBadRequest))
	})
}
