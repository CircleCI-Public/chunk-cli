package sidecar

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/config"
	"github.com/CircleCI-Public/chunk-cli/internal/session"
)

func hashFor(sessionID, branch string) string {
	sum := sha256.Sum256([]byte(sessionID + ":" + branch))
	return fmt.Sprintf("%x", sum[:4])
}

func TestSaveActiveWritesToXDGDataPath(t *testing.T) {
	dataHome := t.TempDir()
	t.Setenv(config.EnvXDGDataHome, dataHome)

	dir := t.TempDir()
	t.Chdir(dir)

	assert.NilError(t, SaveActive(context.Background(), ActiveSidecar{SidecarID: "sb-1"}))

	// Must not appear inside the project's .chunk directory.
	_, err := os.Stat(filepath.Join(dir, ".chunk", "sidecar.json"))
	assert.Assert(t, os.IsNotExist(err), "sidecar.json must not be written inside .chunk/")

	// Must appear at the deterministic XDG data path.
	expected, err := config.ProjectDataDir(dir)
	assert.NilError(t, err)
	_, err = os.Stat(filepath.Join(expected, "sidecar.json"))
	assert.NilError(t, err)
}

func TestStatOrEmptyPermissionsError(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: root bypasses file permission checks")
	}
	dir := t.TempDir()
	assert.NilError(t, os.Chmod(dir, 0o000))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	_, err := statOrEmpty(filepath.Join(dir, "sidecar.json"))
	assert.Assert(t, err != nil, "expected error for inaccessible directory, got nil")
}

func setupXDGData(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvXDGDataHome, t.TempDir())
}

func TestSaveAndLoadActive(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	ctx := context.Background()
	want := ActiveSidecar{SidecarID: "sb-abc", Name: "my-box"}
	err := SaveActive(ctx, want)
	assert.NilError(t, err)

	got, err := LoadActive(ctx)
	assert.NilError(t, err)
	assert.Assert(t, got != nil, "expected non-nil ActiveSidecar")
	assert.Equal(t, got.SidecarID, want.SidecarID)
	assert.Equal(t, got.Name, want.Name)
}

func TestLoadActiveReturnsNilWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	got, err := LoadActive(context.Background())
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "expected nil when no active sidecar file")
}

func TestLoadActiveUsesGitRootAsKey(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "sub", "dir")
	assert.NilError(t, os.MkdirAll(child, 0o755))
	assert.NilError(t, os.MkdirAll(filepath.Join(parent, ".git"), 0o755))

	setupXDGData(t)

	ctx := context.Background()

	// Save from child — keyed to parent (git root).
	t.Chdir(child)
	assert.NilError(t, SaveActive(ctx, ActiveSidecar{SidecarID: "sb-git-root"}))

	// Load from child — should find it.
	got, err := LoadActive(ctx)
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-git-root")

	// Load from parent (the git root) — same project, same file.
	t.Chdir(parent)
	got, err = LoadActive(ctx)
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-git-root")
}

func TestLoadActiveUsesCwdWhenNoGitRepo(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	ctx := context.Background()
	assert.NilError(t, SaveActive(ctx, ActiveSidecar{SidecarID: "sb-cwd"}))

	got, err := LoadActive(ctx)
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-cwd")
}

func TestClearActive(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	ctx := context.Background()
	assert.NilError(t, SaveActive(ctx, ActiveSidecar{SidecarID: "sb-xyz"}))

	got, err := LoadActive(ctx)
	assert.NilError(t, err)
	assert.Assert(t, got != nil)

	assert.NilError(t, ClearActive(ctx))

	got, err = LoadActive(ctx)
	assert.NilError(t, err)
	assert.Assert(t, got == nil)
}

func TestSessionKeyedSidecar(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	ctx := context.Background()
	sessCtx := session.WithID(ctx, "sess-abc")

	// Save without a session — generic file.
	assert.NilError(t, SaveActive(ctx, ActiveSidecar{SidecarID: "sb-generic"}))

	// Session-keyed load should not see the generic file.
	got, err := LoadActive(sessCtx)
	assert.NilError(t, err)
	assert.Assert(t, got == nil, "session-keyed load should not see generic file")

	// Save under the session.
	assert.NilError(t, SaveActive(sessCtx, ActiveSidecar{SidecarID: "sb-session"}))

	got, err = LoadActive(sessCtx)
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-session")

	// Without the session, the original generic file is still intact.
	got, err = LoadActive(ctx)
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.SidecarID, "sb-generic")
}

func TestClearActiveNoopWhenMissing(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	assert.NilError(t, ClearActive(context.Background()))
}

func TestWorkspaceFieldRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	ctx := context.Background()
	want := ActiveSidecar{SidecarID: "sb-1", Name: "test", Workspace: "/workspace/myrepo"}
	assert.NilError(t, SaveActive(ctx, want))

	got, err := LoadActive(ctx)
	assert.NilError(t, err)
	assert.Assert(t, got != nil)
	assert.Equal(t, got.Workspace, want.Workspace)
	assert.Equal(t, got.SidecarID, want.SidecarID)
}

func TestWorkspaceOmittedWhenEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	ctx := context.Background()
	assert.NilError(t, SaveActive(ctx, ActiveSidecar{SidecarID: "sb-1"}))

	stateDir, err := saveDir()
	assert.NilError(t, err)
	data, err := os.ReadFile(filepath.Join(stateDir, sidecarFileName("", "")))
	assert.NilError(t, err)
	assert.Assert(t, !strings.Contains(string(data), "workspace"), "empty workspace should be omitted from JSON")
}

func TestResolveWorkspaceCLIFlagWins(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	ctx := context.Background()
	assert.NilError(t, SaveActive(ctx, ActiveSidecar{SidecarID: "sb-1", Workspace: "/workspace/saved"}))

	got, err := ResolveWorkspace(ctx, "/workspace/override", "myrepo")
	assert.NilError(t, err)
	assert.Equal(t, got, "/workspace/override")
}

func TestResolveWorkspaceSidecarFallback(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	ctx := context.Background()
	assert.NilError(t, SaveActive(ctx, ActiveSidecar{SidecarID: "sb-1", Workspace: "/workspace/saved"}))

	got, err := ResolveWorkspace(ctx, "", "myrepo")
	assert.NilError(t, err)
	assert.Equal(t, got, "/workspace/saved")
}

func TestResolveWorkspaceDefaultFallback(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	got, err := ResolveWorkspace(context.Background(), "", "myrepo")
	assert.NilError(t, err)
	assert.Equal(t, got, "/home/user/myrepo")
}

func TestResolveWorkspaceEmptyRepoErrors(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	_, err := ResolveWorkspace(context.Background(), "", "")
	assert.ErrorContains(t, err, "repo name is empty")
}

func TestResolveWorkspaceSidecarHomeEnvVar(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	setupXDGData(t)

	t.Setenv("CHUNK_SIDECAR_HOME", "/home/runner")

	got, err := ResolveWorkspace(context.Background(), "", "myrepo")
	assert.NilError(t, err)
	assert.Equal(t, got, "/home/runner/myrepo")
}

func TestSidecarFileNameCases(t *testing.T) {
	cases := []struct {
		session string
		branch  string
		want    string
	}{
		{"", "", "sidecar.json"},
		{"sess-1", "", "sidecar.sess-1.json"},
		{"", "main", "sidecar.json"},
		{"sess-1", "main", "sidecar.sess-1-" + hashFor("sess-1", "main") + ".json"},
	}
	for _, tc := range cases {
		got := sidecarFileName(tc.session, tc.branch)
		assert.Equal(t, got, tc.want, "session=%q branch=%q", tc.session, tc.branch)
	}
}

func TestSidecarFileNameHashUniquenessAcrossBranches(t *testing.T) {
	f1 := sidecarFileName("sess-abc", "main")
	f2 := sidecarFileName("sess-abc", "feature/my-branch")
	assert.Assert(t, f1 != f2, "different branches must produce different file names")
}

func TestSaveActivePrunesRekeyedStateFiles(t *testing.T) {
	setupXDGData(t)
	dir := t.TempDir()
	t.Chdir(dir)

	stateDir, err := config.ProjectDataDir(dir)
	assert.NilError(t, err)
	assert.NilError(t, os.MkdirAll(stateDir, 0o755))

	// A file another session left behind naming the same sidecar, plus one naming
	// a different sidecar that must survive.
	stale := filepath.Join(stateDir, "sidecar.other-session.json")
	assert.NilError(t, os.WriteFile(stale, []byte(`{"sidecar_id":"sb-1","last_synced_ref":"oldref"}`), 0o644))
	keep := filepath.Join(stateDir, "sidecar.unrelated.json")
	assert.NilError(t, os.WriteFile(keep, []byte(`{"sidecar_id":"sb-2"}`), 0o644))

	assert.NilError(t, SaveActive(context.Background(), ActiveSidecar{SidecarID: "sb-1"}))

	_, err = os.Stat(stale)
	assert.Assert(t, os.IsNotExist(err), "state file re-keyed to a new name must be removed")
	_, err = os.Stat(keep)
	assert.NilError(t, err, "state for a different sidecar must survive")
	_, err = os.Stat(filepath.Join(stateDir, "sidecar.json"))
	assert.NilError(t, err)
}

func TestClearActiveByOrgRemovesOnlyMatchingOrg(t *testing.T) {
	setupXDGData(t)
	base, err := config.AppData()
	assert.NilError(t, err)

	// Two project directories, each holding state for both orgs.
	writeState := func(project, file, orgID string) string {
		dir := filepath.Join(base, project)
		assert.NilError(t, os.MkdirAll(dir, 0o755))
		path := filepath.Join(dir, file)
		body := fmt.Sprintf(`{"sidecar_id":"sb-1","org_id":%q}`, orgID)
		assert.NilError(t, os.WriteFile(path, []byte(body), 0o644))
		return path
	}
	goneA := writeState("projA", "sidecar.json", "org-1")
	goneB := writeState("projB", "sidecar.json", "org-1")
	keepA := writeState("projA", "sidecar.other.json", "org-2")
	// State written before org_id existed must not be swept.
	legacy := filepath.Join(base, "projB", "sidecar.legacy.json")
	assert.NilError(t, os.WriteFile(legacy, []byte(`{"sidecar_id":"sb-9"}`), 0o644))
	// A stray non-directory entry at the base must be skipped, not fatal.
	assert.NilError(t, os.WriteFile(filepath.Join(base, "auth.json"), []byte(`{}`), 0o644))

	removed, err := ClearActiveByOrg("org-1")
	assert.NilError(t, err)
	assert.Equal(t, removed, 2)

	for _, p := range []string{goneA, goneB} {
		_, statErr := os.Stat(p)
		assert.Assert(t, os.IsNotExist(statErr), "state for the pruned org must be removed: %s", p)
	}
	for _, p := range []string{keepA, legacy} {
		_, statErr := os.Stat(p)
		assert.NilError(t, statErr, "state outside the pruned org must survive")
	}
}

func TestClearActiveByOrgNoopWhenBaseMissing(t *testing.T) {
	setupXDGData(t)

	removed, err := ClearActiveByOrg("org-1")
	assert.NilError(t, err)
	assert.Equal(t, removed, 0)
}

func TestClearActiveByOrgReportsRemovalFailures(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("skipping: root bypasses file permission checks")
	}
	setupXDGData(t)
	base, err := config.AppData()
	assert.NilError(t, err)

	// A readable project whose state must still be cleared.
	good := filepath.Join(base, "projGood")
	assert.NilError(t, os.MkdirAll(good, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(good, "sidecar.json"), []byte(`{"sidecar_id":"sb-1","org_id":"org-1"}`), 0o644))

	// An unreadable project directory: os.Remove of its state file fails.
	bad := filepath.Join(base, "projBad")
	assert.NilError(t, os.MkdirAll(bad, 0o755))
	assert.NilError(t, os.WriteFile(filepath.Join(bad, "sidecar.json"), []byte(`{"sidecar_id":"sb-2","org_id":"org-1"}`), 0o644))
	assert.NilError(t, os.Chmod(bad, 0o500))
	t.Cleanup(func() { _ = os.Chmod(bad, 0o755) })

	removed, err := ClearActiveByOrg("org-1")
	assert.Assert(t, err != nil, "failure to remove state must surface as an error")
	assert.Equal(t, removed, 1, "the sweep must continue past a failing project")
}
