package cmd

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
)

// projectWithCommand writes a project whose only validate command prints a
// marker, so a run can be traced back to the repo it actually resolved.
func projectWithCommand(t *testing.T, marker string) string {
	t.Helper()
	dir := t.TempDir()
	assert.NilError(t, config.SaveProjectConfig(dir, &config.ProjectConfig{
		Commands: []config.Command{{Name: "test", Run: "echo " + marker}},
	}))
	return dir
}

// The daemon has no working directory worth trusting. One daemon serves every
// repo on the machine — the socket is user-global — and it was launched with
// whatever cwd the developer happened to be in, which is one arbitrary repo out
// of all of them. A run that resolved the project from that cwd would validate
// the wrong repo and hand back its exit code as this project's answer; on the
// async path the staleness check then compares the requested project against
// itself, confirms nothing moved, and reports a pass for code nobody ran.
//
// So the runner validates the project it is given, with the process sitting in a
// different directory entirely.
func TestValidateRunnerValidatesTheRequestedProjectNotItsCwd(t *testing.T) {
	isolateConfig(t)
	requested := projectWithCommand(t, "ran-in-requested-project")
	// Where the daemon happens to be running: a different project, with its own
	// config, so resolving from cwd would succeed at validating the wrong repo
	// rather than merely fail to find anything.
	t.Chdir(projectWithCommand(t, "ran-in-daemon-cwd"))

	var stdout, stderr bytes.Buffer
	code := makeValidateRunner()(context.Background(), requested,
		[]string{"validate", "--local"}, os.Environ(), &stdout, &stderr)

	assert.Equal(t, code, 0)
	combined := stdout.String() + stderr.String()
	assert.Assert(t, strings.Contains(combined, "ran-in-requested-project"),
		"the requested project was not the one validated, got: %q", combined)
	assert.Assert(t, !strings.Contains(combined, "ran-in-daemon-cwd"),
		"the daemon's own cwd was validated instead of the requested project, got: %q", combined)
}

// An empty root is what a client too old to send one produces. There is nothing
// better to fall back on than the cwd, so the run proceeds that way rather than
// failing — but no --project is invented, since a wrong one would be worse.
func TestValidateRunnerFallsBackToCwdWithoutAProjectRoot(t *testing.T) {
	isolateConfig(t)
	t.Chdir(projectWithCommand(t, "ran-in-daemon-cwd"))

	var stdout, stderr bytes.Buffer
	code := makeValidateRunner()(context.Background(), "",
		[]string{"validate", "--local"}, os.Environ(), &stdout, &stderr)

	assert.Equal(t, code, 0)
	combined := stdout.String() + stderr.String()
	assert.Assert(t, strings.Contains(combined, "ran-in-daemon-cwd"),
		"a run with no project root should fall back to the cwd, got: %q", combined)
}

// The caller's own --project is what the client resolved the request's root
// from, so the two agree — but the appended one goes last and must win outright
// rather than leaving cobra to pick between two values.
func TestValidateRunnerOverridesACallerSuppliedProject(t *testing.T) {
	isolateConfig(t)
	requested := projectWithCommand(t, "ran-in-requested-project")
	stale := projectWithCommand(t, "ran-in-stale-project")
	t.Chdir(t.TempDir())

	var stdout, stderr bytes.Buffer
	code := makeValidateRunner()(context.Background(), requested,
		[]string{"validate", "--local", "--project", stale}, os.Environ(), &stdout, &stderr)

	assert.Equal(t, code, 0)
	combined := stdout.String() + stderr.String()
	assert.Assert(t, strings.Contains(combined, "ran-in-requested-project"),
		"the appended project root did not win, got: %q", combined)
}
