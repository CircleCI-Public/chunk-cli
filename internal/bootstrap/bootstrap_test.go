package bootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

func TestPlan(t *testing.T) {
	withImage := &config.ProjectConfig{Validation: &config.ValidationConfig{SidecarImage: "snap-1"}}
	tests := []struct {
		name        string
		cfg         *config.ProjectConfig
		skipSidecar bool
		want        Steps
		wantSkipped []string
		empty       bool
	}{
		{
			name: "a new project gets everything",
			want: Steps{Config: true, Sidecar: true},
		},
		{
			name:        "an existing config is kept",
			cfg:         &config.ProjectConfig{},
			want:        Steps{Sidecar: true},
			wantSkipped: []string{".chunk/config.json already exists"},
		},
		{
			name:        "--skip-sidecar leaves the image out",
			skipSidecar: true,
			want:        Steps{Config: true},
			wantSkipped: []string{"sidecar image: --skip-sidecar"},
		},
		{
			name:        "a config and --skip-sidecar leave nothing to do",
			cfg:         &config.ProjectConfig{},
			skipSidecar: true,
			empty:       true,
			wantSkipped: []string{".chunk/config.json already exists", "sidecar image: --skip-sidecar"},
		},
		{
			name:        "a project set up already has nothing to do",
			cfg:         withImage,
			empty:       true,
			wantSkipped: []string{".chunk/config.json already exists", "sidecar image: validation.sidecarImage is snap-1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Plan(tt.cfg, tt.skipSidecar)
			assert.Check(t, cmp.DeepEqual(got.Skipped, tt.wantSkipped))
			assert.Check(t, cmp.Equal(got.Config, tt.want.Config))
			assert.Check(t, cmp.Equal(got.Sidecar, tt.want.Sidecar))
			assert.Check(t, cmp.Equal(got.Empty(), tt.empty))
		})
	}
}

// fakeAPI is a fake CircleCI API and a client pointed at it.
func fakeAPI(t *testing.T) (*fakes.FakeCircleCI, *circleci.Client) {
	t.Helper()
	t.Setenv(config.EnvHome, t.TempDir())
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)
	client, err := circleci.NewClient(circleci.Config{Token: "test-token", BaseURL: srv.URL})
	assert.NilError(t, err)
	return cci, client
}

// nodeRepo is a git repository with a package.json, so the environment has
// setup steps to make an image from.
func nodeRepo(t *testing.T) string {
	t.Helper()
	dir := gitrepo.SetupGitRepo(t, "acme", "widget")
	err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"widget","scripts":{"test":"jest"}}`), 0o644)
	assert.NilError(t, err)
	return dir
}

// fakeProvision reports a setup step and succeeds.
func fakeProvision(_ context.Context, opts sidecar.ProvisionOptions, _ *envbuilder.Environment) error {
	opts.Status(iostream.LevelStep, "Running setup steps...")
	return nil
}

func testRun(dir string, client *circleci.Client, events *[]Event) Run {
	return Run{
		WorkDir:   dir,
		Repo:      "widget",
		OrgID:     "org-1",
		Client:    client,
		Now:       time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		OnEvent:   func(ev Event) { *events = append(*events, ev) },
		Provision: fakeProvision,
	}
}

func createdSidecars(cci *fakes.FakeCircleCI) int {
	n := 0
	for _, r := range cci.Recorder.AllRequests() {
		if r.Method == http.MethodPost && r.URL.Path == "/api/v3/sidecar/instances" {
			n++
		}
	}
	return n
}

func TestSetupEventsInOrder(t *testing.T) {
	cci, client := fakeAPI(t)
	dir := nodeRepo(t)
	var events []Event

	res, err := Setup(context.Background(), testRun(dir, client, &events))
	assert.NilError(t, err)
	assert.Check(t, res.ConfigWritten)
	assert.Check(t, res.Image != "")
	assert.Check(t, cmp.Equal(createdSidecars(cci), 1))

	assert.Assert(t, len(events) > 0)
	assert.Check(t, cmp.Equal(events[0].Step, StepConfig))
	order := map[Step]int{StepConfig: 0, StepEnvironment: 1, StepSnapshot: 2}
	for i := 1; i < len(events); i++ {
		assert.Check(t, order[events[i].Step] >= order[events[i-1].Step], "event %d (%v) after %v", i, events[i], events[i-1])
	}
	var lastDone Event
	for _, ev := range events {
		if ev.Level == iostream.LevelDone {
			lastDone = ev
		}
	}
	assert.Check(t, cmp.Equal(lastDone.Step, StepSnapshot))
	assert.Check(t, cmp.Contains(lastDone.Text, "validation.sidecarImage"))
}

func TestSetupKeepsAnExistingConfig(t *testing.T) {
	_, client := fakeAPI(t)
	dir := nodeRepo(t)
	path := filepath.Join(dir, ".chunk", "config.json")
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	assert.NilError(t, err)
	err = os.WriteFile(path, []byte(`{"commands":[{"name":"test","run":"go test ./..."}]}`), 0o644)
	assert.NilError(t, err)
	var events []Event

	res, err := Setup(context.Background(), testRun(dir, client, &events))
	assert.NilError(t, err)
	assert.Check(t, !res.ConfigWritten)

	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	var got struct {
		Commands   []config.Command `json:"commands"`
		Validation struct {
			SidecarImage string `json:"sidecarImage"`
		} `json:"validation"`
	}
	err = json.Unmarshal(data, &got)
	assert.NilError(t, err)
	assert.Check(t, cmp.DeepEqual(got.Commands, []config.Command{{Name: "test", Run: "go test ./..."}}))
	assert.Check(t, cmp.Equal(got.Validation.SidecarImage, res.Image))
	assert.Check(t, res.Image != "")
}

func TestSetupRefusesAnUnparsableConfig(t *testing.T) {
	cci, client := fakeAPI(t)
	dir := nodeRepo(t)
	path := filepath.Join(dir, ".chunk", "config.json")
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	assert.NilError(t, err)
	err = os.WriteFile(path, []byte(`{`), 0o644)
	assert.NilError(t, err)
	var events []Event

	_, err = Setup(context.Background(), testRun(dir, client, &events))
	se, ok := errors.AsType[*StepError](err)
	assert.Assert(t, ok, "%v", err)
	assert.Check(t, cmp.Equal(se.Step, StepConfig))

	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(string(data), "{"))
	assert.Check(t, cmp.Equal(createdSidecars(cci), 0))
}
