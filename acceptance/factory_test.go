package acceptance

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// factory is a public command, so it has to reach the user: listed by
// 'chunk commands', which skips hidden ones, and with help of its own.
func TestFactoryIsListedAndDocumented(t *testing.T) {
	env := testenv.NewTestEnv(t)

	list := binary.RunCLI(t, []string{"commands"}, env, env.HomeDir)
	assert.Equal(t, list.ExitCode, 0, "stderr: %s", list.Stderr)
	assert.Assert(t, strings.Contains(list.Stdout, "chunk factory"), "stdout: %s", list.Stdout)

	help := binary.RunCLI(t, []string{"factory", "--help"}, env, env.HomeDir)
	assert.Equal(t, help.ExitCode, 0, "stderr: %s", help.Stderr)
	for _, want := range []string{"chunk factory [prompt|-]", "--attempts", "--reviews", "--no-validate"} {
		assert.Assert(t, strings.Contains(help.Stdout, want), "missing %q in stdout: %s", want, help.Stdout)
	}
}

func TestFactoryWithoutAPromptExitsBadArgs(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// With no argument the prompt is read from stdin, which is empty here.
	result := binary.RunCLIWithStdin(t, []string{"factory"}, env, env.HomeDir, []byte{})

	assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "The prompt on stdin is empty."), "stderr: %s", result.Stderr)
}

// Everything below returns before the chunk daemon is started, so these are
// the paths a first run most often lands on, with nothing running.
func TestFactoryOutsideAGitRepoExitsBadArgs(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"factory", "add a --verbose flag"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "is not inside a git repository."), "stderr: %s", result.Stderr)
}

func TestFactoryWithoutProjectConfigPointsAtInit(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	result := binary.RunCLI(t, []string{"factory", "add a --verbose flag"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "No validate commands configured"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "chunk init"), "stderr: %s", result.Stderr)
}

// Without reviews or validation commands the loop would pass whatever the
// implementer wrote, so the run is refused instead.
func TestFactoryWithNothingToCheck(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	writeChunkConfig(t, workDir, nil)

	result := binary.RunCLI(t, []string{"factory", "--no-validate", "add a --verbose flag"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Nothing to check the implementer's work with."), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, ".chunk/reviews"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "chunk factory bootstrap"), "stderr: %s", result.Stderr)
}

func TestFactoryBootstrapWritesReviewPrompts(t *testing.T) {
	answer, err := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "result": "done",
		"structured_output": map[string]any{
			"summary": "A small Go project.",
			"reviews": []map[string]any{
				{"name": "testing", "body": "# Testing\n\nCheck the tests."},
				{"name": "correctness", "body": "# Correctness"},
			},
		},
	})
	assert.NilError(t, err)
	env, cci, workDir := setupReviewProject(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: string(answer)})

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "reviews", "testing.md"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), "# Testing\n\nCheck the tests.\n")
	_, err = os.Stat(filepath.Join(workDir, ".chunk", "reviews", "correctness.md"))
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(result.Stdout, filepath.Join(".chunk", "reviews", "testing.md")), "stdout: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "A small Go project."), "stdout: %s", result.Stdout)

	var bootstraps int
	for _, r := range cci.Recorder.AllRequests() {
		if strings.HasSuffix(r.URL.Path, "/exec") && strings.Contains(string(r.Body), "--json-schema") {
			bootstraps++
		}
	}
	assert.Equal(t, bootstraps, 1)

	// Bootstrap needs Claude Code rather than the project's toolchain, so its
	// sidecar is made from the Claude image, not the project's own.
	var created int
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
		assert.NilError(t, json.Unmarshal(r.Body, &req))
		assert.Equal(t, req.Data.Attributes.Image, "cimg-base:2026.09-claude")
		created++
	}
	assert.Equal(t, created, 1)
}

// bootstrapPrompts returns the prompt of every bootstrap claude run the CLI
// sent to a sidecar.
func bootstrapPrompts(t *testing.T, cci *fakes.FakeCircleCI) []string {
	t.Helper()
	echoed := regexp.MustCompile(`echo '?([A-Za-z0-9+/=]+)'? \| base64 -d`)
	var prompts []string
	for _, r := range cci.Recorder.AllRequests() {
		if !strings.HasSuffix(r.URL.Path, "/exec") || !strings.Contains(string(r.Body), "--json-schema") {
			continue
		}
		var req struct {
			Args []string `json:"args"`
		}
		assert.NilError(t, json.Unmarshal(r.Body, &req))
		m := echoed.FindStringSubmatch(strings.Join(req.Args, " "))
		assert.Assert(t, m != nil, "no prompt in exec: %s", r.Body)
		prompt, err := base64.StdEncoding.DecodeString(m[1])
		assert.NilError(t, err)
		prompts = append(prompts, string(prompt))
	}
	return prompts
}

// The team's standards from build-prompt, when the project has them, reach
// Claude with the request.
func TestFactoryBootstrapUsesTheTeamsStandards(t *testing.T) {
	answer, err := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "result": "done",
		"structured_output": map[string]any{
			"summary": "", "reviews": []map[string]any{{"name": "testing", "body": "# Testing"}},
		},
	})
	assert.NilError(t, err)
	env, cci, workDir := setupReviewProject(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: string(answer)})
	path := filepath.Join(workDir, ".chunk", "context", "review-prompt.md")
	assert.NilError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	assert.NilError(t, os.WriteFile(path, []byte("Name every goroutine's exit."), 0o644))

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	prompts := bootstrapPrompts(t, cci)
	assert.Equal(t, len(prompts), 1)
	assert.Assert(t, strings.Contains(prompts[0], "Name every goroutine's exit."), prompts[0])
}

// A project that has review prompts keeps them, and no sidecar boots to find
// that out.
func TestFactoryBootstrapLeavesExistingPromptsAlone(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, nil, "mine")

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "already has review prompts"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "--output"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0)
	data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "reviews", "mine.md"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), "review mine")
}
