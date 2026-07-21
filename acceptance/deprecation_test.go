package acceptance

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

// TestDeprecationWarning_SunsetInOutput checks that a Deprecation + Sunset
// response header produces a styled warning on stderr while the command still
// succeeds and returns normal output.
func TestDeprecationWarning_SunsetInOutput(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.Sidecars = []fakes.Sidecar{
		{ID: "sb-111", Name: "dev-sidecar", OrgID: "org-aaa"},
	}
	cci.ExtraHeaders = http.Header{
		"Deprecation": []string{"true"},
		"Sunset":      []string{"Sat, 01 Jan 2028 00:00:00 GMT"},
	}
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{"sidecar", "list", "--org-id", "org-aaa"}, env, env.HomeDir)

	// Command succeeds — endpoint still works during the sunset window.
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stdout, "dev-sidecar"),
		"expected sidecar in stdout, got: %s", result.Stdout)

	// Warning appears on stderr with days-remaining count.
	assert.Assert(t, strings.Contains(result.Stderr, "deprecated"),
		"expected deprecation warning on stderr, got: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "days"),
		"expected days-remaining in warning, got: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "upgrade"),
		"expected upgrade hint in warning, got: %s", result.Stderr)
}

// TestGone_410ProducesUpgradeError checks that a 410 response causes the CLI
// to exit non-zero with a clear "out of date" error and upgrade suggestion on
// stderr rather than a generic API error.
func TestGone_410ProducesUpgradeError(t *testing.T) {
	cci := fakes.NewFakeCircleCI()
	cci.ListStatusCode = http.StatusGone
	srv := httptest.NewServer(cci)
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{"sidecar", "list", "--org-id", "org-aaa"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit on 410")
	assert.Assert(t, strings.Contains(result.Stderr, "out of date"),
		"expected 'out of date' in stderr, got: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "upgrade"),
		"expected upgrade suggestion in stderr, got: %s", result.Stderr)
}

// TestGone_410WithServerMessageInOutput checks that a server-provided message
// in the 410 body is surfaced in the CLI error output.
func TestGone_410WithServerMessageInOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Circle-Token") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"error":"upgrade chunk CLI to v2.x or later to use this API"}`))
	}))
	defer srv.Close()

	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL

	result := binary.RunCLI(t, []string{"sidecar", "list", "--org-id", "org-aaa"}, env, env.HomeDir)

	assert.Assert(t, result.ExitCode != 0, "expected non-zero exit on 410")
	assert.Assert(t, strings.Contains(result.Stderr, "out of date"),
		"expected 'out of date' in stderr, got: %s", result.Stderr)
	assert.Assert(t, strings.Contains(result.Stderr, "upgrade chunk CLI to v2.x"),
		"expected server message in stderr, got: %s", result.Stderr)
}
