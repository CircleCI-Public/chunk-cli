package acceptance

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// factory is a public command group, so it and plan have to reach the user:
// listed by 'chunk commands', which skips hidden ones, and with help of their
// own. Neither help lists a command factory does not have.
func TestFactoryIsListedAndDocumented(t *testing.T) {
	env := testenv.NewTestEnv(t)

	list := binary.RunCLI(t, []string{"commands"}, env, env.HomeDir)
	assert.Equal(t, list.ExitCode, 0, "stderr: %s", list.Stderr)
	assert.Assert(t, strings.Contains(list.Stdout, "chunk factory plan"), "stdout: %s", list.Stdout)

	help := binary.RunCLI(t, []string{"factory", "--help"}, env, env.HomeDir)
	assert.Equal(t, help.ExitCode, 0, "stderr: %s", help.Stderr)
	for _, want := range []string{"Plan changes and build them in factory", "plan", "bootstrap"} {
		assert.Check(t, strings.Contains(help.Stdout, want), "missing %q in stdout: %s", want, help.Stdout)
	}
	for _, gone := range []string{"build [", "start", "pull", "eval"} {
		assert.Check(t, !strings.Contains(help.Stdout, gone), "%q in stdout: %s", gone, help.Stdout)
	}

	root := binary.RunCLI(t, []string{"--help"}, env, env.HomeDir)
	assert.Equal(t, root.ExitCode, 0, "stderr: %s", root.Stderr)
	assert.Check(t, strings.Contains(root.Stdout, "chunk factory plan"), "stdout: %s", root.Stdout)

	for _, args := range [][]string{{"--help"}, {"factory", "--help"}, {"watch", "--help"}} {
		result := binary.RunCLI(t, args, env, env.HomeDir)
		assert.Equal(t, result.ExitCode, 0, "%v stderr: %s", args, result.Stderr)
		for _, gone := range []string{"factory build", "factory start", "factory pull", "factory eval"} {
			assert.Check(t, !strings.Contains(result.Stdout, gone), "%v: %q in stdout: %s", args, gone, result.Stdout)
		}
	}
}

func TestFactoryWithoutASubcommandShowsHelp(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"factory"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "Available Commands:"), "stdout: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "plan"), "stdout: %s", result.Stdout)
}

func TestFactoryRequiresASubcommand(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"factory", "add a flag"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, `"add a flag" is not a factory command`), "stderr: %s", result.Stderr)
}

func TestFactoryOldCommandsAreGone(t *testing.T) {
	for _, name := range []string{"build", "start", "pull", "eval"} {
		t.Run(name, func(t *testing.T) {
			env := testenv.NewTestEnv(t)

			result := binary.RunCLI(t, []string{"factory", name}, env, env.HomeDir)

			assert.Check(t, cmp.Equal(result.ExitCode, 2), "stderr: %s", result.Stderr)
			assert.Check(t, cmp.Contains(result.Stderr, `"`+name+`" is not a factory command`))
		})
	}
}

// Bootstrap writes the config and sets up the sidecar image, neither of which
// needs GitHub, so a project with another origin, or none, and no GitHub
// token is set up all the same.
func TestFactoryBootstrapNeedsNoGitHubOrigin(t *testing.T) {
	tests := []struct {
		name string
		git  []string
	}{
		{
			name: "gitlab origin",
			git:  []string{"remote", "set-url", "origin", "https://gitlab.com/test-org/test-repo.git"},
		},
		{
			name: "no origin",
			git:  []string{"remote", "remove", "origin"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, _, workDir := setupReviewProject(t, nil)
			env.GithubToken = ""
			git := exec.Command("git", tt.git...)
			git.Dir = workDir
			git.Env = gitrepo.GitEnv(workDir)
			out, err := git.CombinedOutput()
			assert.NilError(t, err, string(out))
			err = os.Remove(filepath.Join(workDir, ".chunk", "config.json"))
			assert.NilError(t, err)

			result := binary.RunCLI(t, []string{"factory", "bootstrap", "--skip-sidecar"}, env, workDir)

			assert.Check(t, cmp.Equal(result.ExitCode, 0), "stderr: %s", result.Stderr)
			_, err = os.Stat(filepath.Join(workDir, ".chunk", "config.json"))
			assert.Check(t, err)
			// Narrowed to "needs a GitHub origin": detecting the VCS may warn
			// without a GitHub origin.
			assert.Check(t, !strings.Contains(result.Stderr, "needs a GitHub origin"), "stderr: %s", result.Stderr)
		})
	}
}

// A project with nothing left to set up is told it is ready for chunk
// factory, its review prompts are left as they are, and no sidecar boots to
// find that out.
func TestFactoryBootstrapSaysTheProjectIsAlreadySetUp(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		config map[string]interface{}
	}{
		{
			name:   "config and image",
			args:   []string{"factory", "bootstrap"},
			config: map[string]interface{}{"validation": map[string]interface{}{"sidecarImage": "snap-old"}},
		},
		{
			name: "config and --skip-sidecar",
			args: []string{"factory", "bootstrap", "--skip-sidecar"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, cci, workDir := setupReviewProject(t, nil, "mine")
			writeChunkConfig(t, workDir, tt.config)

			result := binary.RunCLI(t, tt.args, env, workDir)

			assert.Check(t, result.ExitCode != 0, "expected non-zero exit code")
			assert.Check(t, cmp.Contains(result.Stderr, "This project is already set up for chunk factory."))
			assert.Check(t, cmp.Contains(result.Stderr, "chunk factory plan"))
			assert.Check(t, cmp.Len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances"), 0))
			data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "reviews", "mine.md"))
			assert.NilError(t, err)
			assert.Check(t, cmp.Equal(string(data), "review mine"))
		})
	}
}

// Flows and plans work from this machine, so chunk watch refuses a remote
// daemon, and says so before it needs a terminal.
func TestWatchRefusesARemoteDaemon(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.Extra["CHUNK_DAEMON_REMOTE_ADDR"] = "127.0.0.1:1"
	env.Extra["CHUNK_DAEMON_TCP_TOKEN"] = "tok"

	result := binary.RunCLI(t, []string{"watch"}, env, env.HomeDir)

	assert.Check(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Check(t, cmp.Contains(result.Stderr, "Unset CHUNK_DAEMON_REMOTE_ADDR"))
}

// chunk watch refuses arguments as bad arguments.
func TestWatchTakesNoDirectories(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"watch", "some/dir"}, env, env.HomeDir)

	assert.Check(t, cmp.Equal(result.ExitCode, 2), "stderr: %s", result.Stderr)
	assert.Check(t, cmp.Contains(result.Stderr, "chunk watch takes no arguments"))
}

// readProjectConfig reads .chunk/config.json from workDir.
func readProjectConfig(t *testing.T, workDir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "config.json"))
	assert.NilError(t, err)
	var cfg map[string]any
	err = json.Unmarshal(data, &cfg)
	assert.NilError(t, err)
	return cfg
}

// claudeOnSidecar answers the check for Claude Code on a sidecar, which would
// otherwise depend on whether this machine has it. It returns how many times
// the check was run.
func claudeOnSidecar(sshSrv *fakes.SSHServer, installed bool) *atomic.Int32 {
	var checks atomic.Int32
	sshSrv.OverrideLocally(func(command string) (string, int, bool) {
		if !strings.Contains(command, "command -v claude") {
			return "", 0, false
		}
		checks.Add(1)
		// A real sidecar runs the command without a shell, so a script not
		// handed to one fails there the way a missing program does.
		if !strings.HasPrefix(command, "bash -l -c ") {
			return "", 127, true
		}
		if installed {
			return "", 0, true
		}
		return "", 97, true
	})
	return &checks
}

// sidecarImages returns the image of each sidecar the CLI created.
func sidecarImages(t *testing.T, cci *fakes.FakeCircleCI) []string {
	t.Helper()
	var images []string
	for _, r := range filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances") {
		if r.URL.Path != "/api/v3/sidecar/instances" {
			continue
		}
		var req struct {
			Data struct {
				Attributes struct {
					Image string `json:"image"`
				} `json:"attributes"`
			} `json:"data"`
		}
		err := json.Unmarshal(r.Body, &req)
		assert.NilError(t, err)
		images = append(images, req.Data.Attributes.Image)
	}
	return images
}

// Bootstrap sets the project's environment up on a sidecar of its own,
// snapshots it and records the snapshot, which is what chunk factory starts
// its sidecars from. Prompts the project has are left as they are.
func TestFactoryBootstrapRecordsTheSidecarImage(t *testing.T) {
	env, cci, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	claudeOnSidecar(sshSrv, true)
	marker := filepath.Join(t.TempDir(), "setup-ran")
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{
			"stack": "go",
			"setup": []map[string]any{{"name": "install", "command": "touch " + marker + " && echo installing"}},
		},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	_, err := os.Stat(marker)
	assert.NilError(t, err, "the setup step did not run")
	cfg := readProjectConfig(t, workDir)
	assert.Equal(t, cfg["validation"].(map[string]any)["sidecarImage"], "snap-1")
	// With no --image, the sidecar starts from the image that has Claude
	// Code, which a plan's build runs on snapshots of.
	assert.DeepEqual(t, sidecarImages(t, cci), []string{"cimg-base:2026.09-claude"})

	snaps := filterByMethod(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/snapshots")
	assert.Equal(t, len(snaps), 1)
	var body struct {
		Data struct {
			Attributes struct {
				Name string `json:"name"`
			} `json:"attributes"`
		} `json:"data"`
	}
	err = json.Unmarshal(snaps[0].Body, &body)
	assert.NilError(t, err)
	// Dated to the second, so a rerun on the same day gets a name of its own.
	assert.Assert(t, regexp.MustCompile(`^test-repo-\d{4}-\d{2}-\d{2}-\d{6}$`).MatchString(body.Data.Attributes.Name),
		"snapshot name: %s", body.Data.Attributes.Name)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "DELETE", "/api/v3/sidecar/instances/")), 1,
		"the sidecar that was snapshotted is deleted")

	// The setup steps' output is shown as they run, on stderr: stdout is for
	// the --json report.
	assert.Check(t, cmp.Contains(result.Stderr, "installing"))
	assert.Check(t, !strings.Contains(result.Stdout, "installing"), "stdout: %s", result.Stdout)
	assert.Check(t, cmp.Contains(result.Stderr, `chunk factory plan "<what to change>"`))
	// Bootstrap never touches the review prompts the project has.
	data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "reviews", "mine.md"))
	assert.NilError(t, err)
	assert.Check(t, cmp.Equal(string(data), "review mine"))
}

// An environment with no setup steps has nothing for a snapshot to hold, so no
// sidecar is made and the default image stays.
func TestFactoryBootstrapRecordsNoImageWithoutSetupSteps(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, nil, "mine")
	writeChunkConfig(t, workDir, map[string]interface{}{"environment": map[string]any{"stack": "unknown", "setup": []any{}}})

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "No setup steps detected"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0)
	_, hasValidation := readProjectConfig(t, workDir)["validation"]
	assert.Assert(t, !hasValidation, "no image is recorded")
}

// A project without .chunk/config.json gets one, as chunk init would write it,
// rather than being sent to run init first.
func TestFactoryBootstrapWritesTheConfig(t *testing.T) {
	for _, extra := range [][]string{{}, {"--json"}} {
		t.Run(strings.Join(append([]string{"bootstrap"}, extra...), " "), func(t *testing.T) {
			env, _, workDir := setupReviewProject(t, nil, "mine")
			err := os.Remove(filepath.Join(workDir, ".chunk", "config.json"))
			assert.NilError(t, err)

			result := binary.RunCLI(t, append([]string{"factory", "bootstrap", "--skip-sidecar"}, extra...), env, workDir)

			assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
			cfg := readProjectConfig(t, workDir)
			assert.Equal(t, cfg["orgID"], "org-aaa")
			vcs := cfg["vcs"].(map[string]any)
			assert.Equal(t, vcs["org"], "test-org")
			assert.Equal(t, vcs["repo"], "test-repo")
			// Hooks belong to chunk init.
			_, err = os.Stat(filepath.Join(workDir, ".claude", "settings.json"))
			assert.Assert(t, os.IsNotExist(err), "bootstrap wrote agent hooks")
			if len(extra) == 0 {
				return
			}
			var report map[string]any
			err = json.Unmarshal([]byte(result.Stdout), &report)
			assert.NilError(t, err, result.Stdout)
			assert.Check(t, cmp.DeepEqual(report, map[string]any{"config_written": true, "sidecar_image": ""}))
		})
	}
}

// With --json, what the run made is reported even when the sidecar image
// could not be set up. A project with a config is used, since a fresh one's
// environment is detected rather than chosen, so its setup step cannot be
// made to fail; TestFactoryBootstrapWritesTheConfig covers config_written.
func TestFactoryBootstrapReportsAsJSONWhenTheImageFails(t *testing.T) {
	env, _, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	claudeOnSidecar(sshSrv, true)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "exit 3"}}},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--json"}, env, workDir)

	assert.Check(t, result.ExitCode != 0, "expected non-zero exit code")
	// With --json the error is JSON on stderr too, so its quotes are escaped.
	assert.Check(t, cmp.Contains(result.Stderr, `Setup step \"install\" exited with status 3`))
	var report map[string]any
	err := json.Unmarshal([]byte(result.Stdout), &report)
	assert.NilError(t, err, result.Stdout)
	assert.Check(t, cmp.DeepEqual(report, map[string]any{"config_written": false, "sidecar_image": ""}))
}

// An --image without Claude Code is snapshotted and recorded, and no
// `command -v claude` check runs on the sidecar.
func TestFactoryBootstrapRecordsAnImageWithoutClaude(t *testing.T) {
	env, cci, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	checks := claudeOnSidecar(sshSrv, false)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "true"}}},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--image", "cimg-base:2026.09"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Check(t, cmp.DeepEqual(sidecarImages(t, cci), []string{"cimg-base:2026.09"}))
	assert.Check(t, cmp.Len(filterByMethod(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/snapshots"), 1))
	cfg := readProjectConfig(t, workDir)
	assert.Check(t, cmp.Equal(cfg["validation"].(map[string]any)["sidecarImage"], "snap-1"))
	assert.Check(t, cmp.Equal(checks.Load(), int32(0)), "bootstrap checked for Claude Code")
}

// Bootstrap's sidecar has a workspace of its own, which the setup steps run
// in, and the project's active sidecar is left as it was.
func TestFactoryBootstrapLeavesTheActiveSidecarAlone(t *testing.T) {
	env, _, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	claudeOnSidecar(sshSrv, true)
	marker := filepath.Join(t.TempDir(), "setup-dir")
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{
			"stack": "go",
			"setup": []map[string]any{{"name": "install", "command": "pwd -P > " + marker}},
		},
	})
	stateDir := projectDataDir(t, env, workDir)
	err := os.MkdirAll(stateDir, 0o755)
	assert.NilError(t, err)
	statePath := filepath.Join(stateDir, sidecar.StateFileName("", gitCurrentBranch(t, workDir)))
	state := []byte(`{"sidecar_ids":["sidecar-mine"],"workspace":"/home/user/mine"}`)
	err = os.WriteFile(statePath, state, 0o644)
	assert.NilError(t, err)

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	ran, err := os.ReadFile(marker)
	assert.NilError(t, err, "the setup step did not run")
	want, err := filepath.EvalSymlinks(filepath.Join(env.Extra["CHUNK_SIDECAR_HOME"], "test-repo"))
	assert.NilError(t, err)
	assert.Equal(t, strings.TrimSpace(string(ran)), want)
	after, err := os.ReadFile(statePath)
	assert.NilError(t, err)
	assert.Equal(t, string(after), string(state))
}

// sidecarImageProject is a project with a saved environment, so a bootstrap
// run only sets up the sidecar image.
func sidecarImageProject(t *testing.T) (*testenv.TestEnv, *fakes.FakeCircleCI, string) {
	t.Helper()
	env, cci, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	claudeOnSidecar(sshSrv, true)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "true"}}},
	})
	return env, cci, workDir
}

// snapshotNames returns the name of each snapshot the CLI asked for.
func snapshotNames(t *testing.T, cci *fakes.FakeCircleCI) []string {
	t.Helper()
	var names []string
	for _, r := range filterByMethod(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/snapshots") {
		var body struct {
			Data struct {
				Attributes struct {
					Name string `json:"name"`
				} `json:"attributes"`
			} `json:"data"`
		}
		err := json.Unmarshal(r.Body, &body)
		assert.NilError(t, err)
		names = append(names, body.Data.Attributes.Name)
	}
	return names
}

func TestFactoryBootstrapNamesTheSnapshotAsAsked(t *testing.T) {
	env, cci, workDir := sidecarImageProject(t)

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--snapshot-name", "compass-env"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.DeepEqual(t, snapshotNames(t, cci), []string{"compass-env"})
	assert.Equal(t, readProjectConfig(t, workDir)["validation"].(map[string]any)["sidecarImage"], "snap-1")
}

// The gate commands run on a sidecar from now on, which chunk validate does
// without being asked, so the run says it marked them.
func TestFactoryBootstrapSaysItMarkedTheGateCommandsRemote(t *testing.T) {
	env, _, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	claudeOnSidecar(sshSrv, true)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"commands":    []map[string]any{{"name": "lint", "run": "golangci-lint run", "role": "gate"}},
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "true"}}},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "Marked install and gate commands as remote"), "stderr: %s", result.Stderr)
	commands := readProjectConfig(t, workDir)["commands"].([]any)
	assert.Equal(t, commands[0].(map[string]any)["remote"], true)
}

func TestFactoryBootstrapReportsTheSidecarImageAsJSON(t *testing.T) {
	env, _, workDir := sidecarImageProject(t)

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--json"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	var report map[string]any
	err := json.Unmarshal([]byte(result.Stdout), &report)
	assert.NilError(t, err, result.Stdout)
	// sidecarImageProject has a config, so none is written.
	assert.Check(t, cmp.DeepEqual(report, map[string]any{"config_written": false, "sidecar_image": "snap-1"}))
}

// A name the org already has is explained, with the ways out, rather than
// left as a bare conflict from the API, and before any sidecar is made for
// the setup steps to run on.
func TestFactoryBootstrapExplainsATakenSnapshotName(t *testing.T) {
	env, cci, workDir := sidecarImageProject(t)
	cci.AddSnapshot(fakes.Snapshot{ID: "snap-old", OrgID: "org-aaa", Name: "compass-env"})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--snapshot-name", "compass-env"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "A snapshot named compass-env already exists"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "--snapshot-name"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "chunk config set validation.sidecarImage snap-old"), "stderr: %s", result.Stderr)
	_, hasValidation := readProjectConfig(t, workDir)["validation"]
	assert.Assert(t, !hasValidation, "no image is recorded")
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0,
		"a sidecar was made for a name that is taken")
}

// A taken name the listing did not show is still explained when the snapshot
// call refuses it, and the sidecar that was made for it is deleted. The fake
// keeps names unique across orgs, so a snapshot filed under no org stands in
// for one created after the listing.
func TestFactoryBootstrapExplainsATakenSnapshotNameAtTheSnapshot(t *testing.T) {
	env, cci, workDir := sidecarImageProject(t)
	cci.AddSnapshot(fakes.Snapshot{ID: "snap-old", Name: "compass-env"})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--snapshot-name", "compass-env"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "A snapshot named compass-env already exists"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "--snapshot-name"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "chunk config set validation.sidecarImage"), "stderr: %s", result.Stderr)
	_, hasValidation := readProjectConfig(t, workDir)["validation"]
	assert.Assert(t, !hasValidation, "no image is recorded")
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "DELETE", "/api/v3/sidecar/instances/")), 1,
		"the sidecar is deleted")
}

// Bootstrap never runs Claude, so it needs no Claude credential: a plan's
// build asks for its own when it starts.
func TestFactoryBootstrapNeedsNoClaudeCredential(t *testing.T) {
	env, _, workDir := sidecarImageProject(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Check(t, !strings.Contains(result.Stderr, "No Claude credential"), "stderr: %s", result.Stderr)
	cfg := readProjectConfig(t, workDir)
	assert.Check(t, cmp.Equal(cfg["validation"].(map[string]any)["sidecarImage"], "snap-1"))
}

// Bootstrap does not read .chunk/reviews, so one it cannot read changes
// nothing: a configured project is still set up already, and a fresh one
// still gets its config.
func TestFactoryBootstrapIgnoresTheReviewsDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads the directory whatever its mode")
	}
	unreadableReviews := func(t *testing.T, workDir string) {
		t.Helper()
		reviews := filepath.Join(workDir, ".chunk", "reviews")
		err := os.Chmod(reviews, 0o000)
		assert.NilError(t, err)
		t.Cleanup(func() { _ = os.Chmod(reviews, 0o755) })
	}

	t.Run("configured project", func(t *testing.T) {
		env, _, workDir := setupReviewProject(t, nil, "mine")
		unreadableReviews(t, workDir)

		result := binary.RunCLI(t, []string{"factory", "bootstrap", "--skip-sidecar"}, env, workDir)

		assert.Check(t, result.ExitCode != 0, "expected non-zero exit code")
		assert.Check(t, cmp.Contains(result.Stderr, "This project is already set up for chunk factory."))
		assert.Check(t, !strings.Contains(result.Stderr, ".chunk/reviews"), "stderr: %s", result.Stderr)
	})

	t.Run("fresh project", func(t *testing.T) {
		env, _, workDir := setupReviewProject(t, nil, "mine")
		err := os.Remove(filepath.Join(workDir, ".chunk", "config.json"))
		assert.NilError(t, err)
		unreadableReviews(t, workDir)

		result := binary.RunCLI(t, []string{"factory", "bootstrap", "--skip-sidecar"}, env, workDir)

		assert.Check(t, cmp.Equal(result.ExitCode, 0), "stderr: %s", result.Stderr)
		_, err = os.Stat(filepath.Join(workDir, ".chunk", "config.json"))
		assert.Check(t, err)
		assert.Check(t, !strings.Contains(result.Stderr, "Could not read .chunk/reviews"), "stderr: %s", result.Stderr)
	})
}
