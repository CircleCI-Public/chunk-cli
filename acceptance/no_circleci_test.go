package acceptance

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
)

// By default chunk never reaches CircleCI, even on a machine with a CircleCI
// token: this runs the real binary against a CircleCI that counts every request.
func TestDefaultConfigMakesNoCircleCIRequests(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		t.Logf("unexpected CircleCI request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	assert.NilError(t, os.MkdirAll(filepath.Join(workDir, ".chunk"), 0o755))
	cfg := `{"commands":[{"name":"lint","run":"echo ran-locally","remote":true}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(workDir, ".chunk", "config.json"), []byte(cfg), 0o644))

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL // and env.CircleToken is set: a token is present

	t.Run("validate", func(t *testing.T) {
		result := binary.RunCLI(t, []string{"validate", "--no-daemon"}, env, workDir)
		assert.Equal(t, result.ExitCode, 0, "stdout: %s\nstderr: %s", result.Stdout, result.Stderr)
		assert.Assert(t, strings.Contains(result.Stdout+result.Stderr, "ran-locally"), result.Stdout+result.Stderr)
	})

	t.Run("validate --remote asks for the opt-in", func(t *testing.T) {
		result := binary.RunCLI(t, []string{"validate", "--no-daemon", "--remote"}, env, workDir)
		assert.Assert(t, result.ExitCode != 0)
		assert.Assert(t, strings.Contains(result.Stderr, "circleci backend"), result.Stderr)
	})

	t.Run("factory build", func(t *testing.T) {
		// Docker is the default backend; with no image configured it stops
		// there rather than falling back to CircleCI.
		result := binary.RunCLI(t, []string{"factory", "build", "--no-publish", "add a flag"}, env, workDir)
		assert.Assert(t, result.ExitCode != 0)
		assert.Assert(t, !strings.Contains(strings.ToLower(result.Stderr), "circleci"), result.Stderr)
	})

	assert.Equal(t, requests.Load(), int32(0), "CircleCI was contacted")
}
