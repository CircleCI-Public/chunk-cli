package acceptance

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/envbuilder"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
)

//go:embed repos.json
var reposJSON []byte

// repoEntry holds a name, URL, optional pinned ref, and optional cached env
// spec for a test case.
type repoEntry struct {
	Name string          `json:"name"`
	URL  string          `json:"url"`
	Ref  string          `json:"ref,omitempty"`
	Env  json.RawMessage `json:"env,omitempty"`
}

// knownRepos is loaded from repos.json at init time.
var knownRepos = func() []repoEntry {
	var repos []repoEntry
	if err := json.Unmarshal(reposJSON, &repos); err != nil {
		panic("acceptance: failed to parse repos.json: " + err.Error())
	}
	return repos
}()

// resolveRepos parses CHUNK_SANDBOX_REPOS (comma-separated nicknames or Git
// URLs). If unset, all knownRepos are returned.
func resolveRepos(t *testing.T) []repoEntry {
	t.Helper()
	raw := os.Getenv("CHUNK_SANDBOX_REPOS")
	if raw == "" {
		return knownRepos
	}

	var entries []repoEntry
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if strings.HasPrefix(item, "https://") || strings.HasPrefix(item, "http://") || strings.HasPrefix(item, "git@") {
			// Raw URL: derive name from the last path component, stripping .git.
			name := strings.TrimSuffix(item[strings.LastIndex(item, "/")+1:], ".git")
			if name == "" {
				name = item
			}
			entries = append(entries, repoEntry{Name: name, URL: item})
		} else {
			found := false
			for _, r := range knownRepos {
				if r.Name == item {
					entries = append(entries, r)
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("unknown repo nickname %q — use a full Git URL or one of the known nicknames", item)
			}
		}
	}
	return entries
}

// TestSandboxesBuildEndToEnd clones real open-source repos, runs
// `chunk sandboxes build` to generate a Dockerfile.test, builds the image,
// and runs the tests inside the container.
//
// Enable with: CHUNK_ENV_BUILDER_ACCEPTANCE=1
// Requires: git and docker accessible without sudo.
//
// To run a specific subset, set CHUNK_SANDBOX_REPOS to a comma-separated list
// of known nicknames (e.g. "flask,serde") or full Git URLs
// (e.g. "https://github.com/owner/repo.git"), or a mix of both.
func TestSandboxesBuildEndToEnd(t *testing.T) {
	if os.Getenv("CHUNK_ENV_BUILDER_ACCEPTANCE") == "" {
		t.Skip("set CHUNK_ENV_BUILDER_ACCEPTANCE=1 to run")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not found in PATH")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}

	repos := resolveRepos(t)

	cacheDir := os.Getenv("CHUNK_SANDBOX_CACHE_DIR")

	for _, repo := range repos {
		t.Run(repo.Name, func(t *testing.T) {
			var cloneDir string
			if cacheDir != "" {
				cloneDir = cacheDir + "/chunk-sandbox-" + repo.Name
			} else {
				cloneDir = t.TempDir()
			}
			e2eCloneRepo(t, repo.URL, repo.Ref, cloneDir)

			t.Log("Running chunk sandbox env...")
			envJSON, envStderr, exitCode := e2eRunEnv(t, cloneDir)
			assert.Equal(t, exitCode, 0,
				"chunk sandbox env failed\nstdout: %s\nstderr: %s", envJSON, envStderr)
			t.Log(envStderr)
			t.Log(envJSON)

			// Always write the env spec so the harness can compare it.
			assert.NilError(t,
				os.WriteFile(cloneDir+"/env.json", []byte(envJSON), 0o644),
				"write env.json",
			)

			// Env-only mode: stop here so the harness can decide whether to build.
			if os.Getenv("CHUNK_SANDBOX_ENV_ONLY") == "1" {
				return
			}

			tag := "chunk-sandbox-" + repo.Name + "-test"

			t.Log("Building Docker image...")
			buildOutput, buildExitCode := e2eRunBuild(t, cloneDir, tag, envJSON)
			assert.Equal(t, buildExitCode, 0,
				"chunk sandbox build failed\n%s", lastLines(buildOutput, 30))

			_, err := os.Stat(cloneDir + "/Dockerfile.test")
			assert.NilError(t, err, "Dockerfile.test not created")

			dfContent, err := os.ReadFile(cloneDir + "/Dockerfile.test")
			assert.NilError(t, err, "read Dockerfile.test")
			var parsedEnv envbuilder.Environment
			if jsonErr := json.Unmarshal([]byte(envJSON), &parsedEnv); jsonErr == nil {
				if envbuilder.NeedsNPMRC(parsedEnv.Stack) {
					assert.Assert(t, strings.Contains(string(dfContent), "--mount=type=secret,id=npmrc"),
						"Dockerfile.test missing npmrc secret mount for stack %q", parsedEnv.Stack)
				}
				if envbuilder.NeedsNetRC(parsedEnv.Stack) {
					assert.Assert(t, strings.Contains(string(dfContent), "--mount=type=secret,id=netrc"),
						"Dockerfile.test missing netrc secret mount for stack %q", parsedEnv.Stack)
				}
			}

			t.Log("Running tests in container...")
			testsOK, testOutput := e2eDockerRun(t, tag)
			assert.Assert(t, testsOK,
				"tests failed in container\n%s", lastLines(testOutput, 30))
		})
	}
}

func e2eCloneRepo(t *testing.T, url, ref, dir string) {
	t.Helper()
	if _, err := os.Stat(dir + "/.git"); err == nil {
		t.Logf("using existing clone at %s", dir)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	// A 40-char hex string is a SHA: fetch that single commit directly.
	// Otherwise treat ref as a branch/tag name for a normal shallow clone.
	switch {
	case len(ref) == 40 && isHex(ref):
		t.Logf("cloning %s at %s into %s...", url, ref[:8], dir)
		git := func(args ...string) {
			out, err := exec.CommandContext(ctx, "git", args...).CombinedOutput()
			assert.NilError(t, err, "git %v failed: %s", args, out)
		}
		git("init", dir)
		git("-C", dir, "remote", "add", "origin", url)
		git("-C", dir, "fetch", "--depth=1", "origin", ref)
		git("-C", dir, "checkout", "FETCH_HEAD")
	case ref != "":
		t.Logf("cloning %s at %s into %s...", url, ref, dir)
		out, err := exec.CommandContext(ctx, "git", "clone", "--depth=1", "--branch", ref, url, dir).CombinedOutput()
		assert.NilError(t, err, "git clone failed: %s", out)
	default:
		t.Logf("cloning %s into %s...", url, dir)
		out, err := exec.CommandContext(ctx, "git", "clone", "--depth=1", url, dir).CombinedOutput()
		assert.NilError(t, err, "git clone failed: %s", out)
	}
}

func isHex(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}

func e2eRunEnv(t *testing.T, dir string) (stdout, stderr string, exitCode int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary.Path(), "sandbox", "env", "--dir", dir)
	cmd.Env = []string{
		fmt.Sprintf("PATH=%s", os.Getenv("PATH")),
		fmt.Sprintf("HOME=%s", os.Getenv("HOME")),
		"NO_COLOR=1",
	}

	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return outBuf.String(), errBuf.String(), exitCode
}

func e2eRunBuild(t *testing.T, dir, tag, envJSON string) (output string, exitCode int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, binary.Path(), "sandbox", "build", "--dir", dir, "--tag", tag)
	cmd.Env = []string{
		fmt.Sprintf("PATH=%s", os.Getenv("PATH")),
		fmt.Sprintf("HOME=%s", os.Getenv("HOME")),
		"NO_COLOR=1",
	}
	cmd.Stdin = strings.NewReader(envJSON)
	var outBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &outBuf

	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	return outBuf.String(), exitCode
}

func e2eDockerRun(t *testing.T, tag string) (bool, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	args := []string{"run", "--rm", tag}
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	return err == nil, string(out)
}

// lastLines returns the last n lines of s, for trimming long failure output.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	omitted := len(lines) - n
	return fmt.Sprintf("... (%d lines omitted) ...\n", omitted) + strings.Join(lines[len(lines)-n:], "\n")
}
