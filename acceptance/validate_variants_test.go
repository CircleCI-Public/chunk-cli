package acceptance

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/recorder"
	"github.com/CircleCI-Public/chunk-cli/internal/variants"
)

// writeVariantsFile writes a variants JSON file to dir and returns its path.
func writeVariantsFile(t *testing.T, dir string, vs []variants.Variant) string {
	t.Helper()
	data, err := json.Marshal(vs)
	assert.NilError(t, err)
	path := filepath.Join(dir, "variants.json")
	assert.NilError(t, os.WriteFile(path, data, 0o644))
	return path
}

// writeChunkConfig writes a minimal .chunk/config.json with one remote command.
func writeChunkConfig(t *testing.T, dir string, extra map[string]interface{}) {
	t.Helper()
	chunkDir := filepath.Join(dir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	cfg := map[string]interface{}{
		"commands": []map[string]interface{}{
			{"name": "test", "run": "go test ./...", "remote": true},
		},
	}
	for k, v := range extra {
		cfg[k] = v
	}
	data, err := json.Marshal(cfg)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), data, 0o644))
}

func filterVariantRequests(reqs []recorder.RecordedRequest, method, pathPrefix string) []recorder.RecordedRequest {
	var out []recorder.RecordedRequest
	for _, r := range reqs {
		if r.Method == method && strings.HasPrefix(r.URL.Path, pathPrefix) {
			out = append(out, r)
		}
	}
	return out
}

func TestValidateVariantsMissingFile(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{
		"validate", "variants", "/nonexistent/variants.json",
		"--org-id", "org-aaa",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
}

func TestValidateVariantsMissingArg(t *testing.T) {
	env := testenv.NewTestEnv(t)

	result := binary.RunCLI(t, []string{"validate", "variants"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
}

func TestValidateVariantsMalformedJSON(t *testing.T) {
	env := testenv.NewTestEnv(t)
	path := filepath.Join(env.HomeDir, "bad.json")
	assert.NilError(t, os.WriteFile(path, []byte("not json"), 0o644))

	result := binary.RunCLI(t, []string{
		"validate", "variants", path,
		"--org-id", "org-aaa",
	}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
}

func TestValidateVariantsEmptyArray(t *testing.T) {
	env := testenv.NewTestEnv(t)
	path := filepath.Join(env.HomeDir, "empty.json")
	assert.NilError(t, os.WriteFile(path, []byte("[]"), 0o644))

	result := binary.RunCLI(t, []string{
		"validate", "variants", path,
		"--org-id", "org-aaa",
	}, env, env.HomeDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "No variants"),
		"expected 'No variants' in stderr, got: %s", result.Stderr)
}

func TestValidateVariantsMissingToken(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.CircleToken = ""

	workDir := env.HomeDir
	writeChunkConfig(t, workDir, nil)
	path := writeVariantsFile(t, workDir, []variants.Variant{
		{ID: "MUT-001", Description: "test"},
	})

	result := binary.RunCLI(t, []string{
		"validate", "variants", path,
		"--org-id", "org-aaa",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
}

func TestValidateVariantsNoRemoteCommands(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	workDir := env.HomeDir
	// Write config with only local (non-remote) commands.
	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	cfg := `{"commands":[{"name":"test","run":"go test ./...","remote":false}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(cfg), 0o644))

	path := writeVariantsFile(t, workDir, []variants.Variant{
		{ID: "MUT-001"},
	})

	result := binary.RunCLI(t, []string{
		"validate", "variants", path,
		"--org-id", "org-aaa",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "remote"),
		"expected 'remote' in error output, got: %s", combined)
}

func TestValidateVariantsNamedCommand(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	// A git repo with an origin remote, so the remote workspace resolves to the
	// same <sidecarHome>/<repo> the rest of the CLI syncs into.
	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeChunkConfig(t, workDir, nil)
	path := writeVariantsFile(t, workDir, []variants.Variant{
		{ID: "MUT-001"},
	})

	// --name references a command that is not marked remote; still accepted since
	// --name bypasses the remote filter.
	result := binary.RunCLI(t, []string{
		"validate", "variants", path,
		"--org-id", "org-aaa",
		"--name", "test",
	}, env, workDir)

	// Command exits 0; individual errors are in JSON results (Sync fails — no SSH).
	assert.Equal(t, result.ExitCode, 0, "stderr: %s\nstdout: %s", result.Stderr, result.Stdout)
}

func TestValidateVariantsUnknownNamedCommand(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	workDir := env.HomeDir
	writeChunkConfig(t, workDir, nil)
	path := writeVariantsFile(t, workDir, []variants.Variant{
		{ID: "MUT-001"},
	})

	result := binary.RunCLI(t, []string{
		"validate", "variants", path,
		"--org-id", "org-aaa",
		"--name", "nonexistent",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
}

func TestValidateVariantsCreatesAndDeletesSidecars(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeChunkConfig(t, workDir, nil)
	path := writeVariantsFile(t, workDir, []variants.Variant{
		{ID: "MUT-001"},
		{ID: "MUT-002"},
	})

	result := binary.RunCLI(t, []string{
		"validate", "variants", path,
		"--org-id", "org-aaa",
		"--image", "snap-abc",
	}, env, workDir)

	// Command exits 0; Sync fails (no git repo / no SSH) but that's captured in Result.Error.
	assert.Equal(t, result.ExitCode, 0, "stderr: %s\nstdout: %s", result.Stderr, result.Stdout)

	reqs := cci.Recorder.AllRequests()
	creates := filterVariantRequests(reqs, "POST", "/api/v3/sidecar/instances")
	deletes := filterVariantRequests(reqs, "DELETE", "/api/v3/sidecar/instances/")
	assert.Check(t, len(creates) >= 2, "expected 2 create requests, got %d", len(creates))
	assert.Check(t, len(deletes) >= 2, "expected 2 delete requests, got %d", len(deletes))

	// Output should be valid JSON array.
	var results []map[string]interface{}
	assert.NilError(t, json.Unmarshal([]byte(result.Stdout), &results),
		"expected valid JSON array in stdout, got: %s", result.Stdout)
	assert.Equal(t, len(results), 2)
}

func TestValidateVariantsImageFromConfig(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	workDir := gitrepo.SetupGitRepo(t, "my-org", "my-repo")
	writeChunkConfig(t, workDir, map[string]interface{}{
		"validation": map[string]interface{}{
			"sidecarImage": "snap-from-config",
		},
	})
	path := writeVariantsFile(t, workDir, []variants.Variant{
		{ID: "MUT-001"},
	})

	binary.RunCLI(t, []string{
		"validate", "variants", path,
		"--org-id", "org-aaa",
		// No --image flag; should use config value.
	}, env, workDir)

	reqs := cci.Recorder.AllRequests()
	creates := filterVariantRequests(reqs, "POST", "/api/v3/sidecar/instances")
	assert.Assert(t, len(creates) >= 1, "expected at least 1 create request")

	// CreateSidecar sends the V3 envelope, so the image lives under
	// data.attributes rather than at the top level. Asserting on the request body
	// rather than cci.Sidecars because the run deletes its sidecar on the way out,
	// leaving nothing in the fake's list to inspect.
	var body struct {
		Data struct {
			Attributes struct {
				Name  string `json:"name"`
				Image string `json:"image"`
			} `json:"attributes"`
		} `json:"data"`
	}
	assert.NilError(t, json.Unmarshal(creates[0].Body, &body))
	assert.Equal(t, body.Data.Attributes.Image, "snap-from-config")
	assert.Assert(t, strings.HasPrefix(body.Data.Attributes.Name, "variant-"),
		"expected a variant-prefixed sidecar name, got %q", body.Data.Attributes.Name)
}

// TestValidateVariantsUnresolvableWorkspace pins the pre-flight failure. With no
// origin remote and no active sidecar there is no way to know where the snapshot
// prepared its dependencies, and guessing a path means every command fails for
// environmental reasons and every mutant reads as caught. Failing before any
// sidecar is booted is the only safe answer.
func TestValidateVariantsUnresolvableWorkspace(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	// Not a git repo, so no org/repo can be detected.
	workDir := t.TempDir()
	writeChunkConfig(t, workDir, nil)
	path := writeVariantsFile(t, workDir, []variants.Variant{
		{ID: "MUT-001"},
	})

	result := binary.RunCLI(t, []string{
		"validate", "variants", path,
		"--org-id", "org-aaa",
		"--image", "snap-abc",
	}, env, workDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit code")
	creates := filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")
	assert.Equal(t, len(creates), 0, "no sidecar may be booted when the workspace is unknown")
}

// TestValidateVariantsWorkdirFlagOverridesDetection confirms --workdir still
// works without a git remote, which is the escape hatch the error above suggests.
func TestValidateVariantsWorkdirFlagOverridesDetection(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	workDir := t.TempDir()
	writeChunkConfig(t, workDir, nil)
	path := writeVariantsFile(t, workDir, []variants.Variant{
		{ID: "MUT-001"},
	})

	result := binary.RunCLI(t, []string{
		"validate", "variants", path,
		"--org-id", "org-aaa",
		"--image", "snap-abc",
		"--workdir", "/home/user/my-repo",
	}, env, workDir)

	assert.Equal(t, result.ExitCode, 0, "stderr: %s\nstdout: %s", result.Stderr, result.Stdout)
	creates := filterVariantRequests(cci.Recorder.AllRequests(), "POST", "/api/v3/sidecar/instances")
	assert.Assert(t, len(creates) >= 1, "expected a sidecar to be created")
}
