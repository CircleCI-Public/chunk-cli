package watchd

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
	"github.com/CircleCI-Public/chunk-cli/internal/sidecar"
)

// newTestDaemon builds a daemon the way RunDaemon does, minus the socket and
// the poll loop, for tests that drive poll and snapshot by hand.
//
// It exists because poll reaches through every collaborator the real daemon is
// assembled with — the output store for each project's commands, the sampler to
// annotate its sidecars — so a daemon put together field by field panics on
// whichever one the last person left out. Build it here and a new collaborator
// is one edit, not one per test.
func newTestDaemon() *daemon {
	return &daemon{
		projects: make(map[string]*projectState),
		out:      newOutputStore(context.Background()),
		// No client: these tests never attach a dashboard, so nothing is sampled
		// and the sampler only has to be non-nil to annotate.
		res: newResourceSampler(nil),
	}
}

// TestDaemonRoundTrip starts the daemon in-process, waits for it to accept
// connections, issues a FetchSnapshot, then cancels the context and verifies
// clean shutdown. Uses CHUNK_WATCHD_DIR to avoid touching ~/.chunk/watchd.
func TestDaemonRoundTrip(t *testing.T) {
	t.Setenv("CHUNK_WATCHD_DIR", t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() { errCh <- RunDaemon(ctx, nil, "", nil) }()

	sockPath, err := SocketPath()
	assert.NilError(t, err)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reachable, _ := ping(sockPath); reachable {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	reachable, build := ping(sockPath)
	assert.Assert(t, reachable, "daemon did not become reachable within 5s")
	// The daemon runs in this process, so it reports this build.
	assert.Equal(t, build, BuildID())

	_, err = FetchSnapshot(nil)
	assert.NilError(t, err)

	cancel()

	select {
	case err := <-errCh:
		assert.NilError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not shut down within 5s after context cancel")
	}
}

// Results recorded while no daemon was running are the reason the log is on
// disk at all. A daemon starting afterwards has only the breadcrumb to go on —
// no sidecar state, no connection to the run that wrote it — and must replay
// what is already in the log rather than only what arrives after it starts.
func TestPollReplaysResultsRecordedWhileTheDaemonWasDown(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	root := t.TempDir()
	dataDir, err := config.ProjectDataDir(root)
	assert.NilError(t, err)
	assert.NilError(t, sidecar.RegisterProjectRoot(dataDir, root))
	// Registration canonicalises, so this is the spelling the daemon reports.
	canonicalRoot, err := filepath.EvalSymlinks(root)
	assert.NilError(t, err)

	log, err := eventlog.Open(dataDir)
	assert.NilError(t, err)
	rec := log.Recorder(nil, eventlog.OpValidate, "", "", "main")
	rec.Status(iostream.LevelInfo, "$ echo hi")
	rec.Final(iostream.LevelDone, "1/1 passed  113ms", 1, 1)

	d := newTestDaemon()
	d.poll()

	snap := d.snapshot(nil)
	assert.Equal(t, len(snap.Projects), 1, "the registered project was not discovered")
	p := snap.Projects[0]
	assert.Equal(t, p.Root, canonicalRoot)
	assert.Equal(t, len(p.Events), 2, "events predating the daemon were dropped")
	passed, total, ok := p.Events[1].Outcome()
	assert.Assert(t, ok, "the closing event did not survive the round trip")
	assert.Equal(t, passed, 1)
	assert.Equal(t, total, 1)

	// The run had no sidecar, so it is the synthesised local row in the dashboard
	// that carries it — there must be no sidecar state invented for it here.
	assert.Equal(t, len(p.Sidecars), 0)
}

func TestBuildID_distinguishesBuildsTheVersionCannot(t *testing.T) {
	mod := time.Unix(1_700_000_000, 0)

	// Every local build reports the same development version, so the version
	// alone would call two different binaries the same daemon.
	same := buildID("dev", "/usr/local/bin/chunk", 100, mod)
	assert.Equal(t, same, buildID("dev", "/usr/local/bin/chunk", 100, mod))

	// A rebuild in place: same path, new size and timestamp.
	assert.Assert(t, buildID("dev", "/usr/local/bin/chunk", 101, mod) != same)
	assert.Assert(t, buildID("dev", "/usr/local/bin/chunk", 100, mod.Add(time.Second)) != same)

	// A dev build alongside an installed one.
	assert.Assert(t, buildID("dev", "/repo/dist/chunk", 100, mod) != same)

	// A tagged release differs from a dev build of the same binary.
	assert.Assert(t, buildID("v1.2.3", "/usr/local/bin/chunk", 100, mod) != same)
}

func TestBuildID_fallsBackToTheVersionWithoutAnExecutable(t *testing.T) {
	assert.Equal(t, buildID("v1.2.3", "", 0, time.Time{}), "v1.2.3")
}

// A daemon that predates the identity answers /ping with an empty body, so the
// client must read that as a mismatch and replace it rather than trust it.
func TestPing_emptyBuildIsAMismatch(t *testing.T) {
	assert.Assert(t, BuildID() != "", "BuildID must never be empty, or a stale daemon would look current")
}

// EnsureLaunched must leave a reachable daemon alone whatever build it reports,
// so a failed poll in one dashboard cannot restart the daemon another is using.
func TestEnsureLaunched_leavesAReachableDaemonAlone(t *testing.T) {
	// Not t.TempDir(): it embeds the test name, and a unix socket path is capped
	// at 104 bytes on darwin, so a descriptive name here silently breaks listen.
	dir, err := os.MkdirTemp("", "wd")
	assert.NilError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("CHUNK_WATCHD_DIR", dir)
	// The daemon polls every known project before it serves, and every project
	// costs a git call. Pointed at the developer's real data directory that first
	// poll can outlast the wait below, so keep it hermetic.
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- RunDaemon(ctx, nil, "", nil) }()

	sockPath, err := SocketPath()
	assert.NilError(t, err)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if reachable, _ := ping(sockPath); reachable {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	reachable, _ := ping(sockPath)
	assert.Assert(t, reachable, "daemon did not become reachable within 5s")

	pidPath, err := PIDPath()
	assert.NilError(t, err)
	_, before, err := IsRunning(pidPath)
	assert.NilError(t, err)

	// Args that could not possibly start anything: if EnsureLaunched tried to
	// relaunch, it would fail rather than silently succeed.
	assert.NilError(t, EnsureLaunched([]string{"definitely", "not", "a", "command"}))

	_, after, err := IsRunning(pidPath)
	assert.NilError(t, err)
	assert.Equal(t, before, after, "the running daemon was replaced")

	cancel()
	select {
	case <-errCh:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not shut down")
	}
}

// A daemon resolves its CircleCI client once at startup, so a login has no
// effect on one that is already running. Stopping it is what makes the next
// launch pick the new credentials up.
func TestStopForCredentialChangeStopsARunningDaemon(t *testing.T) {
	startTestDaemon(t)

	sockPath, err := SocketPath()
	assert.NilError(t, err)
	reachable, _ := ping(sockPath)
	assert.Assert(t, reachable, "daemon should be answering before the stop")

	StopForCredentialChange()

	reachable, _ = ping(sockPath)
	assert.Check(t, !reachable, "daemon still answering after StopForCredentialChange")
}

// Best-effort: with nothing running there is nothing to stop, and a login must
// not fail because of it.
func TestStopForCredentialChangeIsANoopWithNoDaemon(t *testing.T) {
	t.Setenv("CHUNK_WATCHD_DIR", t.TempDir())
	StopForCredentialChange()
}

// The duplicate rows this guards against were found by hand: one project drawn
// twice, the two rows sharing a single event log. The daemon keys projects by
// the breadcrumb string while ProjectDataDir keys the directory by the resolved
// path, so two spellings of one root share a log but count as two projects. It
// took a symlinked path to see it, which on darwin is every temp directory and
// on linux is none — so the symlink here is built rather than assumed, and the
// spelling is flipped between polls the way a validate run and chunk watch used
// to flip it.
func TestPollListsOneProjectPerRootHoweverItIsSpelled(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	base := t.TempDir()
	target := filepath.Join(base, "project")
	assert.NilError(t, os.MkdirAll(target, 0o755))
	link := filepath.Join(base, "link")
	assert.NilError(t, os.Symlink(target, link))

	canonical, err := filepath.EvalSymlinks(target)
	assert.NilError(t, err)

	// One data directory, reached through either spelling.
	dataDir, err := config.ProjectDataDir(link)
	assert.NilError(t, err)
	viaTarget, err := config.ProjectDataDir(target)
	assert.NilError(t, err)
	assert.Equal(t, dataDir, viaTarget, "both spellings must share one data directory")

	log, err := eventlog.Open(dataDir)
	assert.NilError(t, err)
	log.Recorder(nil, eventlog.OpValidate, "", "", "").Final(iostream.LevelDone, "1/1 passed", 1, 1)

	crumb := filepath.Join(dataDir, "project-root")
	d := newTestDaemon()

	// Registered through the symlink, then rewritten as the resolved path — the
	// two writers' spellings, in the order a developer hits them.
	assert.NilError(t, sidecar.RegisterProjectRoot(dataDir, link))
	d.poll()
	assert.NilError(t, os.WriteFile(crumb, []byte(target), 0o644))
	d.poll()
	assert.NilError(t, os.WriteFile(crumb, []byte(link), 0o644))
	d.poll()

	snap := d.snapshot(nil)
	assert.Equal(t, len(snap.Projects), 1, "one project listed once, got %d", len(snap.Projects))
	assert.Equal(t, snap.Projects[0].Root, canonical)
	// The log is not read twice into one project either.
	assert.Equal(t, len(snap.Projects[0].Events), 1)
}

// A registered command is filed under the project root its client sent, and
// listed under the root the daemon discovered — so the two have to be the same
// spelling. The clients send an unresolved working directory while discovery
// canonicalises, which for any repo reached through a symlink files a command's
// output where the dashboard will never look for it.
func TestPollListsCommandsRegisteredUnderAnUnresolvedRoot(t *testing.T) {
	t.Setenv(config.EnvXDGDataHome, t.TempDir())

	base := t.TempDir()
	target := filepath.Join(base, "project")
	assert.NilError(t, os.MkdirAll(target, 0o755))
	link := filepath.Join(base, "link")
	assert.NilError(t, os.Symlink(target, link))

	dataDir, err := config.ProjectDataDir(target)
	assert.NilError(t, err)
	assert.NilError(t, sidecar.RegisterProjectRoot(dataDir, target))

	d := newTestDaemon()
	// What a validate run on a repo reached through the symlink registers.
	d.out.register(reg("cmd-1", link), immediateStream([]string{"output\n"}, 0))
	waitForFinish(t, d.out, "cmd-1")
	d.poll()

	snap := d.snapshot(nil)
	assert.Equal(t, len(snap.Projects), 1)
	assert.Equal(t, len(snap.Projects[0].Commands), 1,
		"a command registered through a symlinked root must still be listed")
}
