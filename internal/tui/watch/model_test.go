package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
)

// groupEvents tests

func TestGroupEvents_empty(t *testing.T) {
	if groups := groupEvents(nil); len(groups) != 0 {
		t.Errorf("want 0 groups, got %d", len(groups))
	}
}

func TestGroupEvents_singleRunning(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpValidate, Level: "step"},
		{Op: eventlog.OpValidate, Level: "info"},
	}
	groups := groupEvents(events)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Errorf("want 2 events in group, got %d", len(groups[0]))
	}
}

func TestGroupEvents_terminatedByDone(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpSync, Level: "step"},
		{Op: eventlog.OpSync, Level: "done"},
		{Op: eventlog.OpSync, Level: "info"},
	}
	groups := groupEvents(events)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	if len(groups[0]) != 2 {
		t.Errorf("first group: want 2 events, got %d", len(groups[0]))
	}
	if len(groups[1]) != 1 {
		t.Errorf("second group: want 1 event, got %d", len(groups[1]))
	}
}

func TestGroupEvents_terminatedByError(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpValidate, Level: "step"},
		{Op: eventlog.OpValidate, Level: "error"},
	}
	groups := groupEvents(events)
	if len(groups) != 1 || len(groups[0]) != 2 {
		t.Fatalf("want 1 group of 2, got %d groups", len(groups))
	}
}

func TestGroupEvents_opChange(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpSync, Level: "info"},
		{Op: eventlog.OpValidate, Level: "info"},
	}
	groups := groupEvents(events)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	if groups[0][0].Op != eventlog.OpSync {
		t.Errorf("group 0 should be sync, got %q", groups[0][0].Op)
	}
	if groups[1][0].Op != eventlog.OpValidate {
		t.Errorf("group 1 should be validate, got %q", groups[1][0].Op)
	}
}

func TestGroupEvents_doneFollowedByNewOp(t *testing.T) {
	// done terminates the group, next event starts a fresh one even with same op.
	events := []eventlog.Event{
		{Op: eventlog.OpSync, Level: "step"},
		{Op: eventlog.OpSync, Level: "done"},
		{Op: eventlog.OpSync, Level: "step"},
		{Op: eventlog.OpSync, Level: "info"},
	}
	groups := groupEvents(events)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
	if len(groups[1]) != 2 {
		t.Errorf("second run: want 2 events, got %d", len(groups[1]))
	}
}

// groupByInvocation / isSummaryEvent tests

func TestGroupByInvocation_singleRun(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpSync, Level: "done", Msg: "synced"},
		{Op: eventlog.OpValidate, Level: "done", Msg: "test    1.0s (remote)"},
		{Op: eventlog.OpValidate, Level: "done", Msg: "lint    0.2s (remote)"},
		{Op: eventlog.OpValidate, Level: "done", Msg: "2/2 passed  1.5s"},
	}
	groups := groupByInvocation(events)
	if len(groups) != 1 {
		t.Fatalf("want 1 group, got %d", len(groups))
	}
	if len(groups[0].events) != 4 {
		t.Errorf("want 4 events in group, got %d", len(groups[0].events))
	}
}

func TestGroupByInvocation_twoRuns(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpSync, Level: "done", Msg: "synced"},
		{Op: eventlog.OpValidate, Level: "done", Msg: "test    1.0s (remote)"},
		{Op: eventlog.OpValidate, Level: "error", Msg: "0/1 passed  1.0s"},
		// second run
		{Op: eventlog.OpSync, Level: "done", Msg: "synced"},
		{Op: eventlog.OpValidate, Level: "done", Msg: "test    0.8s (remote)"},
		{Op: eventlog.OpValidate, Level: "done", Msg: "1/1 passed  0.9s"},
	}
	groups := groupByInvocation(events)
	if len(groups) != 2 {
		t.Fatalf("want 2 groups, got %d", len(groups))
	}
}

func TestGroupByInvocation_inProgress(t *testing.T) {
	events := []eventlog.Event{
		{Op: eventlog.OpSync, Level: "done", Msg: "synced"},
		{Op: eventlog.OpValidate, Level: "step", Msg: "$ task test"},
	}
	groups := groupByInvocation(events)
	if len(groups) != 1 {
		t.Fatalf("want 1 in-progress group, got %d", len(groups))
	}
}

func TestIsSummaryEvent(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"3/3 passed  5.2s", true},
		{"0/4 passed  13.7s", true},
		{"test    8.0s (remote)", false},
		{"lint    0.4s (local)", false},
		{"synced", false},
		{"", false},
	}
	for _, c := range cases {
		e := eventlog.Event{Op: eventlog.OpValidate, Level: "done", Msg: c.msg}
		if got := isSummaryEvent(e); got != c.want {
			t.Errorf("isSummaryEvent(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

// stripUUIDSuffix tests

func TestStripUUIDSuffix(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"chunk-cli-2d66488f-e67f-4c3a-9abc-112233445566", "chunk-cli"},
		{"abc-deadbeef-rest", "abc"},
		{"no-uuid-here", "no-uuid-here"},
		{"", ""},
		// Leading dash: i=0 fails the i>0 guard.
		{"-2d66488f-rest", "-2d66488f-rest"},
		// 'g' is not valid hex.
		{"abc-2d66488g-rest", "abc-2d66488g-rest"},
		{"plain", "plain"},
	}
	for _, tt := range tests {
		if got := stripUUIDSuffix(tt.in); got != tt.want {
			t.Errorf("stripUUIDSuffix(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// truncate tests

func TestTruncate(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"hello", 10, "hello"},
		{"hello", 5, "hello"},
		{"hello", 4, "hel…"},
		{"hello", 1, "h"},
		{"", 5, ""},
		{"héllo", 3, "hé…"},
	}
	for _, tt := range tests {
		if got := truncate(tt.in, tt.n); got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

// padLine tests

func TestPadLine_padsShortString(t *testing.T) {
	got := padLine("hi", 5)
	// lipgloss.Width("hi") == 2; expect 3 spaces appended.
	if got != "hi   " {
		t.Errorf("want %q, got %q", "hi   ", got)
	}
}

func TestPadLine_alreadyAtWidth(t *testing.T) {
	got := padLine("hello", 5)
	if got != "hello" {
		t.Errorf("want %q, got %q", "hello", got)
	}
}

// ago tests

func TestAgo(t *testing.T) {
	now := time.Now()

	// Minutes bucket: clearly > 1 min, < 1 hour.
	if got := ago(now.Add(-5*time.Minute - 30*time.Second)); got != "5m ago" {
		t.Errorf("want '5m ago', got %q", got)
	}

	// Hours bucket.
	if got := ago(now.Add(-3*time.Hour - 30*time.Minute)); got != "3h ago" {
		t.Errorf("want '3h ago', got %q", got)
	}

	// Seconds bucket: verify format without asserting exact count.
	if got := ago(now.Add(-30 * time.Second)); !strings.HasSuffix(got, "s ago") {
		t.Errorf("seconds bucket: want 'Xs ago', got %q", got)
	}
}

// loadSidecars tests

func writeSidecarJSON(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSidecars_deduplicatesIDs(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1","last_synced_ref":"abc123"}`)
	writeSidecarJSON(t, dir, "sidecar.sess1.json", `{"sidecar_id":"id2","name":"sc2"}`)
	// Duplicate id1 — should be deduplicated.
	writeSidecarJSON(t, dir, "sidecar.sess2.json", `{"sidecar_id":"id1","name":"sc1"}`)

	result := loadSidecars(dir, root, "", "abc123", 0, "root", "main")
	if len(result) != 2 {
		t.Fatalf("want 2 unique sidecars, got %d", len(result))
	}
}

func TestLoadSidecars_inSyncWhenHeadMatches(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1","last_synced_ref":"abc123"}`)

	result := loadSidecars(dir, root, "", "abc123", 0, "root", "main")
	if len(result) != 1 {
		t.Fatalf("want 1, got %d", len(result))
	}
	if !result[0].inSync {
		t.Error("sidecar should be in sync when lastSyncedRef matches head")
	}
}

func TestLoadSidecars_notInSyncWhenHeadDiffers(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","last_synced_ref":"oldref"}`)

	result := loadSidecars(dir, root, "", "newref", 0, "root", "main")
	if len(result) != 1 {
		t.Fatalf("want 1, got %d", len(result))
	}
	if result[0].inSync {
		t.Error("sidecar should not be in sync when refs differ")
	}
}

func TestLoadSidecars_emptyDir(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()
	if result := loadSidecars(dir, root, "", "", 0, "root", "main"); len(result) != 0 {
		t.Errorf("want 0, got %d", len(result))
	}
}

func TestLoadSidecars_skipsEmptySidecarID(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"","name":"empty"}`)

	if result := loadSidecars(dir, root, "", "", 0, "root", "main"); len(result) != 0 {
		t.Errorf("want 0 (skipped empty ID), got %d", len(result))
	}
}

func TestLoadSidecars_snapshotName(t *testing.T) {
	dir := t.TempDir()
	root := t.TempDir()

	writeSidecarJSON(t, dir, "sidecar.json", `{"sidecar_id":"id1","name":"sc1"}`)

	result := loadSidecars(dir, root, "my-snap", "", 0, "root", "main")
	if len(result) != 1 {
		t.Fatalf("want 1, got %d", len(result))
	}
	if result[0].snapshotName != "my-snap" {
		t.Errorf("snapshotName should be 'my-snap', got %q", result[0].snapshotName)
	}
}

// recency ordering and per-project event caps

func TestSortByActivity_freshestProjectFirst(t *testing.T) {
	now := time.Now()
	sidecars := []sidecarInfo{
		{id: "stale", repoName: "old-project", fileMtime: now.Add(-6 * time.Hour)},
		{id: "older", repoName: "busy-project", lastActivity: now.Add(-30 * time.Minute)},
		{id: "newest", repoName: "busy-project", lastActivity: now.Add(-1 * time.Minute)},
	}

	sortByActivity(sidecars)

	want := []string{"newest", "older", "stale"}
	for i, id := range want {
		if sidecars[i].id != id {
			t.Errorf("position %d: want %s, got %s", i, id, sidecars[i].id)
		}
	}
}

func TestSortByActivity_keepsProjectsGrouped(t *testing.T) {
	now := time.Now()
	sidecars := []sidecarInfo{
		{id: "a1", repoName: "a", lastActivity: now.Add(-2 * time.Minute)},
		{id: "b1", repoName: "b", lastActivity: now.Add(-1 * time.Minute)},
		{id: "a2", repoName: "a", lastActivity: now.Add(-3 * time.Minute)},
	}

	sortByActivity(sidecars)

	// Project b is freshest so it leads, then both of a's sidecars together.
	want := []string{"b1", "a1", "a2"}
	for i, id := range want {
		if sidecars[i].id != id {
			t.Errorf("position %d: want %s, got %s", i, id, sidecars[i].id)
		}
	}
}

func TestFilterSidecars_noPerProjectCap(t *testing.T) {
	now := time.Now()
	sidecars := []sidecarInfo{
		{id: "s1", projectName: "p", lastActivity: now.Add(-1 * time.Minute)},
		{id: "s2", projectName: "p", lastActivity: now.Add(-2 * time.Minute)},
		{id: "s3", projectName: "p", lastActivity: now.Add(-3 * time.Minute)},
		{id: "s4", projectName: "p", lastActivity: now.Add(-4 * time.Minute)},
	}

	sortByActivity(sidecars)
	got := filterSidecars(sidecars, 2)

	if len(got) != 4 {
		t.Fatalf("want all 4 sidecars, got %d", len(got))
	}
	for i, id := range []string{"s1", "s2", "s3", "s4"} {
		if got[i].id != id {
			t.Errorf("position %d: want %s, got %s", i, id, got[i].id)
		}
	}
}

func TestFilterSidecars_dropsInactive(t *testing.T) {
	now := time.Now()
	sidecars := []sidecarInfo{
		{id: "recent", projectName: "p", lastActivity: now.Add(-59 * time.Minute)},
		{id: "just-aged-out", projectName: "p", lastActivity: now.Add(-61 * time.Minute)},
		{id: "mtime-recent", projectName: "q", fileMtime: now.Add(-10 * time.Minute)},
		{id: "mtime-old", projectName: "q", fileMtime: now.Add(-25 * time.Hour)},
		{id: "no-activity-at-all", projectName: "r"},
	}

	got := filterSidecars(sidecars, 10)

	want := []string{"recent", "mtime-recent"}
	if len(got) != len(want) {
		t.Fatalf("want %d sidecars, got %d", len(want), len(got))
	}
	for i, id := range want {
		if got[i].id != id {
			t.Errorf("position %d: want %s, got %s", i, id, got[i].id)
		}
	}
}

func TestFilterSidecars_fallsBackToPaneFullOfRecent(t *testing.T) {
	now := time.Now()
	// Nothing inside the hour, so the fallback fills the pane by recency.
	sidecars := []sidecarInfo{
		{id: "h2", projectName: "a", lastActivity: now.Add(-2 * time.Hour)},
		{id: "h3", projectName: "b", lastActivity: now.Add(-3 * time.Hour)},
		{id: "h4", projectName: "c", lastActivity: now.Add(-4 * time.Hour)},
		{id: "yesterday", projectName: "d", lastActivity: now.Add(-25 * time.Hour)},
	}

	got := filterSidecars(sidecars, 2)

	if len(got) != 2 {
		t.Fatalf("want 2 sidecars (pane capacity), got %d", len(got))
	}
	for i, id := range []string{"h2", "h3"} {
		if got[i].id != id {
			t.Errorf("position %d: want %s, got %s", i, id, got[i].id)
		}
	}
}

func TestFilterSidecars_fallbackStopsAtOneDay(t *testing.T) {
	now := time.Now()
	sidecars := []sidecarInfo{
		{id: "just-inside", projectName: "a", lastActivity: now.Add(-23 * time.Hour)},
		{id: "too-old", projectName: "b", lastActivity: now.Add(-25 * time.Hour)},
	}

	got := filterSidecars(sidecars, 10)

	if len(got) != 1 || got[0].id != "just-inside" {
		t.Fatalf("want only just-inside, got %v", got)
	}
}

func TestFilterSidecars_activeSetIgnoresCapacity(t *testing.T) {
	now := time.Now()
	var sidecars []sidecarInfo
	for i := range 8 {
		sidecars = append(sidecars, sidecarInfo{
			id: fmt.Sprintf("s%d", i), projectName: "p",
			lastActivity: now.Add(-time.Duration(i) * time.Minute),
		})
	}

	// All eight are inside the hour, so none are dropped to fit the pane.
	if got := filterSidecars(sidecars, 2); len(got) != 8 {
		t.Fatalf("want all 8 active sidecars, got %d", len(got))
	}
}

func TestSidecarCapacity(t *testing.T) {
	tests := []struct {
		name   string
		height int
		want   int
	}{
		{"unset height", 0, defaultCapacity},
		{"tiny terminal", 8, 1},
		{"40 rows", 40, 6},
		{"80 rows", 80, 14},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(nil)
			m.height = tt.height
			if got := m.sidecarCapacity(); got != tt.want {
				t.Errorf("height %d: want %d, got %d", tt.height, tt.want, got)
			}
		})
	}
}

func TestCapEvents_capsPerProject(t *testing.T) {
	prior := make([]eventlog.Event, recentEvents)
	for i := range prior {
		prior[i] = eventlog.Event{SidecarID: "old", Msg: "old"}
	}
	fresh := []eventlog.Event{{SidecarID: "new", Msg: "new"}}

	got := capEvents(prior, fresh)

	if len(got) != recentEvents {
		t.Fatalf("want %d events, got %d", recentEvents, len(got))
	}
	if got[len(got)-1].SidecarID != "new" {
		t.Errorf("newest event should survive the cap, got %s", got[len(got)-1].SidecarID)
	}
}

func TestLoadData_busyProjectDoesNotEvictRecentValidate(t *testing.T) {
	noisyDir, quietDir := t.TempDir(), t.TempDir()
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

	// The noisy project alone exceeds the event cap.
	for i := 0; i < recentEvents+50; i++ {
		if err := noisyLog.Append(eventlog.Event{
			Ts: time.Now().Add(-2 * time.Hour), SidecarID: "noisy", Op: eventlog.OpValidate, Level: "info", Msg: "noise",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := quietLog.Append(eventlog.Event{
		Ts: time.Now(), SidecarID: "quiet", Op: eventlog.OpValidate, Level: "done", Msg: "validate passed",
	}); err != nil {
		t.Fatal(err)
	}

	m := New([]ProjectEntry{
		{Log: noisyLog, DataDir: noisyDir, ProjectRoot: filepath.Join(noisyDir, "noisy-project")},
		{Log: quietLog, DataDir: quietDir, ProjectRoot: filepath.Join(quietDir, "quiet-project")},
	})

	msg, ok := m.loadData().(dataMsg)
	if !ok {
		t.Fatalf("want dataMsg, got %T", msg)
	}
	// The quiet project just validated, so only it is inside activeWindow; the
	// noisy sidecar's events are 2h old and the fallback does not apply.
	if len(msg.sidecars) != 1 {
		t.Fatalf("want 1 active sidecar, got %d", len(msg.sidecars))
	}
	if msg.sidecars[0].id != "quiet" {
		t.Errorf("want quiet sidecar, got %s", msg.sidecars[0].id)
	}
	if msg.sidecars[0].lastActivity.IsZero() {
		t.Error("recent validate lost its activity to the other project's history")
	}
}

func TestUpdate_selectionFollowsSidecarID(t *testing.T) {
	now := time.Now()
	m := New(nil)
	m.sidecars = []sidecarInfo{{id: "a", projectName: "p"}, {id: "b", projectName: "p"}}

	// Select the second sidecar.
	next, _ := m.Update(tea.KeyPressMsg{Code: 's'})
	m = next.(Model)
	if m.selectedID != "b" {
		t.Fatalf("want selectedID b, got %q", m.selectedID)
	}

	// A poll reorders the list; the selection should follow the id, not the index.
	next, _ = m.Update(dataMsg{
		sidecars: []sidecarInfo{
			{id: "c", projectName: "p", lastActivity: now},
			{id: "b", projectName: "p", lastActivity: now.Add(-time.Minute)},
			{id: "a", projectName: "p", lastActivity: now.Add(-2 * time.Minute)},
		},
	})
	m = next.(Model)
	if m.selectedIdx != 1 || m.selectedID != "b" {
		t.Errorf("want selection to stay on b (idx 1), got idx %d id %q", m.selectedIdx, m.selectedID)
	}
}

func TestUpdate_initialSelectionPicksMostRecent(t *testing.T) {
	// On first entry, New() has no selection. Even when a local runner (id="")
	// is in the list with older activity, the freshest sidecar must be selected.
	now := time.Now()
	m := New(nil)

	next, _ := m.Update(dataMsg{sidecars: []sidecarInfo{
		{id: "newest", lastActivity: now.Add(-1 * time.Minute)},
		{id: "older", lastActivity: now.Add(-30 * time.Minute)},
		{id: "", name: "local", lastActivity: now.Add(-45 * time.Minute)},
	}})
	m = next.(Model)

	if m.selectedIdx != 0 || m.selectedID != "newest" {
		t.Errorf("want initial selection at idx 0 (newest), got idx %d id %q", m.selectedIdx, m.selectedID)
	}
}

func TestUpdate_unknownSelectionFallsBackToFreshest(t *testing.T) {
	m := New(nil)
	m.selectedID = "gone"

	next, _ := m.Update(dataMsg{sidecars: []sidecarInfo{{id: "fresh"}, {id: "older"}}})
	m = next.(Model)

	if m.selectedIdx != 0 || m.selectedID != "fresh" {
		t.Errorf("want fallback to idx 0 (fresh), got idx %d id %q", m.selectedIdx, m.selectedID)
	}
}
