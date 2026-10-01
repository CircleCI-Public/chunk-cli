package eventlog

import (
	"fmt"
	"testing"
	"time"

	"github.com/CircleCI-Public/chunk-cli/internal/iostream"
)

func makeEvent(op Op, level, msg string) Event {
	return Event{Ts: time.Now(), Op: op, Level: level, Msg: msg}
}

func TestAppendAndRecent(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	for i := range 5 {
		if err := l.Append(makeEvent(OpValidate, "info", fmt.Sprintf("msg%d", i))); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	got, err := l.Recent(3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d", len(got))
	}
	if got[0].Msg != "msg2" || got[2].Msg != "msg4" {
		t.Errorf("unexpected tail: %v %v", got[0].Msg, got[2].Msg)
	}
}

func TestRecentEmptyLog(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := l.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 events, got %d", len(got))
	}
}

func TestRecentAll(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		if err := l.Append(makeEvent(OpSync, "info", fmt.Sprintf("m%d", i))); err != nil {
			t.Fatal(err)
		}
	}
	got, err := l.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want all 3, got %d", len(got))
	}
}

func TestTailFrom(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	if err := l.Append(makeEvent(OpSync, "info", "first")); err != nil {
		t.Fatal(err)
	}

	got, offset, err := l.TailFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Msg != "first" {
		t.Errorf("want [first], got %v", got)
	}
	if offset == 0 {
		t.Error("offset should advance past first event")
	}

	if err := l.Append(makeEvent(OpSync, "done", "second")); err != nil {
		t.Fatal(err)
	}

	got2, _, err := l.TailFrom(offset)
	if err != nil {
		t.Fatal(err)
	}
	if len(got2) != 1 || got2[0].Msg != "second" {
		t.Errorf("want [second], got %v", got2)
	}
}

func TestTailFromEmptyLog(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, offset, err := l.TailFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 || offset != 0 {
		t.Errorf("want empty/0, got len=%d offset=%d", len(got), offset)
	}
}

func TestTailFromNoNewEvents(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Append(makeEvent(OpSync, "info", "only")); err != nil {
		t.Fatal(err)
	}
	_, offset, err := l.TailFrom(0)
	if err != nil {
		t.Fatal(err)
	}
	// Tail again from the advanced offset — should return nothing.
	got, offset2, err := l.TailFrom(offset)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("want no new events, got %d", len(got))
	}
	if offset2 != offset {
		t.Errorf("offset should be unchanged: want %d, got %d", offset, offset2)
	}
}

func TestRecorderForwardsAndLogs(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	var innerCalls []string
	inner := func(_ iostream.Level, msg string) {
		innerCalls = append(innerCalls, msg)
	}

	rec := l.Recorder(inner, OpExec, "sc-id", "my-sc", "main")
	rec.Status(iostream.LevelStep, "step1")
	rec.Status(iostream.LevelDone, "done1")

	if len(innerCalls) != 2 {
		t.Fatalf("want 2 inner calls, got %d", len(innerCalls))
	}

	got, err := l.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d", len(got))
	}
	if got[0].Level != levelStep || got[0].SidecarID != "sc-id" || got[0].Op != OpExec {
		t.Errorf("unexpected event[0]: %+v", got[0])
	}
	if got[1].Level != levelDone || got[1].Msg != "done1" {
		t.Errorf("unexpected event[1]: %+v", got[1])
	}
}

func TestRecorderNilInner(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := l.Recorder(nil, OpSync, "", "", "")
	rec.Status(iostream.LevelInfo, "should not panic")
}

func TestSetCommandIDStampsTerminalEvent(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	rec := l.Recorder(nil, OpValidate, "sc-id", "my-sc", "main")

	rec.Status(iostream.LevelInfo, "$ go test ./...")
	rec.SetCommandID("cmd-abc123")
	rec.Status(iostream.LevelError, "test  12s (remote)") // per-command fail event
	rec.Status(iostream.LevelError, "0/1 passed  12s")    // run-wide summary, no command

	got, err := l.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d", len(got))
	}
	if got[0].CommandID != "" {
		t.Errorf("info event CommandID = %q, want empty", got[0].CommandID)
	}
	if got[1].CommandID != "cmd-abc123" {
		t.Errorf("terminal event CommandID = %q, want %q", got[1].CommandID, "cmd-abc123")
	}
	if got[2].CommandID != "" {
		t.Errorf("summary event CommandID = %q, want empty (ID already consumed)", got[2].CommandID)
	}
}

func TestSetCommandIDConsumedOnce(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	rec := l.Recorder(nil, OpValidate, "sc-id", "my-sc", "main")
	rec.SetCommandID("cmd-1")
	rec.Status(iostream.LevelDone, "test  3s (remote)")
	rec.Status(iostream.LevelDone, "lint  1s (remote)") // second command: no ID set yet

	got, err := l.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].CommandID != "cmd-1" {
		t.Errorf("first event CommandID = %q, want %q", got[0].CommandID, "cmd-1")
	}
	if got[1].CommandID != "" {
		t.Errorf("second event CommandID = %q, want empty", got[1].CommandID)
	}
}

func TestSetCommandIDNotOnFinalEvent(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	rec := l.Recorder(nil, OpValidate, "sc-id", "my-sc", "main")
	rec.SetCommandID("cmd-abc")
	rec.Final(iostream.LevelDone, "1/1 passed  3s", 1, 1)

	got, err := l.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].CommandID != "" {
		t.Errorf("Final event CommandID = %q, want empty", got[0].CommandID)
	}
}

func TestLevelStr(t *testing.T) {
	tests := []struct {
		in   iostream.Level
		want string
	}{
		{iostream.LevelStep, levelStep},
		{iostream.LevelInfo, levelInfo},
		{iostream.LevelWarn, levelWarn},
		{iostream.LevelDone, levelDone},
		{iostream.LevelError, levelError},
	}
	for _, tt := range tests {
		if got := levelStr(tt.in); got != tt.want {
			t.Errorf("levelStr(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestFinalRecordsOutcome(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := l.Recorder(nil, OpValidate, "sc-id", "my-sc", "main")
	rec.Status(iostream.LevelInfo, "$ task test")
	rec.Final(iostream.LevelError, "1/2 passed  3s", 1, 2)

	got, err := l.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, ok := got[0].Outcome(); ok {
		t.Error("status event should not close the operation")
	}
	passed, total, ok := got[1].Outcome()
	if !ok || passed != 1 || total != 2 {
		t.Errorf("Outcome() = (%d, %d, %v), want (1, 2, true)", passed, total, ok)
	}
}

// A Recorder with no log still reports, so a project without a data dir does
// not lose its status output.
func TestRecordWithoutLogStillReports(t *testing.T) {
	var got []string
	rec := Record("", func(_ iostream.Level, msg string) { got = append(got, msg) }, OpValidate, "", "", "")
	rec.Status(iostream.LevelInfo, "syncing")
	rec.Final(iostream.LevelDone, "1/1 passed", 1, 1)
	if len(got) != 2 {
		t.Fatalf("want 2 reported messages, got %d", len(got))
	}
}

func TestOutcomeLegacyMessages(t *testing.T) {
	cases := []struct {
		msg    string
		passed int
		total  int
		want   bool
	}{
		{"3/3 passed  5.2s", 3, 3, true},
		{"0/4 passed  13.7s", 0, 4, true},
		{"test    8.0s (remote)", 0, 0, false},
		{"lint    0.4s (local)", 0, 0, false},
		{"synced", 0, 0, false},
		{"", 0, 0, false},
		{"a/b passed", 0, 0, false},
	}
	for _, c := range cases {
		e := Event{Op: OpValidate, Level: levelDone, Msg: c.msg}
		passed, total, ok := e.Outcome()
		if ok != c.want || passed != c.passed || total != c.total {
			t.Errorf("Event{Msg: %q}.Outcome() = (%d, %d, %v), want (%d, %d, %v)", c.msg, passed, total, ok, c.passed, c.total, c.want)
		}
	}
}

func TestOutcomeLegacyIgnoresOtherOps(t *testing.T) {
	e := Event{Op: OpSync, Level: levelDone, Msg: "1/1 passed  1s"}
	if _, _, ok := e.Outcome(); ok {
		t.Error("a sync event should not close a validate run")
	}
}

func recentOrFail(t *testing.T, dir string, want int) []Event {
	t.Helper()
	log, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	events, err := log.Recent(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != want {
		t.Fatalf("got %d events, want %d", len(events), want)
	}
	return events
}

func TestRecorderForSharesLogAndInheritsRun(t *testing.T) {
	dir := t.TempDir()
	run := Record(dir, nil, OpHook, "sb-run", "primary", "feature")

	run.For("sb-1", "").Status(iostream.LevelStep, "$ go test ./...")
	run.For("sb-2", "").Final(iostream.LevelDone, "lint  1.0s", 1, 1)
	run.Final(iostream.LevelDone, "2/2 passed", 2, 2)

	// One log, or a sibling would trim the file out from under the others.
	events := recentOrFail(t, dir, 3)

	if events[0].SidecarID != "sb-1" {
		t.Errorf("got sidecar %q, want sb-1", events[0].SidecarID)
	}
	if events[0].Op != OpHook {
		t.Errorf("got op %q, want the run's own op", events[0].Op)
	}
	if events[0].Branch != "feature" {
		t.Errorf("got branch %q, want feature", events[0].Branch)
	}
	if events[0].SidecarName != "" {
		t.Errorf("got name %q, want the run's name left behind", events[0].SidecarName)
	}
	if events[1].SidecarID != "sb-2" {
		t.Errorf("got sidecar %q, want sb-2", events[1].SidecarID)
	}
	if events[2].SidecarID != "sb-run" || events[2].SidecarName != "primary" {
		t.Errorf("got %q/%q, want sb-run/primary", events[2].SidecarID, events[2].SidecarName)
	}
}

func TestRecorderPerCommandClosesEachRun(t *testing.T) {
	dir := t.TempDir()
	status := Record(dir, nil, OpValidate, "sb-1", "", "main").PerCommand()

	status(iostream.LevelInfo, "running on sidecar sb-1: test")
	status(iostream.LevelStep, "$ go test ./...")
	status(iostream.LevelDone, "test  18.4s")
	status(iostream.LevelStep, "$ golangci-lint run")
	status(iostream.LevelError, "lint  3.1s")

	events := recentOrFail(t, dir, 5)

	// Only a pass or a failure closes a run, so one sidecar's stream reads as a
	// sequence of single-command runs rather than one that never ends.
	var closed []int
	for i, e := range events {
		if _, _, ok := e.Outcome(); ok {
			closed = append(closed, i)
		}
	}
	if len(closed) != 2 || closed[0] != 2 || closed[1] != 4 {
		t.Fatalf("runs closed at %v, want the pass and the failure", closed)
	}
	if passed, total, _ := events[2].Outcome(); passed != 1 || total != 1 {
		t.Errorf("pass tallied %d/%d, want 1/1", passed, total)
	}
	if passed, total, _ := events[4].Outcome(); passed != 0 || total != 1 {
		t.Errorf("failure tallied %d/%d, want 0/1", passed, total)
	}
}

func TestRecorderPerCommandStampsCommandID(t *testing.T) {
	dir := t.TempDir()
	run := Record(dir, nil, OpValidate, "sb-run", "", "main")
	sibling := run.For("sb-1", "")
	status := sibling.PerCommand()

	// The ID belongs to the recorder whose command it names, not to its siblings.
	run.SetCommandID("cmd-run")
	sibling.SetCommandID("cmd-1")
	status(iostream.LevelStep, "$ go test ./...")
	status(iostream.LevelDone, "test  18.4s")
	status(iostream.LevelError, "lint  3.1s") // a second command, no ID set yet
	run.Status(iostream.LevelDone, "synced")

	events := recentOrFail(t, dir, 4)

	if events[1].CommandID != "cmd-1" {
		t.Errorf("pass carried command ID %q, want cmd-1", events[1].CommandID)
	}
	if _, _, ok := events[1].Outcome(); !ok {
		t.Error("the stamped pass should still close its run")
	}
	if events[2].CommandID != "" {
		t.Errorf("second command carried %q, want the ID consumed once", events[2].CommandID)
	}
	if events[3].CommandID != "cmd-run" {
		t.Errorf("run recorder event carried %q, want its own cmd-run", events[3].CommandID)
	}
}

func TestRecorderPerCommandForwardsEveryLevel(t *testing.T) {
	dir := t.TempDir()
	var reported []iostream.Level
	status := Record(dir, func(l iostream.Level, _ string) { reported = append(reported, l) }, OpValidate, "sb-1", "", "main").PerCommand()

	for _, l := range []iostream.Level{iostream.LevelStep, iostream.LevelInfo, iostream.LevelWarn, iostream.LevelDone, iostream.LevelError} {
		status(l, "msg")
	}

	if len(reported) != 5 {
		t.Errorf("reported %d of 5 levels", len(reported))
	}
	recentOrFail(t, dir, 5)
}
