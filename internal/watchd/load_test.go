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

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1"}`)
	writeSidecarJSON(t, dir, "sidecar.sess1.json", `{"sidecar_id":"id2","name":"sc2"}`)
	writeSidecarJSON(t, dir, "sidecar.sess2.json", `{"sidecar_id":"id1","name":"sc1"}`)

	result := loadSidecars(dir, root, "")
	assert.Equal(t, len(result), 2, "want 2 unique sidecars")
}

func TestLoadSidecars_carriesSessionID(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	// Two sessions holding a sidecar each for the same project: the session ID
	// is the only thing that tells their state files apart, so dropping it
	// leaves the dashboard unable to label them.
	writeSidecarJSON(t, dir, "sidecar.sessA.json", `{"sidecar_id":"id1","name":"sc1","session_id":"sessA"}`)
	writeSidecarJSON(t, dir, "sidecar.sessB.json", `{"sidecar_id":"id2","name":"sc2","session_id":"sessB"}`)
	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id3","name":"sc3"}`)

	result := loadSidecars(dir, root, "")
	assert.Equal(t, len(result), 3)

	got := map[string]string{}
	for _, ss := range result {
		got[ss.ID] = ss.SessionID
	}
	assert.Equal(t, got["id1"], "sessA")
	assert.Equal(t, got["id2"], "sessB")
	// State written outside a session stays unattributed rather than guessing.
	assert.Equal(t, got["id3"], "")
}


func TestLoadSidecars_emptyDir(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	result := loadSidecars(dir, root, "")
	assert.Equal(t, len(result), 0)
}

func TestLoadSidecars_skipsEmptySidecarID(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"","name":"empty"}`)

	result := loadSidecars(dir, root, "")
	assert.Equal(t, len(result), 0, "want 0 (skipped empty ID)")
}

func TestLoadSidecars_snapshotName(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1"}`)

	result := loadSidecars(dir, root, "my-snap")
	assert.Equal(t, len(result), 1)
	assert.Equal(t, result[0].SnapshotName, "my-snap")
}

func TestLoadSidecars_prefersNewestFileForDuplicateID(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.aaa.json", `{"sidecar_id":"id1","name":"sc1-old"}`)
	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1-new"}`)

	old := time.Now().Add(-time.Hour)
	err := os.Chtimes(filepath.Join(dir, "sidecar.aaa.json"), old, old)
	assert.NilError(t, err)

	result := loadSidecars(dir, root, "")
	assert.Equal(t, len(result), 1)
	assert.Equal(t, result[0].Name, "sc1-new", "newest file's name should win")
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
