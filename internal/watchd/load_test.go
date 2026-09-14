package watchd

import (
	"os"
	"path/filepath"
	"strconv"
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

func TestLoadSidecarsExpandsActivePoolMembers(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_ids":["id1","id2","id3"],"name":"validate"}`)

	result := loadSidecars(dir, root, "")
	assert.Equal(t, len(result), 3)
	for i, want := range []string{"id1", "id2", "id3"} {
		assert.Equal(t, result[i].ID, want)
		assert.Equal(t, result[i].Name, "validate-"+strconv.Itoa(i+1))
	}
}

func TestLoadSidecars_readsPoolState(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	chunkDir := filepath.Join(root, ".chunk")
	assert.NilError(t, os.Mkdir(chunkDir, 0o755))
	writeSidecarJSON(t, chunkDir, "validate-pool.json", `{"sidecar_ids":["id1","id2"]}`)

	result := loadSidecars(dataDir, root, "")
	assert.Equal(t, len(result), 2)
	assert.Equal(t, result[0].Name, "validate-1")
	assert.Equal(t, result[1].Name, "validate-2")
}

func TestLoadSidecars_deduplicatesActiveAndPoolState(t *testing.T) {
	dataDir := t.TempDir()
	root := t.TempDir()
	chunkDir := filepath.Join(root, ".chunk")
	assert.NilError(t, os.Mkdir(chunkDir, 0o755))

	writeSidecarJSON(t, dataDir, "sidecar.json", `{"sidecar_ids":["id1","id2"],"name":"active","session_id":"session-1","workspace":"/active/workspace"}`)
	writeSidecarJSON(t, chunkDir, "validate-pool.json", `{"sidecar_ids":["id2","id3"],"repo_path":"/pool/workspace"}`)
	newer := time.Now().Add(time.Hour)
	assert.NilError(t, os.Chtimes(filepath.Join(chunkDir, "validate-pool.json"), newer, newer))

	result := loadSidecars(dataDir, root, "")
	assert.Equal(t, len(result), 3)
	byID := make(map[string]SidecarState, len(result))
	for _, state := range result {
		byID[state.ID] = state
	}
	assert.Equal(t, byID["id2"].Name, "active-2")
	assert.Equal(t, byID["id2"].SessionID, "session-1")
	assert.Equal(t, byID["id2"].Workspace, "/active/workspace")
	assert.Equal(t, byID["id3"].Name, "validate-2")
	assert.Equal(t, byID["id3"].Workspace, "/pool/workspace")
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
