package watch

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/CircleCI-Public/chunk-cli/internal/eventlog"
	"github.com/CircleCI-Public/chunk-cli/internal/watchd"
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

	if got := ago(now.Add(-5*time.Minute - 30*time.Second)); got != "5m ago" {
		t.Errorf("want '5m ago', got %q", got)
	}

	if got := ago(now.Add(-3*time.Hour - 30*time.Minute)); got != "3h ago" {
		t.Errorf("want '3h ago', got %q", got)
	}

	if got := ago(now.Add(-30 * time.Second)); !strings.HasSuffix(got, "s ago") {
		t.Errorf("seconds bucket: want 'Xs ago', got %q", got)
	}
}

// buildDataMsg tests

func TestBuildDataMsg_mapsProjectsInOrder(t *testing.T) {
	snap := watchd.Snapshot{
		Projects: []watchd.ProjectSnapshot{
			{Root: "/b", Branch: "main", HeadRef: "bbb"},
			{Root: "/a", Branch: "feat", HeadRef: "aaa"},
		},
	}
	entries := []ProjectEntry{{ProjectRoot: "/a"}, {ProjectRoot: "/b"}}

	msg := buildDataMsg(snap, entries)

	if msg.branches[0] != "feat" || msg.branches[1] != "main" {
		t.Errorf("branches not in entry order: %v", msg.branches)
	}
	if msg.headRefs[0] != "aaa" || msg.headRefs[1] != "bbb" {
		t.Errorf("headRefs not in entry order: %v", msg.headRefs)
	}
}

func TestBuildDataMsg_mapsSidecars(t *testing.T) {
	snap := watchd.Snapshot{
		Projects: []watchd.ProjectSnapshot{
			{
				Root: "/p",
				Sidecars: []watchd.SidecarState{
					{ID: "sc1", Name: "my-sc", InSync: true},
				},
			},
		},
	}
	entries := []ProjectEntry{{ProjectRoot: "/p"}}

	msg := buildDataMsg(snap, entries)

	if len(msg.sidecars) != 1 {
		t.Fatalf("want 1 sidecar, got %d", len(msg.sidecars))
	}
	sc := msg.sidecars[0]
	if sc.id != "sc1" || sc.name != "my-sc" || !sc.inSync {
		t.Errorf("sidecar not mapped correctly: %+v", sc)
	}
}

// Model update tests

func TestUpdate_selectionFollowsSidecarID(t *testing.T) {
	now := time.Now()
	m := New(nil)
	m.sidecars = []sidecarInfo{{id: "a", projectName: "p"}, {id: "b", projectName: "p"}}

	next, _ := m.Update(tea.KeyPressMsg{Code: 'j'})
	m = next.(Model)
	if m.selectedID != "b" {
		t.Fatalf("want selectedID b, got %q", m.selectedID)
	}

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

func TestUpdate_unknownSelectionFallsBackToFreshest(t *testing.T) {
	m := New(nil)
	m.selectedID = "gone"

	next, _ := m.Update(dataMsg{sidecars: []sidecarInfo{{id: "fresh"}, {id: "older"}}})
	m = next.(Model)

	if m.selectedIdx != 0 || m.selectedID != "fresh" {
		t.Errorf("want fallback to idx 0 (fresh), got idx %d id %q", m.selectedIdx, m.selectedID)
	}
}
