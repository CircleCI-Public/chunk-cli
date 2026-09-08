package acceptance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/binary"
	testenv "github.com/CircleCI-Public/chunk-cli/internal/testing/env"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/gitrepo"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/recorder"
)

// sessionEnv is a project configured to validate remotely, plus the fake API it
// talks to. Two runs against one sessionEnv stand in for two agent sessions open
// in the same working tree.
type sessionEnv struct {
	cci          *fakes.FakeCircleCI
	env          *testenv.TestEnv
	workDir      string
	identityFile string
}

func newSessionEnv(t *testing.T) *sessionEnv {
	t.Helper()
	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = "127.0.0.1" // no SSH server: the run fails after the sidecar is resolved
	srv := httptest.NewServer(cci)
	t.Cleanup(srv.Close)

	workDir := gitrepo.SetupGitRepo(t, "test-org", "test-repo")
	chunkDir := filepath.Join(workDir, ".chunk")
	assert.NilError(t, os.MkdirAll(chunkDir, 0o755))
	cfg := `{"commands":[{"name":"test","run":"echo test","remote":true}]}`
	assert.NilError(t, os.WriteFile(filepath.Join(chunkDir, "config.json"), []byte(cfg), 0o644))

	sshDir := filepath.Join(t.TempDir(), ".ssh")
	assert.NilError(t, os.MkdirAll(sshDir, 0o700))
	identityFile := filepath.Join(sshDir, "chunk_ai")
	assert.NilError(t, generateTestSSHKey(t, identityFile))

	// One HOME across both runs, so they share the state directory the way two
	// sessions on one machine do.
	env := testenv.NewTestEnv(t)
	env.CircleCIURL = srv.URL
	env.Extra["CIRCLECI_ORG_ID"] = "org-aaa"

	return &sessionEnv{cci: cci, env: env, workDir: workDir, identityFile: identityFile}
}

// validateAs runs `chunk validate` as the given agent session, identified the way
// Claude Code identifies one to every command it runs.
func (e *sessionEnv) validateAs(t *testing.T, sessionID string) {
	t.Helper()
	e.env.Extra[session.EnvClaudeSessionID] = sessionID
	result := binary.RunCLI(t, []string{"validate", "--identity-file", e.identityFile}, e.env, e.workDir)
	assert.Assert(t, result.ExitCode != 0, "expected failure because no SSH server is running; stderr: %s", result.Stderr)
}

// createRequests returns the create-sidecar calls made so far. The path is
// filtered by method because the reaper's listing shares it.
func (e *sessionEnv) createRequests(t *testing.T) []recorder.RecordedRequest {
	t.Helper()
	var creates []recorder.RecordedRequest
	for _, r := range filterByPath(e.cci.Recorder.AllRequests(), "/api/v3/sidecar/instances") {
		if r.Method == http.MethodPost {
			creates = append(creates, r)
		}
	}
	return creates
}

func (e *sessionEnv) createdSidecars(t *testing.T) int {
	t.Helper()
	return len(e.createRequests(t))
}

// TestParallelSessionsGetSeparateSidecars is the isolation guarantee: two agent
// sessions open in one working tree each get their own sidecar, instead of both
// syncing into one remote workspace and resetting it under each other's tests.
func TestParallelSessionsGetSeparateSidecars(t *testing.T) {
	e := newSessionEnv(t)

	e.validateAs(t, "session-one")
	assert.Equal(t, e.createdSidecars(t), 1, "first session must create a sidecar")

	e.validateAs(t, "session-two")
	assert.Equal(t, e.createdSidecars(t), 2,
		"a second session must not take over the sidecar the first one is using")
}

// TestSameSessionReusesItsSidecar is the other half of the trade: isolation must
// not cost a fresh sidecar on every run, or a session would leak one per turn.
func TestSameSessionReusesItsSidecar(t *testing.T) {
	e := newSessionEnv(t)

	e.validateAs(t, "session-one")
	assert.Equal(t, e.createdSidecars(t), 1)

	e.validateAs(t, "session-one")
	assert.Equal(t, e.createdSidecars(t), 1, "a session must reuse the sidecar it already has")
}

// TestSessionCreatesOwnSidecarAlways covers that a session always creates its
// own sidecar, even when an unowned one already exists for the project.
func TestSessionCreatesOwnSidecarAlways(t *testing.T) {
	e := newSessionEnv(t)

	// No session ID: this is a human running the command.
	result := binary.RunCLI(t, []string{"validate", "--identity-file", e.identityFile}, e.env, e.workDir)
	assert.Assert(t, result.ExitCode != 0, "expected failure because no SSH server is running")
	assert.Equal(t, e.createdSidecars(t), 1)

	e.validateAs(t, "session-one")
	assert.Equal(t, e.createdSidecars(t), 2, "a session must create its own sidecar, not adopt an unowned one")
}

// TestLegacyStateIsNotAdoptedWhileWarm is the regression for what happened on the
// first run after this shipped: state written before the owning session was
// recorded in the file still belongs to a session, so a second session must not
// treat it as free and sync onto the sidecar the first one is working on.
func TestLegacyStateIsNotAdoptedWhileWarm(t *testing.T) {
	e := newSessionEnv(t)

	// Exactly what an older chunk left behind: keyed to a session by file name,
	// with no owner inside the file.
	writeSidecarState(t, e.env, e.workDir, "session-one", "sb-in-use")

	e.validateAs(t, "session-two")
	assert.Equal(t, e.createdSidecars(t), 1,
		"the second session must create its own sidecar, not adopt the one in use")
}

// TestSidecarCurrentNamesTheOwningSession covers the thing that made two
// separate sidecars look like one: a sidecar keeps the name it was created with,
// so after adoption the name carries the session that created it, not the one
// using it. `sidecar current` names the owner so the two can be told apart.
func TestSidecarCurrentNamesTheOwningSession(t *testing.T) {
	e := newSessionEnv(t)
	e.env.Extra[session.EnvClaudeSessionID] = "c4cf421e-8581-45c6-ac11-b231c5368e97"

	result := binary.RunCLI(t, []string{"sidecar", "use", "sb-adopted"}, e.env, e.workDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)

	result = binary.RunCLI(t, []string{"sidecar", "current"}, e.env, e.workDir)
	assert.Equal(t, result.ExitCode, 0, "stderr: %s", result.Stderr)
	combined := result.Stdout + result.Stderr
	assert.Assert(t, strings.Contains(combined, "session c4cf421e"),
		"expected the owning session in current output, got: %s", combined)
}

// TestParallelSessionsSidecarNames pins the naming that makes two sidecars
// tellable apart in `chunk sidecar list`.
func TestParallelSessionsSidecarNames(t *testing.T) {
	e := newSessionEnv(t)

	e.validateAs(t, "session-one")
	e.validateAs(t, "session-two")

	var names []string
	for _, req := range e.createRequests(t) {
		var body struct {
			Data struct {
				Attributes struct {
					Name string `json:"name"`
				} `json:"attributes"`
			} `json:"data"`
		}
		assert.NilError(t, json.Unmarshal(req.Body, &body))
		names = append(names, body.Data.Attributes.Name)
	}
	assert.Equal(t, len(names), 2)
	assert.Assert(t, names[0] != names[1], "each session's sidecar must carry its own name, got %v", names)
}
