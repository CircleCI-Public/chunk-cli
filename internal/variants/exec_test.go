package variants

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

func discardStatus(_ iostream.Level, _ string) {}

// newSession opens a real SSH session against a fake sidecar, so the verdict
// tests below exercise the actual exec path rather than a stub of it. exitFor
// decides the result of each command the sidecar receives.
func newSession(t *testing.T, exitFor func(command string) (string, int)) (*sidecar.Session, *fakes.SSHServer) {
	t.Helper()

	keyFile, pubKey := fakes.GenerateSSHKeypair(t)
	sshSrv := fakes.NewSSHServer(t, pubKey)
	sshSrv.SetResultFn(exitFor)

	cci := fakes.NewFakeCircleCI()
	cci.AddKeyURL = sshSrv.Addr()
	api := httptest.NewServer(cci)
	t.Cleanup(api.Close)

	// OpenSession resolves known_hosts under HOME; keep it in a temp dir.
	t.Setenv(config.EnvHome, t.TempDir())

	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: api.URL})
	assert.NilError(t, err)

	session, err := sidecar.OpenSession(context.Background(), client, "sc-1", keyFile, "", false)
	assert.NilError(t, err)
	return session, sshSrv
}

func testOpts(cmds ...Command) Options {
	return Options{
		Workspace: "/home/user/repo",
		Commands:  cmds,
		StatusFn:  discardStatus,
	}
}

// runOne drives runCommands against a fake sidecar and returns the verdict along
// with the SSH server, whose Commands() is the race-free record of what actually
// reached the sidecar.
func runOne(t *testing.T, exitFor func(string) (string, int), cmds ...Command) (Result, *fakes.SSHServer) {
	t.Helper()
	session, sshSrv := newSession(t, exitFor)
	v := Variant{ID: "MUT-001", Description: "invert nil guard"}
	res := runCommands(context.Background(), session, v, Result{ID: v.ID, Description: v.Description}, testOpts(cmds...))
	return res, sshSrv
}

// TestRunCommandsFailingSuiteIsKilled is the one case that may set Killed: the
// command ran and the tests failed.
func TestRunCommandsFailingSuiteIsKilled(t *testing.T) {
	res, _ := runOne(t, func(string) (string, int) { return "--- FAIL: TestThing\n", 1 },
		Command{Name: "test", Run: "go test ./..."})

	assert.Equal(t, res.Killed, true)
	assert.Equal(t, res.Error, "")
	assert.Equal(t, res.ExitCode, 1)
	assert.Equal(t, res.Command, "test")
	assert.Assert(t, strings.Contains(res.Stdout, "FAIL"))
}

// TestRunCommandsShellFailureIsNotAKill covers the failure mode this whole
// package has to avoid. The shell could not run the command at all, which looks
// the same for every variant in a run, so recording it as a caught mutant would
// turn a snapshot missing the project's tooling into a clean bill of health.
func TestRunCommandsShellFailureIsNotAKill(t *testing.T) {
	for _, tc := range []struct {
		name string
		exit int
		want string
	}{
		{name: "command not found", exit: 127, want: "not found"},
		{name: "not executable", exit: 126, want: "not executable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res, _ := runOne(t, func(string) (string, int) { return "", tc.exit },
				Command{Name: "test", Run: "task test"})

			assert.Equal(t, res.Killed, false)
			assert.Equal(t, res.ExitCode, tc.exit)
			assert.Equal(t, res.Command, "test")
			assert.Assert(t, strings.Contains(res.Error, tc.want),
				"expected %q in error, got %q", tc.want, res.Error)
		})
	}
}

// TestRunCommandsTimeoutIsNotAKill covers a mutant that makes the suite hang.
// Nothing was proven, so it must not read as caught — and returning is what frees
// the parallel slot and lets the caller delete the billed sidecar.
func TestRunCommandsTimeoutIsNotAKill(t *testing.T) {
	release := make(chan struct{})
	// A defer, not a t.Cleanup: this must release the blocked handler before the
	// cleanups that shut the servers down, not after.
	defer close(release)

	start := time.Now()
	res, _ := runOne(t, func(string) (string, int) {
		<-release
		return "", 0
	}, Command{Name: "test", Run: "go test ./...", Timeout: 1})

	assert.Equal(t, res.Killed, false)
	assert.Equal(t, res.Command, "test")
	assert.Assert(t, strings.Contains(res.Error, "timed out"), "got error %q", res.Error)
	assert.Assert(t, time.Since(start) < 30*time.Second, "timeout was not enforced")
}

func TestRunCommandsAllPassingSurvives(t *testing.T) {
	res, _ := runOne(t, func(string) (string, int) { return "ok\n", 0 },
		Command{Name: "lint", Run: "task lint"},
		Command{Name: "test", Run: "go test ./..."})

	assert.Equal(t, res.Killed, false)
	assert.Equal(t, res.Error, "")
	assert.Equal(t, res.ExitCode, 0)
	// No command is credited with a survivor: every one of them passed.
	assert.Equal(t, res.Command, "")
}

// TestRunCommandsStopsAtFirstFailure confirms the remaining commands are skipped
// once a mutant is caught; they would only run against known-broken code.
func TestRunCommandsStopsAtFirstFailure(t *testing.T) {
	res, sshSrv := runOne(t, func(string) (string, int) { return "", 1 },
		Command{Name: "lint", Run: "task lint"},
		Command{Name: "test", Run: "go test ./..."})

	assert.Equal(t, res.Killed, true)
	assert.Equal(t, res.Command, "lint")

	seen := sshSrv.Commands()
	assert.Equal(t, len(seen), 1, "expected only the first command to run, got %v", seen)
	assert.Assert(t, strings.Contains(seen[0], "task lint"))
}

// TestCreatedAtRejectsNamesItCannotDate covers everything the sweep must refuse
// to date, and therefore must leave alone.
func TestCreatedAtRejectsNamesItCannotDate(t *testing.T) {
	for _, name := range []string{
		"happy-quickly-tesla",  // not ours
		"variant-",             // nothing after the prefix
		"variant-mut-001",      // pre-timestamp name: single dashes, no nameSep
		"variant-timeout-fix",  // ditto, and would have parsed as base 36
		"variant---mut-001",    // empty timestamp
		"variant-!!!--mut-001", // timestamp is not base 36
	} {
		_, ok := createdAt(name)
		assert.Check(t, !ok, "expected %q to be undatable", name)
	}
}

func TestCreatedAtRoundTripsSidecarName(t *testing.T) {
	born := time.Date(2026, time.August, 11, 9, 0, 0, 0, time.UTC)

	name := sidecarName(born, "MUT-001")
	got, ok := createdAt(name)
	assert.Assert(t, ok, "expected %q to be datable", name)
	assert.Equal(t, got.Unix(), born.Unix())
}

// TestSidecarNameCollapsesDashesInID is what keeps nameSep unambiguous: if a
// variant ID could produce two dashes in a row, the split that recovers the
// timestamp would land in the middle of the ID instead.
func TestSidecarNameCollapsesDashesInID(t *testing.T) {
	born := time.Date(2026, time.August, 11, 9, 0, 0, 0, time.UTC)

	name := sidecarName(born, "MUT_001//x--y")
	assert.Assert(t, strings.HasSuffix(name, nameSep+"mut-001-x-y"), "got %q", name)

	// Still datable, and the timestamp is recovered intact despite the ID's own
	// separators.
	got, ok := createdAt(name)
	assert.Assert(t, ok, "got %q", name)
	assert.Equal(t, got.Unix(), born.Unix())
}
