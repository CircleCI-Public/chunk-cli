package watchd

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
)

func writeSidecarJSON(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// LoadSidecars tests

func TestLoadSidecars_deduplicatesIDs(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1","last_synced_ref":"abc123"}`)
	writeSidecarJSON(t, dir, "sidecar.sess1.json", `{"sidecar_id":"id2","name":"sc2"}`)
	writeSidecarJSON(t, dir, "sidecar.sess2.json", `{"sidecar_id":"id1","name":"sc1"}`) // duplicate

	result := LoadSidecars(dir, root, "", "abc123")
	if len(result) != 2 {
		t.Fatalf("want 2 unique sidecars, got %d", len(result))
	}
}

func TestLoadSidecars_inSyncWhenHeadMatches(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1","last_synced_ref":"abc123"}`)

	result := LoadSidecars(dir, root, "", "abc123")
	if len(result) != 1 {
		t.Fatalf("want 1, got %d", len(result))
	}
	if !result[0].InSync {
		t.Error("sidecar should be in sync when lastSyncedRef matches head")
	}
}

func TestLoadSidecars_notInSyncWhenHeadDiffers(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","last_synced_ref":"oldref"}`)

	result := LoadSidecars(dir, root, "", "newref")
	if len(result) != 1 {
		t.Fatalf("want 1, got %d", len(result))
	}
	if result[0].InSync {
		t.Error("sidecar should not be in sync when refs differ")
	}
}

func TestLoadSidecars_emptyDir(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	if result := LoadSidecars(dir, root, "", ""); len(result) != 0 {
		t.Errorf("want 0, got %d", len(result))
	}
}

func TestLoadSidecars_skipsEmptySidecarID(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"","name":"empty"}`)

	if result := LoadSidecars(dir, root, "", ""); len(result) != 0 {
		t.Errorf("want 0 (skipped empty ID), got %d", len(result))
	}
}

func TestLoadSidecars_snapshotName(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1"}`)

	result := LoadSidecars(dir, root, "my-snap", "")
	if len(result) != 1 {
		t.Fatalf("want 1, got %d", len(result))
	}
	if result[0].SnapshotName != "my-snap" {
		t.Errorf("SnapshotName should be 'my-snap', got %q", result[0].SnapshotName)
	}
}

// SortByActivity tests

func TestSortByActivity_freshestProjectFirst(t *testing.T) {
	now := time.Now()
	sidecars := []SidecarState{
		{ID: "stale", ProjectName: "old-project", FileMtime: now.Add(-6 * time.Hour)},
		{ID: "older", ProjectName: "busy-project", LastActivity: now.Add(-30 * time.Minute)},
		{ID: "newest", ProjectName: "busy-project", LastActivity: now.Add(-1 * time.Minute)},
	}

	SortByActivity(sidecars)

	want := []string{"newest", "older", "stale"}
	for i, id := range want {
		if sidecars[i].ID != id {
			t.Errorf("position %d: want %s, got %s", i, id, sidecars[i].ID)
		}
	}
}

func TestSortByActivity_keepsProjectsGrouped(t *testing.T) {
	now := time.Now()
	sidecars := []SidecarState{
		{ID: "a1", ProjectName: "a", LastActivity: now.Add(-2 * time.Minute)},
		{ID: "b1", ProjectName: "b", LastActivity: now.Add(-1 * time.Minute)},
		{ID: "a2", ProjectName: "a", LastActivity: now.Add(-3 * time.Minute)},
	}

	SortByActivity(sidecars)

	want := []string{"b1", "a1", "a2"}
	for i, id := range want {
		if sidecars[i].ID != id {
			t.Errorf("position %d: want %s, got %s", i, id, sidecars[i].ID)
		}
	}
}

// FilterSidecars tests

func TestFilterSidecars_noPerProjectCap(t *testing.T) {
	now := time.Now()
	sidecars := []SidecarState{
		{ID: "s1", ProjectName: "p", LastActivity: now.Add(-1 * time.Minute)},
		{ID: "s2", ProjectName: "p", LastActivity: now.Add(-2 * time.Minute)},
		{ID: "s3", ProjectName: "p", LastActivity: now.Add(-3 * time.Minute)},
		{ID: "s4", ProjectName: "p", LastActivity: now.Add(-4 * time.Minute)},
	}

	SortByActivity(sidecars)
	got := FilterSidecars(sidecars)

	if len(got) != 4 {
		t.Fatalf("want all 4 sidecars, got %d", len(got))
	}
	for i, id := range []string{"s1", "s2", "s3", "s4"} {
		if got[i].ID != id {
			t.Errorf("position %d: want %s, got %s", i, id, got[i].ID)
		}
	}
}

func TestFilterSidecars_dropsInactive(t *testing.T) {
	now := time.Now()
	sidecars := []SidecarState{
		{ID: "recent", ProjectName: "p", LastActivity: now.Add(-59 * time.Minute)},
		{ID: "just-aged-out", ProjectName: "p", LastActivity: now.Add(-61 * time.Minute)},
		{ID: "mtime-recent", ProjectName: "q", FileMtime: now.Add(-10 * time.Minute)},
		{ID: "mtime-old", ProjectName: "q", FileMtime: now.Add(-25 * time.Hour)},
		{ID: "no-activity-at-all", ProjectName: "r"},
	}

	got := FilterSidecars(sidecars)

	want := []string{"recent", "mtime-recent"}
	if len(got) != len(want) {
		t.Fatalf("want %d sidecars, got %d", len(want), len(got))
	}
	for i, id := range want {
		if got[i].ID != id {
			t.Errorf("position %d: want %s, got %s", i, id, got[i].ID)
		}
	}
}

// CapEvents tests

func TestCapEvents_capsPerProject(t *testing.T) {
	prior := make([]eventlog.Event, RecentEvents)
	for i := range prior {
		prior[i] = eventlog.Event{SidecarID: "old", Msg: "old"}
	}
	fresh := []eventlog.Event{{SidecarID: "new", Msg: "new"}}

	got := CapEvents(prior, fresh, RecentEvents)

	if len(got) != RecentEvents {
		t.Fatalf("want %d events, got %d", RecentEvents, len(got))
	}
	if got[len(got)-1].SidecarID != "new" {
		t.Errorf("newest event should survive the cap, got %s", got[len(got)-1].SidecarID)
	}
}

// AnnotateActivity + integration: busy project should not evict quiet project's events

func TestDaemonPoll_busyProjectDoesNotEvictQuiet(t *testing.T) {
	noisyDir := t.TempDir()
	quietDir := t.TempDir()

	writeSidecarJSON(t, noisyDir, "sidecar.json", `{"sidecar_id":"noisy","name":"noisy"}`)
	writeSidecarJSON(t, quietDir, "sidecar.json", `{"sidecar_id":"quiet","name":"quiet"}`)

	noisyLog, err := eventlog.Open(noisyDir)
	if err != nil {
		t.Fatal(err)
	}
	quietLog, err := eventlog.Open(quietDir)
	if err != nil {
		t.Fatal(err)
	}

	// Noisy project exceeds the per-project event cap with old events.
	for i := 0; i < RecentEvents+50; i++ {
		if err := noisyLog.Append(eventlog.Event{
			Ts: time.Now().Add(-2 * time.Hour), SidecarID: "noisy",
			Op: eventlog.OpValidate, Level: "info", Msg: "noise",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Quiet project has one recent event.
	if err := quietLog.Append(eventlog.Event{
		Ts: time.Now(), SidecarID: "quiet",
		Op: eventlog.OpValidate, Level: "done", Msg: "validate passed",
	}); err != nil {
		t.Fatal(err)
	}

	// Simulate what the daemon does per project.
	noisyState := &projectState{root: filepath.Join(noisyDir, "noisy-project"), dataDir: noisyDir, log: noisyLog}
	quietState := &projectState{root: filepath.Join(quietDir, "quiet-project"), dataDir: quietDir, log: quietLog}
	d := &daemon{projects: map[string]*projectState{
		noisyState.root: noisyState,
		quietState.root: quietState,
	}}

	d.mu.Lock()
	d.updateProject(noisyState)
	d.updateProject(quietState)
	d.mu.Unlock()

	// Noisy sidecar events are 2h old, outside activeWindow — it should be filtered.
	if len(noisyState.snap.Sidecars) != 0 {
		t.Errorf("noisy sidecar should be filtered (all events are 2h old), got %d sidecars", len(noisyState.snap.Sidecars))
	}
	// Quiet sidecar has a recent event and should survive.
	if len(quietState.snap.Sidecars) != 1 {
		t.Fatalf("want 1 active sidecar for quiet project, got %d", len(quietState.snap.Sidecars))
	}
	if quietState.snap.Sidecars[0].ID != "quiet" {
		t.Errorf("want quiet sidecar, got %s", quietState.snap.Sidecars[0].ID)
	}
	if quietState.snap.Sidecars[0].LastActivity.IsZero() {
		t.Error("recent validate event lost — activity not annotated")
	}
}
