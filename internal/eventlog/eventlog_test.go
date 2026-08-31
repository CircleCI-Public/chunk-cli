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

func TestWrapForwardsAndLogs(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	var innerCalls []string
	inner := func(_ iostream.Level, msg string) {
		innerCalls = append(innerCalls, msg)
	}

	wrapped := l.Wrap(inner, OpExec, "sc-id", "my-sc", "main", nil)
	wrapped(iostream.LevelStep, "step1")
	wrapped(iostream.LevelDone, "done1")

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

func TestWrapNilInner(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := l.Wrap(nil, OpSync, "", "", "", nil)
	wrapped(iostream.LevelInfo, "should not panic")
}

func TestWrapRecordsCommandIDOnTerminalEvent(t *testing.T) {
	dir := t.TempDir()
	l, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}

	ids := &CommandIDs{}
	wrapped := l.Wrap(nil, OpValidate, "sc-id", "my-sc", "main", ids)

	// One command: its output line, the exec that reports an ID, then the
	// command's terminal event. A run-wide summary follows, belonging to no
	// single command.
	wrapped(iostream.LevelInfo, "$ go test ./...")
	ids.Set("cmd-abc123")
	wrapped(iostream.LevelError, "test  12s (remote)")
	wrapped(iostream.LevelError, "0/1 passed  12s")

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
		t.Errorf("summary event CommandID = %q, want empty", got[2].CommandID)
	}
}

func TestCommandIDsSetReplacesUnclaimedID(t *testing.T) {
	ids := &CommandIDs{}
	ids.Set("probe-id")
	ids.Set("cmd-id")
	if got := ids.Take(); got != "cmd-id" {
		t.Errorf("Take() = %q, want %q", got, "cmd-id")
	}
	if got := ids.Take(); got != "" {
		t.Errorf("second Take() = %q, want empty", got)
	}
}

func TestCommandIDsNilIsUsable(t *testing.T) {
	var ids *CommandIDs
	ids.Set("cmd-id")
	if got := ids.Take(); got != "" {
		t.Errorf("Take() on nil = %q, want empty", got)
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
