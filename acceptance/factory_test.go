package acceptance

import (
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
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
	assert.Assert(t, strings.Contains(result.Stderr, "chunk factory bootstrap"), "stderr: %s", result.Stderr)
}

// A config that cannot be read is reported as such, not as a project with no
// config: bootstrap, which that would point at, refuses it too.
func TestFactoryWithAnUnreadableProjectConfig(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	assert.NilError(t, os.MkdirAll(filepath.Join(workDir, ".chunk"), 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, ".chunk", "config.json"), []byte("{"), 0o644))

	result := binary.RunCLI(t, []string{"factory", "build", "add a --verbose flag"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Could not read .chunk/config.json"), "stderr: %s", result.Stderr)
	assert.Assert(t, !strings.Contains(result.Stderr, "No validate commands configured"), "stderr: %s", result.Stderr)
}

// A project with no sidecar image is told its sidecars start from the default
// image, unless one is given for the run. --continue with a run that does not
// exist stops the command just after, before the daemon is started.
func TestFactoryHintsAtAMissingSidecarImage(t *testing.T) {
	for _, tc := range []struct {
		name     string
		extra    map[string]interface{}
		args     []string
		wantHint bool
	}{
		{name: "no image", wantHint: true},
		{name: "image in config", extra: map[string]interface{}{"validation": map[string]any{"sidecarImage": "snap-1"}}},
		{name: "image flag", args: []string{"--image", "snap-2"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := testenv.NewTestEnv(t)
			workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
			writeChunkConfig(t, workDir, tc.extra)

			args := append([]string{"factory", "build", "--continue", "nope"}, tc.args...)
			result := binary.RunCLI(t, append(args, "add a --verbose flag"), env, workDir)

			assert.Equal(t, result.ExitCode, 2, "stderr: %s", result.Stderr)
			assert.Assert(t, strings.Contains(result.Stderr, "No factory run nope to continue"), "stderr: %s", result.Stderr)
			hint := strings.Contains(result.Stderr, "No validation.sidecarImage, so sidecars start from the default image")
			assert.Equal(t, hint, tc.wantHint, "stderr: %s", result.Stderr)
		})
	}
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

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--no-history", "--skip-sidecar"}, env, workDir)

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

// --top keeps only the most active reviewers' comments.
func TestFactoryBootstrapKeepsTheTopReviewers(t *testing.T) {
	env, _, workDir := setupReviewProject(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: bootstrapAnswer(t)})
	gh := fakes.NewFakeGitHub()
	gh.SetReviewActivity("test-repo", fixtures.MultiReviewerResponse())
	ghSrv := httptest.NewServer(gh)
	t.Cleanup(ghSrv.Close)
	env.GithubURL = ghSrv.URL

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--json", "--top", "1"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	var report struct {
		ReviewComments int `json:"review_comments"`
	}
	assert.NilError(t, json.Unmarshal([]byte(result.Stdout), &report), result.Stdout)
	assert.Equal(t, report.ReviewComments, 3)
}

// --since leaves out the comments made before it.
func TestFactoryBootstrapOnlyUsesCommentsSinceTheGivenDate(t *testing.T) {
	env, _, workDir := setupReviewProject(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: bootstrapAnswer(t)})
	gh := fakes.NewFakeGitHub()
	gh.SetReviewActivity("test-repo", fixtures.ReviewActivityResponse())
	ghSrv := httptest.NewServer(gh)
	t.Cleanup(ghSrv.Close)
	env.GithubURL = ghSrv.URL
	since := time.Now().AddDate(0, 0, -10).Format(time.DateOnly)

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--json", "--since", since}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "since "+since), "stderr: %s", result.Stderr)
	var report struct {
		ReviewComments int `json:"review_comments"`
	}
	assert.NilError(t, json.Unmarshal([]byte(result.Stdout), &report), result.Stdout)
	assert.Equal(t, report.ReviewComments, 0)
}

// A bad --since or --top fails before any sidecar boots.
func TestFactoryBootstrapRejectsBadHistoryFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "since", args: []string{"--since", "31/01/2026"}, want: "--since must be a date"},
		{name: "top", args: []string{"--top", "0"}, want: "--top must be at least 1, not 0."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, cci, workDir := setupReviewProject(t, nil)

			result := binary.RunCLI(t, append([]string{"factory", "bootstrap"}, tt.args...), env, workDir)

			assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
			assert.Assert(t, strings.Contains(result.Stderr, tt.want), "stderr: %s", result.Stderr)
			assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0)
		})
	}
}

// --eval-commits that cannot score anything fails before any sidecar boots,
// including when the history has no commit to make a case of.
func TestFactoryBootstrapRefusesEvalCommitsThatScoreNothing(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "zero", args: []string{"--eval-commits", "0"}, want: "--eval-commits must be at least 1, not 0."},
		{name: "template", args: []string{"--eval-commits", "3", "--template", "generic"}, want: "[eval-commits template] were all set"},
		{name: "no usable commits", args: []string{"--eval-commits", "3", "--no-history"}, want: "--eval-commits found no commits on HEAD to score the prompts with."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, cci, workDir := setupReviewProject(t, nil)

			result := binary.RunCLI(t, append([]string{"factory", "bootstrap"}, tt.args...), env, workDir)

			assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
			assert.Assert(t, strings.Contains(result.Stderr, tt.want), "stderr: %s", result.Stderr)
			assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0)
		})
	}
}

// Without a GitHub token there is no history to fetch, and bootstrap goes on
// from the code alone.
func TestFactoryBootstrapGoesOnWithoutAGitHubToken(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: bootstrapAnswer(t)})
	env.GithubToken = ""

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "chunk auth set github"), "stderr: %s", result.Stderr)
	// The plan says so up front, rather than promising comments it then
	// leaves out.
	assert.Assert(t, strings.Contains(result.Stderr, "Write review prompts to .chunk/reviews from the code\n"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "Skipping: the team's review comments: no GitHub token"), "stderr: %s", result.Stderr)
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

// The sidecar's copy of the repository is named from origin, so a non-GitHub
// origin fails before any sidecar boots, with or without history.
func TestFactoryBootstrapNeedsAGitHubOrigin(t *testing.T) {
	for _, args := range [][]string{{"factory", "bootstrap"}, {"factory", "bootstrap", "--no-history"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			env, cci, workDir := setupReviewProject(t, nil)
			git := exec.Command("git", "remote", "set-url", "origin", "https://gitlab.com/test-org/test-repo.git")
			git.Dir = workDir
			git.Env = gitrepo.GitEnv(workDir)
			out, err := git.CombinedOutput()
			assert.NilError(t, err, string(out))

			result := binary.RunCLI(t, args, env, workDir)

			assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
			assert.Assert(t, strings.Contains(result.Stderr, "needs a GitHub origin"), "stderr: %s", result.Stderr)
			assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0)
		})
	}
}

// A project that is set up already keeps its review prompts, and no sidecar
// boots to find that out.
func TestFactoryBootstrapLeavesExistingPromptsAlone(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, nil, "mine")
	writeChunkConfig(t, workDir, map[string]interface{}{"validation": map[string]interface{}{"sidecarImage": "snap-old"}})

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "already has review prompts"), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "--output"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0)
	data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "reviews", "mine.md"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), "review mine")
}

// A suite that cannot run here is refused before any sidecar starts: no
// CircleCI is configured, so getting further would fail differently.
func TestFactoryEvalRefusesCasesThatCannotRun(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	suite := t.TempDir()
	assert.NilError(t, os.WriteFile(filepath.Join(suite, "case.patch"), []byte("diff --git a/x b/x\n"), 0o644))
	assert.NilError(t, os.WriteFile(filepath.Join(suite, "suite.json"), []byte(`{"cases": [
		{"name": "gone", "revision": "0000000000000000000000000000000000000000", "request": "r", "patch": "case.patch"}
	]}`), 0o644))

	result := binary.RunCLI(t, []string{"factory", "eval", "--suite", suite}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Some cases cannot run in this repository."), "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "case gone: revision 0000000000000000000000000000000000000000 is not in this repository"), "stderr: %s", result.Stderr)
}

// Commits carry no labels, so without the grader nothing would score them,
// and a run needs one source of cases or the other.
func TestFactoryEvalRefusesFlagsThatScoreNothing(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"factory", "eval"}, "at least one of the flags in the group [suite commits] is required"},
		{[]string{"factory", "eval", "--commits", "3", "--no-grade"}, "[commits no-grade] were all set"},
		{[]string{"factory", "eval", "--suite", t.TempDir(), "--commits", "3"}, "[commits suite] were all set"},
		{[]string{"factory", "eval", "--suite", t.TempDir(), "--no-grade", "--grader-model", "x"}, "[grader-model no-grade] were all set"},
		// The group is satisfied by --suite being given, so an empty one would
		// otherwise fall through to the commits path and fail there.
		{[]string{"factory", "eval", "--suite", ""}, "--suite needs a directory"},
	} {
		result := binary.RunCLI(t, tc.args, env, workDir)
		assert.Assert(t, result.ExitCode != 0, "%v: expected non-zero exit code", tc.args)
		assert.Assert(t, strings.Contains(result.Stderr, tc.want), "%v: stderr: %s", tc.args, result.Stderr)
	}
}

// A revision with no history to take commits from is refused before any
// sidecar starts.
func TestFactoryEvalRefusesCommitsFromAMissingRevision(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")

	result := binary.RunCLI(t, []string{"factory", "eval", "--commits", "3", "--from", "no-such-branch"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Could not make cases of the commits on no-such-branch."), "stderr: %s", result.Stderr)
}

// Without a GitHub origin the sidecars cannot name their copy of the
// repository, which is said before any credential or org prompt.
func TestFactoryEvalNeedsAGitHubOrigin(t *testing.T) {
	env := testenv.NewTestEnv(t)
	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	cmd := exec.Command("git", "remote", "remove", "origin")
	cmd.Dir = workDir
	cmd.Env = gitrepo.GitEnv(workDir)
	out, err := cmd.CombinedOutput()
	assert.NilError(t, err, string(out))

	result := binary.RunCLI(t, []string{"factory", "eval", "--suite", t.TempDir()}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "chunk factory eval needs a GitHub origin."), "stderr: %s", result.Stderr)
}

// readProjectConfig reads .chunk/config.json from workDir.
func readProjectConfig(t *testing.T, workDir string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "config.json"))
	assert.NilError(t, err)
	var cfg map[string]any
	assert.NilError(t, json.Unmarshal(data, &cfg))
	return cfg
}

// claudeOnSidecar answers the check for Claude Code on a sidecar, which would
// otherwise depend on whether this machine has it.
func claudeOnSidecar(sshSrv *fakes.SSHServer, installed bool) {
	sshSrv.OverrideLocally(func(command string) (string, int, bool) {
		if !strings.Contains(command, "command -v claude") {
			return "", 0, false
		}
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
		assert.NilError(t, json.Unmarshal(r.Body, &req))
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
			"setup": []map[string]any{{"name": "install", "command": "touch " + marker}},
		},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	_, err := os.Stat(marker)
	assert.NilError(t, err, "the setup step did not run")
	cfg := readProjectConfig(t, workDir)
	assert.Equal(t, cfg["validation"].(map[string]any)["sidecarImage"], "snap-1")
	// chunk factory runs Claude on sidecars made from the snapshot, so the
	// sidecar it is taken of starts from the image that has Claude.
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
	assert.NilError(t, json.Unmarshal(snaps[0].Body, &body))
	// Dated to the second, so a rerun on the same day gets a name of its own.
	assert.Assert(t, regexp.MustCompile(`^test-repo-\d{4}-\d{2}-\d{2}-\d{6}$`).MatchString(body.Data.Attributes.Name),
		"snapshot name: %s", body.Data.Attributes.Name)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "DELETE", "/api/v3/sidecar/instances/")), 1,
		"the sidecar that was snapshotted is deleted")

	// The prompts were there already, so no Claude ran to write more.
	assert.Equal(t, len(bootstrapPrompts(t, cci)), 0)
	data, err := os.ReadFile(filepath.Join(workDir, ".chunk", "reviews", "mine.md"))
	assert.NilError(t, err)
	assert.Equal(t, string(data), "review mine")
}

// The sidecar image is set up first and the review prompts after it, so each
// part's output reads as a whole.
func TestFactoryBootstrapSetsUpTheImageBeforeThePrompts(t *testing.T) {
	env, cci, sshSrv, workDir := setupReviewProjectWithSSH(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: bootstrapAnswer(t)})
	claudeOnSidecar(sshSrv, true)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "echo installing"}}},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--no-history"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Equal(t, readProjectConfig(t, workDir)["validation"].(map[string]any)["sidecarImage"], "snap-1")
	_, err := os.Stat(filepath.Join(workDir, ".chunk", "reviews", "testing.md"))
	assert.NilError(t, err)
	assert.Equal(t, len(bootstrapPrompts(t, cci)), 1)

	recorded := strings.Index(result.Stderr, "Recorded snapshot snap-1")
	prompts := strings.Index(result.Stderr, "Reading the project and writing review prompts")
	assert.Assert(t, recorded >= 0 && prompts > recorded, "the prompts came before the image: %s", result.Stderr)
	// The setup steps' output is shown as they run, on stderr: stdout is for
	// the prompts.
	assert.Assert(t, strings.Contains(result.Stderr, "installing"), "stderr: %s", result.Stderr)
	assert.Assert(t, !strings.Contains(result.Stdout, "installing"), "stdout: %s", result.Stdout)
	assert.Assert(t, strings.Contains(result.Stderr, "chunk factory build"), "stderr: %s", result.Stderr)
}

// A sidecar image that cannot be set up does not stop the review prompts, so
// a rerun has only the image left to do.
func TestFactoryBootstrapWritesThePromptsWhenTheImageFails(t *testing.T) {
	env, cci, sshSrv, workDir := setupReviewProjectWithSSH(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: bootstrapAnswer(t)})
	claudeOnSidecar(sshSrv, true)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "exit 3"}}},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--no-history"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, `Setup step "install" exited with status 3`), "stderr: %s", result.Stderr)
	_, err := os.Stat(filepath.Join(workDir, ".chunk", "reviews", "testing.md"))
	assert.NilError(t, err, "the prompts were not written")
	assert.Equal(t, len(filterByMethod(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/snapshots")), 0)
	_, hasValidation := readProjectConfig(t, workDir)["validation"]
	assert.Assert(t, !hasValidation, "no image is recorded")
}

// With --json, the prompts written after the image failed are still reported:
// a rerun skips them, so this is the only report of them.
func TestFactoryBootstrapReportsThePromptsAsJSONWhenTheImageFails(t *testing.T) {
	env, _, sshSrv, workDir := setupReviewProjectWithSSH(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: bootstrapAnswer(t)})
	claudeOnSidecar(sshSrv, true)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "exit 3"}}},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--no-history", "--json"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, `Setup step "install" exited with status 3`), "stderr: %s", result.Stderr)
	var report struct {
		Prompts      []string `json:"prompts"`
		SidecarImage string   `json:"sidecar_image"`
	}
	assert.NilError(t, json.Unmarshal([]byte(result.Stdout), &report), result.Stdout)
	assert.Assert(t, slices.Contains(report.Prompts, filepath.Join(".chunk", "reviews", "testing.md")), "prompts: %v", report.Prompts)
	assert.Equal(t, report.SidecarImage, "")
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
	env, _, workDir := setupReviewProject(t, nil, "mine")
	assert.NilError(t, os.Remove(filepath.Join(workDir, ".chunk", "config.json")))

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--skip-sidecar"}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	cfg := readProjectConfig(t, workDir)
	assert.Equal(t, cfg["orgID"], "org-aaa")
	vcs := cfg["vcs"].(map[string]any)
	assert.Equal(t, vcs["org"], "test-org")
	assert.Equal(t, vcs["repo"], "test-repo")
	// Hooks belong to chunk init.
	_, err := os.Stat(filepath.Join(workDir, ".claude", "settings.json"))
	assert.Assert(t, os.IsNotExist(err), "bootstrap wrote agent hooks")
}

// Prompts already in an --output directory are an error: the run was asked to
// write there.
func TestFactoryBootstrapRefusesAnOutputWithPrompts(t *testing.T) {
	env, cci, workDir := setupReviewProject(t, nil, "mine")

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--output", filepath.Join(workDir, ".chunk", "reviews")}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "already has review prompts"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0)
}

// An image without Claude Code is never recorded: chunk factory could not run
// on a snapshot of it.
func TestFactoryBootstrapRecordsNoImageWithoutClaude(t *testing.T) {
	env, cci, sshSrv, workDir := setupReviewProjectWithSSH(t, nil, "mine")
	claudeOnSidecar(sshSrv, false)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "true"}}},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--image", "cimg-base:2026.09"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Claude Code is not installed in the image cimg-base:2026.09"), "stderr: %s", result.Stderr)
	assert.DeepEqual(t, sidecarImages(t, cci), []string{"cimg-base:2026.09"})
	assert.Equal(t, len(filterByMethod(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/snapshots")), 0)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "DELETE", "/api/v3/sidecar/instances/")), 1,
		"the sidecar is deleted")
	_, hasValidation := readProjectConfig(t, workDir)["validation"]
	assert.Assert(t, !hasValidation, "no image is recorded")
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
	assert.NilError(t, os.MkdirAll(stateDir, 0o755))
	statePath := filepath.Join(stateDir, sidecar.StateFileName("", gitCurrentBranch(t, workDir)))
	state := []byte(`{"sidecar_ids":["sidecar-mine"],"workspace":"/home/user/mine"}`)
	assert.NilError(t, os.WriteFile(statePath, state, 0o644))

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

// An image without Claude Code would fail the review prompts the same way, on
// another sidecar, so they are not written either.
func TestFactoryBootstrapWritesNoPromptsWithoutClaude(t *testing.T) {
	env, cci, sshSrv, workDir := setupReviewProjectWithSSH(t, &fakes.ExecResponse{CommandID: "cmd-1", Stdout: bootstrapAnswer(t)})
	claudeOnSidecar(sshSrv, false)
	writeChunkConfig(t, workDir, map[string]interface{}{
		"environment": map[string]any{"stack": "go", "setup": []map[string]any{{"name": "install", "command": "true"}}},
	})

	result := binary.RunCLI(t, []string{"factory", "bootstrap", "--no-history", "--image", "cimg-base:2026.09"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Claude Code is not installed in the image cimg-base:2026.09"), "stderr: %s", result.Stderr)
	assert.Assert(t, !strings.Contains(result.Stderr, "Writing the review prompts anyway"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(bootstrapPrompts(t, cci)), 0)
	files, err := filepath.Glob(filepath.Join(workDir, ".chunk", "reviews", "*"))
	assert.NilError(t, err)
	assert.Equal(t, len(files), 0, "prompts were written: %v", files)
}

// sidecarImageProject is a project with review prompts and a saved
// environment, so a bootstrap run only sets up the sidecar image.
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
		assert.NilError(t, json.Unmarshal(r.Body, &body))
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
	var report struct {
		Prompts      []string `json:"prompts"`
		SidecarImage string   `json:"sidecar_image"`
	}
	assert.NilError(t, json.Unmarshal([]byte(result.Stdout), &report), result.Stdout)
	assert.Equal(t, report.SidecarImage, "snap-1")
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

// chunk factory build runs Claude on every change, so a missing Claude
// credential is found out before a sidecar image is set up for it, even when
// the review prompts need no Claude.
func TestFactoryBootstrapNeedsAClaudeCredentialWhenThePromptsExist(t *testing.T) {
	env, cci, workDir := sidecarImageProject(t)
	env.AnthropicKey = ""

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "No Claude credential found"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0)
}

// A prompts directory that cannot be read is reported, not taken for one that
// has prompts and skipped.
func TestFactoryBootstrapReportsAnUnreadablePromptsDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads the directory whatever its mode")
	}
	env, cci, workDir := setupReviewProject(t, nil, "mine")
	reviews := filepath.Join(workDir, ".chunk", "reviews")
	assert.NilError(t, os.Chmod(reviews, 0o000))
	t.Cleanup(func() { _ = os.Chmod(reviews, 0o755) })

	result := binary.RunCLI(t, []string{"factory", "bootstrap"}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	assert.Assert(t, strings.Contains(result.Stderr, "Could not read .chunk/reviews"), "stderr: %s", result.Stderr)
	assert.Equal(t, len(filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")), 0)
}
