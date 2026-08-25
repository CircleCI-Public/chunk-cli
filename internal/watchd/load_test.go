package watchd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gotest.tools/v3/assert"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
)

func writeSidecarJSON(t *testing.T, dir, filename, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644)
	assert.NilError(t, err)
}

func TestLoadSidecars_deduplicatesIDs(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1","last_synced_ref":"abc123"}`)
	writeSidecarJSON(t, dir, "sidecar.sess1.json", `{"sidecar_id":"id2","name":"sc2"}`)
	writeSidecarJSON(t, dir, "sidecar.sess2.json", `{"sidecar_id":"id1","name":"sc1"}`)

	result := loadSidecars(dir, root, "", "abc123")
	assert.Equal(t, len(result), 2, "want 2 unique sidecars")
}

func TestLoadSidecars_inSyncWhenHeadMatches(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1","last_synced_ref":"abc123"}`)

	result := loadSidecars(dir, root, "", "abc123")
	assert.Equal(t, len(result), 1)
	assert.Assert(t, result[0].InSync, "sidecar should be in sync when LastSyncedRef matches head")
}

func TestLoadSidecars_notInSyncWhenHeadDiffers(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","last_synced_ref":"oldref"}`)

	result := loadSidecars(dir, root, "", "newref")
	assert.Equal(t, len(result), 1)
	assert.Assert(t, !result[0].InSync, "sidecar should not be in sync when refs differ")
}

func TestLoadSidecars_emptyDir(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	result := loadSidecars(dir, root, "", "")
	assert.Equal(t, len(result), 0)
}

func TestLoadSidecars_skipsEmptySidecarID(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"","name":"empty"}`)

	result := loadSidecars(dir, root, "", "")
	assert.Equal(t, len(result), 0, "want 0 (skipped empty ID)")
}

func TestLoadSidecars_snapshotName(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1"}`)

	result := loadSidecars(dir, root, "my-snap", "")
	assert.Equal(t, len(result), 1)
	assert.Equal(t, result[0].SnapshotName, "my-snap")
}

func TestLoadSidecars_prefersNewestFileForDuplicateID(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.aaa.json", `{"sidecar_id":"id1","name":"sc1","last_synced_ref":"oldref"}`)
	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1","last_synced_ref":"newref"}`)

	old := time.Now().Add(-time.Hour)
	err := os.Chtimes(filepath.Join(dir, "sidecar.aaa.json"), old, old)
	assert.NilError(t, err)

	result := loadSidecars(dir, root, "", "newref")
	assert.Equal(t, len(result), 1)
	assert.Equal(t, result[0].LastSyncedRef, "newref")
	assert.Assert(t, result[0].InSync, "sidecar should be in sync when the newest state file matches head")
}

func TestCapEvents_keepsNewest(t *testing.T) {
	prior := make([]eventlog.Event, RecentEvents)
	for i := range prior {
		prior[i] = eventlog.Event{SidecarID: "old"}
	}
	fresh := []eventlog.Event{{SidecarID: "new"}}

	got := capEvents(prior, fresh, RecentEvents)

	assert.Equal(t, len(got), RecentEvents)
	assert.Equal(t, got[len(got)-1].SidecarID, "new", "newest event should survive the cap")
}
