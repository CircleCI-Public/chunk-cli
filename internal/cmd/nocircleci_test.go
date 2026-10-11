package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/smeltdev/smelt/internal/config"
	"github.com/smeltdev/smelt/internal/testing/fakes"
	"github.com/smeltdev/smelt/internal/testing/gitrepo"
	"github.com/smeltdev/smelt/internal/upgrade"
)

// requestLog records the requests a fake server saw.
type requestLog struct {
	mu    sync.Mutex
	paths []string
}

func (l *requestLog) add(r *http.Request) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.paths = append(l.paths, r.Method+" "+r.URL.Path)
}

func (l *requestLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.paths...)
}

// noCircleCIEnv is a machine with a CircleCI token and nothing else going for
// it: the situation in which smelt must still never contact CircleCI.
type noCircleCIEnv struct {
	circleci *requestLog
	github   *requestLog
}

// newNoCircleCIEnv points every CircleCI and GitHub URL at recording servers.
// The CircleCI one answers like the real API so the opt-in path can run; the
// default-config tests assert it was never asked anything.
func newNoCircleCIEnv(t *testing.T) *noCircleCIEnv {
	t.Helper()
	isolateConfig(t)

	env := &noCircleCIEnv{circleci: &requestLog{}, github: &requestLog{}}

	fake := fakes.NewFakeCircleCI()
	cci := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.circleci.add(r)
		fake.ServeHTTP(w, r)
	}))
	t.Cleanup(cci.Close)

	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.github.add(r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"tag_name":"v1.0.0"}`))
	}))
	t.Cleanup(gh.Close)

	t.Setenv(config.EnvCircleCIBaseURL, cci.URL)
	t.Setenv(config.EnvGitHubAPIURL, gh.URL)
	// A valid-looking token is on the machine: having one must cause no traffic.
	t.Setenv(config.EnvCircleToken, "CCIPAT_looks-valid-0123456789abcdef")
	t.Setenv(config.EnvCircleCIToken, "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv(config.EnvCI, "")
	t.Setenv(config.EnvSmeltNoUpdateCheck, "")
	t.Setenv("SMELT_NO_TELEMETRY", "1")
	return env
}

func (e *noCircleCIEnv) assertNoCircleCI(t *testing.T) {
	t.Helper()
	assert.DeepEqual(t, e.circleci.all(), []string(nil))
}

func runSmelt(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	var out, errOut bytes.Buffer
	root := newTestRootCmd()
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// project is a git repo with a Go module and the given .smelt/config.json.
func noCircleCIProject(t *testing.T, cfgJSON string) string {
	t.Helper()
	dir := gitrepo.SetupGitRepo(t, "acme", "widgets")
	assert.NilError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/widgets\n\ngo 1.22\n"), 0o644))
	if cfgJSON != "" {
		assert.NilError(t, os.MkdirAll(filepath.Join(dir, ".smelt"), 0o755))
		assert.NilError(t, os.WriteFile(filepath.Join(dir, ".smelt", "config.json"), []byte(cfgJSON), 0o644))
	}
	return dir
}

const remoteLintConfig = `{"commands":[{"name":"lint","run":"echo ran-locally-lint","remote":true}]}`

func TestDefaultConfigNeverReachesCircleCI(t *testing.T) {
	t.Run("init", func(t *testing.T) {
		env := newNoCircleCIEnv(t)
		dir := noCircleCIProject(t, "")

		_, stderr, err := runSmelt(t, "", "init", "--project-dir", dir, "--skip-auth", "--skip-completions")
		assert.NilError(t, err, stderr)

		env.assertNoCircleCI(t)

		cfg, err := config.LoadProjectConfig(dir)
		assert.NilError(t, err)
		assert.Equal(t, cfg.OrgID, "")
		assert.Assert(t, !cfg.UsesCircleCI())

		// The CircleCI sidecar skills and scaffolding stay out; generic skills go in.
		_, err = os.Stat(filepath.Join(dir, ".claude", "skills", "smelt-sidecar"))
		assert.Assert(t, os.IsNotExist(err), "smelt-sidecar skill installed on the docker path")
		_, err = os.Stat(filepath.Join(dir, ".claude", "skills", "smelt-sidecar-setup"))
		assert.Assert(t, os.IsNotExist(err), "smelt-sidecar-setup skill installed on the docker path")
		_, err = os.Stat(filepath.Join(dir, ".claude", "skills", "smelt-validate-config", "SKILL.md"))
		assert.NilError(t, err)
		_, err = os.Stat(filepath.Join(dir, ".circleci"))
		assert.Assert(t, os.IsNotExist(err), ".circleci scaffolded on the docker path")
		assert.Assert(t, !strings.Contains(stderr, "validate --remote"), stderr)
	})

	t.Run("validate runs a remote command locally", func(t *testing.T) {
		env := newNoCircleCIEnv(t)
		dir := noCircleCIProject(t, remoteLintConfig)

		stdout, stderr, err := runSmelt(t, "", "validate", "--project", dir, "--no-daemon")
		assert.NilError(t, err, stderr)

		assert.Assert(t, strings.Contains(stdout+stderr, "ran-locally-lint"), "stdout=%q stderr=%q", stdout, stderr)
		assert.Equal(t, strings.Count(stderr, "remote validation needs the circleci backend"), 1, stderr)
		assert.Assert(t, strings.Contains(stderr, "running lint locally"), stderr)
		env.assertNoCircleCI(t)
	})

	t.Run("the git pre-commit and agent hooks run locally", func(t *testing.T) {
		env := newNoCircleCIEnv(t)
		t.Setenv(config.EnvCircleToken, "") // and work with no CircleCI auth at all
		dir := noCircleCIProject(t, remoteLintConfig)

		stdout, stderr, err := runValidateHook(t, dir)
		assert.NilError(t, err, "stdout=%q stderr=%q", stdout, stderr)
		assert.Assert(t, !strings.Contains(stderr, "CircleCI auth"), stderr)
		env.assertNoCircleCI(t)
	})

	t.Run("validate --remote explains the opt-in instead of falling back", func(t *testing.T) {
		env := newNoCircleCIEnv(t)
		dir := noCircleCIProject(t, remoteLintConfig)

		_, _, err := runSmelt(t, "", "validate", "--project", dir, "--no-daemon", "--remote")
		var ue *userError
		assert.Assert(t, asUserError(err, &ue), "got %T: %v", err, err)
		assert.Assert(t, strings.Contains(ue.UserMessage(), "circleci backend"), ue.UserMessage())
		assert.Assert(t, strings.Contains(ue.Suggestion(), "circleci"), ue.Suggestion())
		env.assertNoCircleCI(t)
	})

	t.Run("factory settings resolve to docker", func(t *testing.T) {
		env := newNoCircleCIEnv(t)

		got, err := resolveFactorySettings(factorySettings{Attempts: 3}, factoryExplicit{},
			&config.ProjectConfig{Factory: &config.FactoryConfig{Image: "smelt-sandbox:local"}})
		assert.NilError(t, err)
		assert.Equal(t, got.Backend, config.BackendDocker)
		env.assertNoCircleCI(t)
	})

	t.Run("the startup update check asks the fork, never upstream", func(t *testing.T) {
		env := newNoCircleCIEnv(t)

		// What upgrade.Check does on startup, with a release version: version.Value
		// itself is not touched, since earlier commands' background checks read it.
		stateDir, err := config.AppState()
		assert.NilError(t, err)
		upgrade.CheckForUpdate(stateDir, os.Getenv(config.EnvGitHubAPIURL), "v1.0.0")

		env.assertNoCircleCI(t)
		got := env.github.all()
		assert.DeepEqual(t, got, []string{"GET /repos/smeltdev/smelt/releases/latest"})
		for _, p := range got {
			assert.Assert(t, !strings.Contains(p, "CircleCI-Public"), p)
		}
	})
}

func TestExplicitCircleCIBackendStillWorks(t *testing.T) {
	t.Run("init on the circleci backend detects the org and installs the sidecar skills", func(t *testing.T) {
		env := newNoCircleCIEnv(t)
		dir := noCircleCIProject(t, "")

		_, stderr, err := runSmelt(t, "", "init", "--project-dir", dir, "--skip-auth", "--skip-completions", "--factory-backend", "circleci")
		assert.NilError(t, err, stderr)

		assert.Assert(t, len(env.circleci.all()) > 0, "the circleci backend made no CircleCI requests")
		cfg, err := config.LoadProjectConfig(dir)
		assert.NilError(t, err)
		assert.Assert(t, cfg.UsesCircleCI())
		for _, skill := range []string{"smelt-sidecar", "smelt-sidecar-setup", "smelt-validate-config"} {
			_, err = os.Stat(filepath.Join(dir, ".claude", "skills", skill, "SKILL.md"))
			assert.NilError(t, err, skill)
		}
		assert.Assert(t, strings.Contains(stderr, "validate --remote"), stderr)
	})

	t.Run("validate sends the project's commands to CircleCI", func(t *testing.T) {
		env := newNoCircleCIEnv(t)
		dir := noCircleCIProject(t, `{"factory":{"backend":"circleci"},"orgID":"org-1",`+
			`"commands":[{"name":"lint","run":"echo should-not-run-locally","remote":true}]}`)

		stdout, stderr, _ := runSmelt(t, "", "validate", "--project", dir, "--no-daemon")

		// Whatever became of the run, it went to CircleCI rather than here.
		assert.Assert(t, len(env.circleci.all()) > 0, "no CircleCI requests; stdout=%q stderr=%q", stdout, stderr)
		assert.Assert(t, !strings.Contains(stderr, "running lint locally"), stderr)
	})
}

func TestUsesCircleCI(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.ProjectConfig
		want bool
	}{
		{"nil config", nil, false},
		{"empty config", &config.ProjectConfig{}, false},
		{"factory section without a backend", &config.ProjectConfig{Factory: &config.FactoryConfig{}}, false},
		{"docker", &config.ProjectConfig{Factory: &config.FactoryConfig{Backend: "docker"}}, false},
		{"circleci", &config.ProjectConfig{Factory: &config.FactoryConfig{Backend: "circleci"}}, true},
		{"an org ID is not consent", &config.ProjectConfig{OrgID: "org-1", Validation: &config.ValidationConfig{SidecarImage: "snap"}}, false},
		{"remote commands are not consent", &config.ProjectConfig{Commands: []config.Command{{Name: "t", Run: "x", Remote: true}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.cfg.UsesCircleCI(), tt.want)
		})
	}
}

func TestPlanInitBackendDecidesTheCircleCISteps(t *testing.T) {
	docker := planInit(false)
	assert.Assert(t, !docker.DetectOrgID && !docker.TestSuites && !docker.RemoteHints)
	assert.DeepEqual(t, docker.Skills, []string{"smelt-validate-config"})

	circle := planInit(true)
	assert.Assert(t, circle.DetectOrgID && circle.TestSuites && circle.RemoteHints)
	assert.DeepEqual(t, circle.Skills, []string{"smelt-sidecar", "smelt-sidecar-setup", "smelt-validate-config"})
}
