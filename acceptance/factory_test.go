package acceptance

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fixtures"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// factory is a public command group, so both it and build have to reach the
// user: listed by 'chunk commands', which skips hidden ones, and with help of
// their own.
func TestFactoryIsListedAndDocumented(t *testing.T) {
	env := testenv.NewTestEnv(t)

	list := binary.RunCLI(t, []string{"commands"}, env, env.HomeDir)
	assert.Equal(t, list.ExitCode, 0, "stderr: %s", list.Stderr)
	assert.Assert(t, strings.Contains(list.Stdout, "chunk factory build"), "stdout: %s", list.Stdout)

	help := binary.RunCLI(t, []string{"factory", "--help"}, env, env.HomeDir)
	assert.Equal(t, help.ExitCode, 0, "stderr: %s", help.Stderr)
	for _, want := range []string{"Build and manage factory runs", "build"} {
		assert.Assert(t, strings.Contains(help.Stdout, want), "missing %q in stdout: %s", want, help.Stdout)
	}

	buildHelp := binary.RunCLI(t, []string{"factory", "build", "--help"}, env, env.HomeDir)
	assert.Equal(t, buildHelp.ExitCode, 0, "stderr: %s", buildHelp.Stderr)
	for _, want := range []string{"chunk factory build [prompt|-]", "--max-attempts", "--reviews", "--no-validate"} {
		assert.Assert(t, strings.Contains(buildHelp.Stdout, want), "missing %q in stdout: %s", want, buildHelp.Stdout)
	}
}

func TestFactoryWithoutASubcommandShowsHelp(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"factory"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "Available Commands:"), "stdout: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stdout, "build"), "stdout: %s", result.Stdout)
}

func TestFactoryRequiresASubcommand(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"factory", "add a flag"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, `"add a flag" is not a factory command`), "stderr: %s", result.Stderr)
}

// Build flags on the group must fail before pflag can consume the prompt as an
// unknown flag's value and turn a legacy invocation into successful help output.
func TestFactoryRejectsBuildFlagsWithoutBuildSubcommand(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"factory", "--no-validate", "add a flag"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "unknown flag: --no-validate"), "stderr: %s", result.Stderr)
}

func TestFactoryBuildWithoutAPromptExitsBadArgs(t *testing.T) {
	env := testenv.NewTestEnv(t)

	// With no argument the prompt is read from stdin, which is empty here.
	result := binary.RunCLIWithStdin(t, []string{"factory", "build"}, env, env.HomeDir, []byte{})

	assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "The prompt on stdin is empty."), "stderr: %s", result.Stderr)
}

// Everything below returns before the chunk daemon is started, so these are
// the paths a first run most often lands on, with nothing running.
func TestFactoryOutsideAGitRepoExitsBadArgs(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"factory", "build", "add a --verbose flag"}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "is not inside a git repository."), "stderr: %s", result.Stderr)
}

func TestFactoryWithoutProjectConfigPointsAtInit(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	result := binary.RunCLI(t, []string{"factory", "build", "add a --verbose flag"}, env, workDir)

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

	result := binary.RunCLI(t, []string{"factory", "build", "--no-validate", "add a --verbose flag"}, env, workDir)

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

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--no-history"}, env, workDir)

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

func bootstrapAnswer(t *testing.T) string {
	t.Helper()
	answer, err := json.Marshal(map[string]any{
		"type": "result", "subtype": "success", "is_error": false, "result": "done",
		"structured_output": map[string]any{
			"summary": "", "reviews": []map[string]any{{"name": "testing", "body": "# Testing"}},
		},
	})
	assert.NilError(t, err)
	return string(answer)
}

// The review comments on the project's GitHub repository reach the sidecar as
// a file, and Claude is told to read it.
func TestFactoryBootstrapUsesTheTeamsReviewHistory(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: bootstrapAnswer(t)})
	gh := fakes.NewFakeGitHub()
	gh.SetReviewActivity("test-repo", fixtures.ReviewActivityResponse())
	ghSrv := httptest.NewServer(gh)
	t.Cleanup(ghSrv.Close)
	env.GithubURL = ghSrv.URL

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--json"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "Fetching review comments on test-org/test-repo"), "stderr: %s", result.Stderr)
	var report struct {
		ReviewComments int `json:"review_comments"`
	}
	assert.NilError(t, json.Unmarshal([]byte(result.Stdout), &report), result.Stdout)
	assert.Equal(t, report.ReviewComments, 3)

	prompts := bootstrapPrompts(t, cci)
	assert.Equal(t, len(prompts), 1)
	assert.Assert(t, strings.Contains(prompts[0], "`.chunk/review-history.md` holds 3 review comments"), prompts[0])
	assert.Assert(t, !strings.Contains(prompts[0], "Consider using early return"), "the comments go in the file, not the prompt: %s", prompts[0])

	files, err := filepath.Glob(filepath.Join(env.Extra["CHUNK_SIDECAR_HOME"], "*", ".chunk", "review-history.md"))
	assert.NilError(t, err)
	assert.Equal(t, len(files), 1, "history on the sidecar: %v", files)
	data, err := os.ReadFile(files[0])
	assert.NilError(t, err)
	assert.Assert(t, strings.Contains(string(data), "> Consider using early return here to reduce nesting"), string(data))
}

// Without a GitHub token there is no history to fetch, and bootstrap goes on
// from the code alone.
func TestFactoryBootstrapGoesOnWithoutAGitHubToken(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: bootstrapAnswer(t)})
	env.GithubToken = ""

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "chunk auth set github"), "stderr: %s", result.Stderr)
	prompts := bootstrapPrompts(t, cci)
	assert.Equal(t, len(prompts), 1)
	assert.Assert(t, !strings.Contains(prompts[0], "review-history.md"), prompts[0])
}

// A token that cannot read the repository fails before any sidecar boots.
func TestFactoryBootstrapFailsWhenTheHistoryCannotBeFetched(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, nil)
	gh := fakes.NewFakeGitHub()
	gh.SetRepoError("test-repo", fixtures.RepoNotFoundError("test-org", "test-repo"))
	ghSrv := httptest.NewServer(gh)
	t.Cleanup(ghSrv.Close)
	env.GithubURL = ghSrv.URL

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Could not fetch the review comments on test-org/test-repo."), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "--no-history"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0)
}

// A template is written from the binary, so no sidecar boots and Claude never
// runs.
func TestFactoryBootstrapWritesATemplate(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, nil)

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--template", "python"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	for _, name := range []string{"correctness", "security", "testing"} {
		data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "reviews", name+".md"))
		assert.NilError(t, err)
		assert.Assert(t, strings.Contains(string(data), "## Severity"), "%s: %s", name, data)
		assert.Assert(t, strings.Contains(result.Stdout, filepath.Join(".chunk", "reviews", name+".md")), "stdout: %s", result.Stdout)
	}
	assert.Equal(t, len(cci.Recorder.AllRequests()), 0)
}

func TestFactoryBootstrapNamesTheTemplates(t *testing.T) {
	env, _, workDir := setupReviewProject(t, nil)

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--template", "rails"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, `No review template is named "rails"`), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "go-service"), "stderr: %s", result.Stderr)
	entries, err := os.ReadDir(filepath.Join(workDir, ".chunk", "reviews"))
	assert.NilError(t, err)
	assert.Equal(t, len(entries), 0)
}

func TestFactoryBootstrapTemplateLeavesExistingPromptsAlone(t *testing.T) {
	env, _, workDir := setupReviewProject(t, nil, "mine")

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--template", "generic"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "already has review prompts"), "stderr: %s", result.Stderr)
	_, err := os.Stat(filepath.Join(workDir, ".chunk", "reviews", "correctness.md"))
	assert.Assert(t, os.IsNotExist(err), "correctness.md: %v", err)
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
