package sidecar

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
)

// adoptEnv is a project rooted in a temp dir with its own state directory.
type adoptEnv struct {
	t        *testing.T
	stateDir string
}

func newAdoptEnv(t *testing.T) *adoptEnv {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	stateDir, err := config.ProjectDataDir(dir)
	assert.NilError(t, err)
	assert.NilError(t, os.MkdirAll(stateDir, 0o755))
	return &adoptEnv{t: t, stateDir: stateDir}
}

// write writes a state file for sessionID, owned by it, aged by age.
func (e *adoptEnv) write(sessionID, sidecarID string, age time.Duration) string {
	e.t.Helper()
	return e.writeAs(StateFileName(sessionID, ""), ActiveSidecar{
		SidecarID: sidecarID,
		SessionID: sessionID,
	}, age)
}

// writeAs writes active to name verbatim, so a test can describe state that no
// current code path would produce (unowned files, mismatched keys).
func (e *adoptEnv) writeAs(name string, active ActiveSidecar, age time.Duration) string {
	e.t.Helper()
	path := filepath.Join(e.stateDir, name)
	data, err := json.Marshal(active)
	assert.NilError(e.t, err)
	assert.NilError(e.t, os.WriteFile(path, data, 0o644))
	stamp := time.Now().Add(-age)
	assert.NilError(e.t, os.Chtimes(path, stamp, stamp))
	return path
}

func (e *adoptEnv) read(name string) ActiveSidecar {
	e.t.Helper()
	data, err := os.ReadFile(filepath.Join(e.stateDir, name))
	assert.NilError(e.t, err)
	var a ActiveSidecar
	assert.NilError(e.t, json.Unmarshal(data, &a))
	return a
}

// TestAdoptIdleActiveLeavesLiveSessionAlone is the point of the whole mechanism:
// two agent sessions in one working tree must not drive one sidecar, so the
// second session finds nothing to adopt and goes on to create its own.
func TestAdoptIdleActiveLeavesLiveSessionAlone(t *testing.T) {
	e := newAdoptEnv(t)
	path := e.write("sess-a", "sb-a", time.Minute)

	got, err := AdoptIdleActive(session.WithID(context.Background(), "sess-b"))
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "a sidecar in use by a live session must not be adoptable")
	assert.Assert(t, exists(t, path), "the live session's own state must survive")
}

// TestAdoptIdleActiveTakesUnownedState covers the setup handoff: a sidecar
// created in a plain terminal has no owner, so the first session to want one
// takes it straight away rather than creating a second sidecar.
func TestAdoptIdleActiveTakesUnownedState(t *testing.T) {
	e := newAdoptEnv(t)
	e.writeAs(defaultSidecarFile, ActiveSidecar{SidecarID: "sb-manual"}, time.Second)

	got, err := AdoptIdleActive(session.WithID(context.Background(), "sess-a"))
	assert.NilError(t, err)
	assert.Assert(t, got != nil, "unowned state must be adoptable without waiting")
	assert.Equal(t, got.SidecarID, "sb-manual")
}

// TestAdoptIdleActiveTakesOwnStateFromAnotherBranch keeps a session on one
// sidecar as it switches branches. Its own state is never withheld from it, no
// matter how recently it was written.
func TestAdoptIdleActiveTakesOwnStateFromAnotherBranch(t *testing.T) {
	e := newAdoptEnv(t)
	e.writeAs("sidecar.sess-a-deadbeef.json", ActiveSidecar{
		SidecarID: "sb-a",
		SessionID: "sess-a",
	}, time.Second)

	got, err := AdoptIdleActive(session.WithID(context.Background(), "sess-a"))
	assert.NilError(t, err)
	assert.Assert(t, got != nil, "a session must always be able to reclaim its own sidecar")
	assert.Equal(t, got.SidecarID, "sb-a")
}

// TestAdoptIdleActivePrefersNewest picks the sidecar whose remote workspace is
// closest to the current tree, which is the one used most recently.
func TestAdoptIdleActivePrefersNewest(t *testing.T) {
	e := newAdoptEnv(t)
	// Two same-session files from different branches: both are adoptable by sess-b.
	e.writeAs("sidecar.sess-b-aaaa0000.json", ActiveSidecar{SidecarID: "sb-old", SessionID: "sess-b"}, 20*24*time.Hour)
	e.writeAs("sidecar.sess-b-bbbb1111.json", ActiveSidecar{SidecarID: "sb-recent", SessionID: "sess-b"}, time.Minute)

	got, err := AdoptIdleActive(session.WithID(context.Background(), "sess-b"))
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-recent")
}

// TestAdoptIdleActiveIgnoresEmptyState guards against handing back a state file
// that names no sidecar, which would stop a real one from being created.
func TestAdoptIdleActiveIgnoresEmptyState(t *testing.T) {
	e := newAdoptEnv(t)
	e.writeAs(defaultSidecarFile, ActiveSidecar{}, time.Second)

	got, err := AdoptIdleActive(session.WithID(context.Background(), "sess-b"))
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "state naming no sidecar must not be adopted")
}

// TestAdoptIdleActiveLeavesLegacyStateAlone is the regression for the sidecar
// that was taken from a live session on the first run after an upgrade: state
// written before the owner was recorded in the file is still a session's, and its
// session-keyed file name says so even though the file itself cannot name it.
func TestAdoptIdleActiveLeavesLegacyStateAlone(t *testing.T) {
	e := newAdoptEnv(t)
	legacy := e.writeAs("sidecar.sess-a-deadbeef.json", ActiveSidecar{SidecarID: "sb-a"}, time.Minute)

	got, err := AdoptIdleActive(session.WithID(context.Background(), "sess-b"))
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "state from before owners were recorded must not be adopted")
	assert.Assert(t, exists(t, legacy))
}

// TestSaveActiveRecordsOwningSession is what adoption is gated on, so the owner
// must be whoever wrote the file — not whatever the value being saved carried.
func TestSaveActiveRecordsOwningSession(t *testing.T) {
	e := newAdoptEnv(t)
	ctx := session.WithID(context.Background(), "sess-b")

	assert.NilError(t, SaveActive(ctx, ActiveSidecar{SidecarID: "sb-1", SessionID: "sess-a"}))

	assert.Equal(t, e.read(StateFileName("sess-b", "")).SessionID, "sess-b", "")
}
