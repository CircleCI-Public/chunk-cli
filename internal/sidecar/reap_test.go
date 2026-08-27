package sidecar

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
	"github.com/CircleCI-Public/chunk-cli/internal/testing/fakes"
)

const testOrg = "org-1"

// reapEnv is a project rooted in a temp dir with its own state directory and a
// client pointed at a fake API.
type reapEnv struct {
	t        *testing.T
	fake     *fakes.FakeCircleCI
	client   *circleci.Client
	stateDir string
}

func newReapEnv(t *testing.T) *reapEnv {
	t.Helper()
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	fake := fakes.NewFakeCircleCI()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)

	client, err := circleci.NewClient(circleci.Config{Token: "fake-token", BaseURL: srv.URL})
	assert.NilError(t, err)

	stateDir, err := config.ProjectDataDir(dir)
	assert.NilError(t, err)
	assert.NilError(t, os.MkdirAll(stateDir, 0o755))

	return &reapEnv{t: t, fake: fake, client: client, stateDir: stateDir}
}

// writeState writes a state file under name, aged by age, and registers the
// sidecar as live in the fake unless live is false.
func (e *reapEnv) writeState(name string, active ActiveSidecar, age time.Duration, live bool) string {
	e.t.Helper()
	path := filepath.Join(e.stateDir, name)
	data, err := json.Marshal(active)
	assert.NilError(e.t, err)
	assert.NilError(e.t, os.WriteFile(path, data, 0o644))

	stamp := time.Now().Add(-age)
	assert.NilError(e.t, os.Chtimes(path, stamp, stamp))

	if live {
		e.fake.Sidecars = append(e.fake.Sidecars, fakes.Sidecar{
			ID: active.SidecarID, Name: active.Name, OrgID: testOrg,
		})
	}
	return path
}

func (e *reapEnv) liveIDs() []string {
	e.t.Helper()
	ids := make([]string, 0, len(e.fake.Sidecars))
	for _, s := range e.fake.Sidecars {
		ids = append(ids, s.ID)
	}
	return ids
}

func exists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// TestReapDropsStateForVanishedSidecar covers the resurrection bug: a state file
// naming a sidecar that no longer exists must go, even when it is the file for
// the current session.
func TestReapDropsStateForVanishedSidecar(t *testing.T) {
	e := newReapEnv(t)
	ctx := context.Background()
	// Written under the no-session name, which is also the current session's name,
	// and recent enough that the age sweep would never touch it.
	path := e.writeState("sidecar.json", ActiveSidecar{SidecarID: "sb-dead"}, time.Minute, false)

	res, err := Reap(ctx, e.client, testOrg)
	assert.NilError(t, err)

	assert.DeepEqual(t, res.Vanished, []string{"sb-dead"})
	assert.Equal(t, len(res.Deleted), 0)
	assert.Assert(t, !exists(t, path), "state for a vanished sidecar must be removed")
}

// TestReapDeletesAbandonedSidecar covers a sidecar that is still running but
// whose state has not been touched for longer than StaleAfter.
func TestReapDeletesAbandonedSidecar(t *testing.T) {
	e := newReapEnv(t)
	ctx := session.WithID(context.Background(), "sess-current")
	path := e.writeState("sidecar.sess-old-deadbeef.json",
		ActiveSidecar{SidecarID: "sb-old", OrgID: testOrg}, StaleAfter+time.Hour, true)

	res, err := Reap(ctx, e.client, testOrg)
	assert.NilError(t, err)

	assert.DeepEqual(t, res.Deleted, []string{"sb-old"})
	assert.Assert(t, !exists(t, path), "state for a deleted sidecar must be removed")
	assert.Equal(t, len(e.liveIDs()), 0, "sidecar must be deleted through the API")
}

// TestReapKeepsRecentlyUsedSidecar covers a concurrent session on another
// branch: its sidecar is alive and its state file is warm, so it is not ours to
// delete.
func TestReapKeepsRecentlyUsedSidecar(t *testing.T) {
	e := newReapEnv(t)
	ctx := session.WithID(context.Background(), "sess-current")
	path := e.writeState("sidecar.sess-other-deadbeef.json",
		ActiveSidecar{SidecarID: "sb-busy", OrgID: testOrg}, time.Hour, true)

	res, err := Reap(ctx, e.client, testOrg)
	assert.NilError(t, err)

	assert.Assert(t, res.Empty(), "a recently used sidecar must be left alone")
	assert.Assert(t, exists(t, path), "state for a live sidecar in use must be kept")
	assert.DeepEqual(t, e.liveIDs(), []string{"sb-busy"})
}

// TestReapSparesCurrentSessionFromAgeSweep covers the sidecar this run is about
// to use. Even when its state is old, deleting it would only force an immediate
// recreate.
func TestReapSparesCurrentSessionFromAgeSweep(t *testing.T) {
	e := newReapEnv(t)
	ctx := session.WithID(context.Background(), "sess-current")
	// No git repo under the temp dir, so the current state file carries no branch.
	path := e.writeState(StateFileName("sess-current", ""),
		ActiveSidecar{SidecarID: "sb-mine", OrgID: testOrg}, StaleAfter+time.Hour, true)

	res, err := Reap(ctx, e.client, testOrg)
	assert.NilError(t, err)

	assert.Assert(t, res.Empty(), "the current session's own sidecar must not be reaped by age")
	assert.Assert(t, exists(t, path), "the current session's state must be kept")
	assert.DeepEqual(t, e.liveIDs(), []string{"sb-mine"})
}

// TestReapFailsOpenWhenListFails covers the case that matters most: an empty or
// failed listing is not proof of absence, so nothing may be destroyed.
func TestReapFailsOpenWhenListFails(t *testing.T) {
	e := newReapEnv(t)
	ctx := context.Background()
	path := e.writeState("sidecar.sess-a-deadbeef.json",
		ActiveSidecar{SidecarID: "sb-1", OrgID: testOrg}, StaleAfter+time.Hour, true)
	e.fake.ListStatusCode = 500

	res, err := Reap(ctx, e.client, testOrg)
	assert.Assert(t, err != nil, "a failed list must be reported")
	assert.Assert(t, res.Empty(), "a failed list must delete nothing")
	assert.Assert(t, exists(t, path), "a failed list must prune nothing")
	assert.DeepEqual(t, e.liveIDs(), []string{"sb-1"})
}

// TestReapIgnoresOtherOrgs covers state recorded against a different org, which
// this org's listing cannot judge.
func TestReapIgnoresOtherOrgs(t *testing.T) {
	e := newReapEnv(t)
	ctx := context.Background()
	// Absent from the listing, but only because it belongs to another org.
	path := e.writeState("sidecar.sess-a-deadbeef.json",
		ActiveSidecar{SidecarID: "sb-elsewhere", OrgID: "org-2"}, StaleAfter+time.Hour, false)

	res, err := Reap(ctx, e.client, testOrg)
	assert.NilError(t, err)

	assert.Assert(t, res.Empty(), "another org's sidecar must not be reaped")
	assert.Assert(t, exists(t, path), "another org's state must be kept")
}

func TestReapSkipsWithoutOrgOrClient(t *testing.T) {
	e := newReapEnv(t)
	ctx := context.Background()
	path := e.writeState("sidecar.json", ActiveSidecar{SidecarID: "sb-dead"}, time.Minute, false)

	res, err := Reap(ctx, e.client, "")
	assert.NilError(t, err)
	assert.Assert(t, res.Empty())

	res, err = Reap(ctx, nil, testOrg)
	assert.NilError(t, err)
	assert.Assert(t, res.Empty())
	assert.Assert(t, exists(t, path), "state must survive a skipped reap")
}

// TestPruneIDRemovesEveryDuplicate covers the duplication that made a single
// ClearActive useless: promotion copies an ID into a new file, so the same dead
// ID is recorded several times over.
func TestPruneIDRemovesEveryDuplicate(t *testing.T) {
	e := newReapEnv(t)
	ctx := context.Background()
	dup1 := e.writeState("sidecar.json", ActiveSidecar{SidecarID: "sb-dead"}, time.Minute, false)
	dup2 := e.writeState("sidecar.sess-a-deadbeef.json", ActiveSidecar{SidecarID: "sb-dead"}, time.Hour, false)
	other := e.writeState("sidecar.sess-b-cafebabe.json", ActiveSidecar{SidecarID: "sb-live"}, time.Hour, true)

	assert.NilError(t, PruneID(ctx, e.client, "sb-dead", false))

	assert.Assert(t, !exists(t, dup1), "every file naming the dead sidecar must go")
	assert.Assert(t, !exists(t, dup2), "every file naming the dead sidecar must go")
	assert.Assert(t, exists(t, other), "unrelated state must be untouched")
	// No remote delete was asked for, so the live sidecar is still there.
	assert.DeepEqual(t, e.liveIDs(), []string{"sb-live"})
}

// TestPruneIDDeletesRemoteWhenAsked covers a sidecar that still exists but can
// never be used again, so it must be deleted rather than orphaned.
func TestPruneIDDeletesRemoteWhenAsked(t *testing.T) {
	e := newReapEnv(t)
	ctx := context.Background()
	path := e.writeState("sidecar.json", ActiveSidecar{SidecarID: "sb-stale"}, time.Minute, true)

	assert.NilError(t, PruneID(ctx, e.client, "sb-stale", true))

	assert.Assert(t, !exists(t, path))
	assert.Equal(t, len(e.liveIDs()), 0, "an unusable sidecar must be deleted, not orphaned")
}

// TestPruneIDDropsStateWhenRemoteDeleteFails covers the ordering that keeps a
// dead ID from coming back: the local state must go even when the API call for
// it fails, or the next run reuses the same unusable sidecar.
func TestPruneIDDropsStateWhenRemoteDeleteFails(t *testing.T) {
	e := newReapEnv(t)
	ctx := context.Background()
	path := e.writeState("sidecar.json", ActiveSidecar{SidecarID: "sb-stale"}, time.Minute, true)
	e.fake.DeleteStatusCode = 500

	err := PruneID(ctx, e.client, "sb-stale", true)
	assert.Assert(t, err != nil, "a failed delete must be reported")
	assert.Assert(t, !exists(t, path), "state must be dropped even when the delete fails")
}

// TestReapDropsStateWhenDeleteReturnsGone covers a sidecar the listing still
// shows but the API has already let go of. The end state is the same as one that
// was never listed.
func TestReapDropsStateWhenDeleteReturnsGone(t *testing.T) {
	e := newReapEnv(t)
	ctx := context.Background()
	path := e.writeState("sidecar.sess-a-deadbeef.json",
		ActiveSidecar{SidecarID: "sb-ghost", OrgID: testOrg}, StaleAfter+time.Hour, true)
	e.fake.DeleteStatusCode = 404

	res, err := Reap(ctx, e.client, testOrg)
	assert.NilError(t, err)

	assert.DeepEqual(t, res.Vanished, []string{"sb-ghost"})
	assert.Equal(t, len(res.Failed), 0, "a 404 is not a failure to report")
	assert.Assert(t, !exists(t, path))
}

// TestReapDropsStateWhenDeleteFails is the regression for the state file that
// survived a failed delete, became the active sidecar, and made every following
// run fail against a box the API would not release.
func TestReapDropsStateWhenDeleteFails(t *testing.T) {
	e := newReapEnv(t)
	ctx := context.Background()
	path := e.writeState("sidecar.sess-a-deadbeef.json",
		ActiveSidecar{SidecarID: "sb-stuck", OrgID: testOrg}, StaleAfter+time.Hour, true)
	e.fake.DeleteStatusCode = 500

	res, err := Reap(ctx, e.client, testOrg)
	assert.NilError(t, err)

	assert.DeepEqual(t, res.Failed, []string{"sb-stuck"})
	assert.Assert(t, !exists(t, path), "a failed delete must not leave state files behind")
	assert.Assert(t, strings.Contains(res.Summary(), "may still be running"),
		"the leak must be reported, got: %s", res.Summary())
}
