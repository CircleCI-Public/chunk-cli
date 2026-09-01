package watchd

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/assert/cmp"

	"github.com/CircleCI-Public/chunk-cli/internal/circleci"
)

// blockingStream returns a streamFn that emits chunks then blocks until release
// is closed, so a test can hold a command in the running state.
func blockingStream(chunks []string, release <-chan struct{}, exitCode int) streamFn {
	return func(ctx context.Context, _ string, onOutput func([]byte)) (*circleci.ExecResponse, error) {
		for _, c := range chunks {
			onOutput([]byte(c))
		}
		if release != nil {
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &circleci.ExecResponse{ExitCode: exitCode}, nil
	}
}

func immediateStream(chunks []string, exitCode int) streamFn {
	return blockingStream(chunks, nil, exitCode)
}

// waitForFinish polls until the command reports not-running, so tests do not
// race the streamer goroutine.
func waitForFinish(t *testing.T, s *outputStore, commandID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if chunk := s.read(commandID, 0); chunk.Found && !chunk.Running {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("command %s did not finish", commandID)
}

func reg(id, root string) CommandReg {
	return CommandReg{
		CommandID:   id,
		SidecarID:   "sc-1",
		ProjectRoot: root,
		Op:          "validate",
		SubmittedAt: time.Now(),
	}
}

func TestOutputStoreBuffersAndReportsExit(t *testing.T) {
	s := newOutputStore()
	s.register(reg("cmd-1", "/repo"), immediateStream([]string{"hello ", "world\n"}, 0))
	waitForFinish(t, s, "cmd-1")

	chunk := s.read("cmd-1", 0)
	assert.Assert(t, chunk.Found)
	assert.Check(t, cmp.Equal(string(chunk.Data), "hello world\n"))
	assert.Check(t, cmp.Equal(chunk.NextOffset, int64(12)))
	assert.Check(t, !chunk.Running)
	assert.Check(t, !chunk.Truncated)
	assert.Assert(t, chunk.ExitCode != nil)
	assert.Check(t, cmp.Equal(*chunk.ExitCode, 0))
}

func TestOutputStoreResumesFromOffset(t *testing.T) {
	s := newOutputStore()
	s.register(reg("cmd-1", "/repo"), immediateStream([]string{"abcdef"}, 0))
	waitForFinish(t, s, "cmd-1")

	chunk := s.read("cmd-1", 4)
	assert.Check(t, cmp.Equal(string(chunk.Data), "ef"))
	assert.Check(t, cmp.Equal(chunk.NextOffset, int64(6)))

	// A caller already at the end gets no data rather than a repeat, which is
	// the normal "nothing new" tick while tailing.
	current := s.read("cmd-1", 6)
	assert.Check(t, cmp.Len(current.Data, 0))
	assert.Check(t, cmp.Equal(current.NextOffset, int64(6)))
}

func TestOutputStoreUnknownCommandIsNotFound(t *testing.T) {
	s := newOutputStore()
	chunk := s.read("nope", 0)
	assert.Check(t, !chunk.Found)
}

func TestOutputStoreDuplicateRegistrationIgnored(t *testing.T) {
	s := newOutputStore()
	var calls int
	var mu sync.Mutex
	counting := func(ctx context.Context, _ string, onOutput func([]byte)) (*circleci.ExecResponse, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		onOutput([]byte("x"))
		return &circleci.ExecResponse{ExitCode: 0}, nil
	}
	s.register(reg("cmd-1", "/repo"), counting)
	waitForFinish(t, s, "cmd-1")
	s.register(reg("cmd-1", "/repo"), counting)

	mu.Lock()
	got := calls
	mu.Unlock()
	assert.Check(t, cmp.Equal(got, 1), "a duplicate registration must not start a second streamer")

	chunk := s.read("cmd-1", 0)
	assert.Check(t, cmp.Equal(string(chunk.Data), "x"))
}

func TestBufferKeepsTailAndReportsTruncation(t *testing.T) {
	b := newBuffer()
	// Two full buffers' worth, so the first half must be evicted.
	b.append([]byte(strings.Repeat("a", MaxCommandBytes)))
	b.append([]byte(strings.Repeat("b", 100)))

	chunk := b.read(0)
	assert.Check(t, chunk.Truncated, "reading from before the retained window must say so")
	assert.Check(t, cmp.Len(chunk.Data, MaxCommandBytes))

	// The tail is what survived: the end of a failed run is what anyone wants.
	data := string(chunk.Data)
	assert.Check(t, cmp.Equal(strings.HasSuffix(data, strings.Repeat("b", 100)), true))
	assert.Check(t, cmp.Equal(chunk.NextOffset, int64(MaxCommandBytes+100)))
}

func TestBufferNotTruncatedWhenUnderCap(t *testing.T) {
	b := newBuffer()
	b.append([]byte("small"))
	chunk := b.read(0)
	assert.Check(t, !chunk.Truncated)
	assert.Check(t, cmp.Equal(string(chunk.Data), "small"))
}

func TestOutputStoreEvictsOldestFinishedOnly(t *testing.T) {
	s := newOutputStore()

	// One long-running command registered first, then enough finished commands
	// to push the project over the cap.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s.register(reg("running", "/repo"), blockingStream([]string{"live"}, release, 0))

	for i := 0; i < MaxCommands+3; i++ {
		id := "done-" + strconv.Itoa(i)
		s.register(reg(id, "/repo"), immediateStream([]string{"x"}, 0))
		waitForFinish(t, s, id)
	}

	// The running command must survive: its streamer is still writing, and
	// evicting it would lose output that is still arriving.
	live := s.read("running", 0)
	assert.Check(t, live.Found, "a running command must never be evicted")
	assert.Check(t, live.Running)

	// The oldest finished ones are gone.
	gone := s.read("done-0", 0)
	assert.Check(t, !gone.Found, "oldest finished command should have been evicted")

	states := s.commandsFor("/repo")
	assert.Check(t, len(states) <= MaxCommands+1,
		"project should be capped (plus the un-evictable running command), got %d", len(states))
}

func TestOutputStoreRecordsCommandWithoutStreamer(t *testing.T) {
	s := newOutputStore()
	// nil streamFn is the unauthenticated case: the command is still recorded so
	// the dashboard can say it ran, but there is no output for it.
	s.register(reg("cmd-1", "/repo"), nil)

	chunk := s.read("cmd-1", 0)
	assert.Assert(t, chunk.Found)
	assert.Check(t, !chunk.Running)
	assert.Check(t, cmp.Len(chunk.Data, 0))

	states := s.commandsFor("/repo")
	assert.Check(t, cmp.Len(states, 1))
}

// A stream that fails must record why. Without it the reader sees an empty pane
// and no exit code, which is indistinguishable from a command that printed
// nothing — and sends them hunting a bug in their own command.
func TestOutputStoreRecordsStreamFailureReason(t *testing.T) {
	s := newOutputStore()
	failing := func(_ context.Context, _ string, onOutput func([]byte)) (*circleci.ExecResponse, error) {
		onOutput([]byte("partial output\n"))
		return nil, errors.New("400 Bad Request — Invalid command ID")
	}
	s.register(reg("cmd-1", "/repo"), failing)
	waitForFinish(t, s, "cmd-1")

	chunk := s.read("cmd-1", 0)
	assert.Assert(t, chunk.Found)
	assert.Check(t, cmp.Contains(chunk.Error, "Invalid command ID"))
	// Whatever did arrive is still served, and there is no exit code to claim.
	assert.Check(t, cmp.Equal(string(chunk.Data), "partial output\n"))
	assert.Check(t, cmp.Nil(chunk.ExitCode))
}

func TestOutputStoreNoCredentialsExplainsItself(t *testing.T) {
	s := newOutputStore()
	s.register(reg("cmd-1", "/repo"), nil)

	chunk := s.read("cmd-1", 0)
	assert.Assert(t, chunk.Found)
	assert.Check(t, cmp.Contains(chunk.Error, "credentials"))
}

func TestOutputStoreCleanExitHasNoError(t *testing.T) {
	s := newOutputStore()
	s.register(reg("cmd-1", "/repo"), immediateStream([]string{"fine\n"}, 0))
	waitForFinish(t, s, "cmd-1")

	chunk := s.read("cmd-1", 0)
	assert.Check(t, cmp.Equal(chunk.Error, ""), "a clean exit must not report an error")
}

func TestOutputStoreCommandsForIsPerProject(t *testing.T) {
	s := newOutputStore()
	s.register(reg("a", "/repo-one"), immediateStream([]string{"1"}, 0))
	s.register(reg("b", "/repo-two"), immediateStream([]string{"2"}, 0))
	waitForFinish(t, s, "a")
	waitForFinish(t, s, "b")

	one := s.commandsFor("/repo-one")
	assert.Assert(t, cmp.Len(one, 1))
	assert.Check(t, cmp.Equal(one[0].CommandID, "a"))

	two := s.commandsFor("/repo-two")
	assert.Assert(t, cmp.Len(two, 1))
	assert.Check(t, cmp.Equal(two[0].CommandID, "b"))

	none := s.commandsFor("/repo-three")
	assert.Check(t, cmp.Len(none, 0))
}

func TestOutputStoreStopAllCancelsStreamers(t *testing.T) {
	s := newOutputStore()
	release := make(chan struct{})
	defer close(release)
	s.register(reg("cmd-1", "/repo"), blockingStream([]string{"live"}, release, 0))

	// Wait for the streamer to have written before cancelling.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if chunk := s.read("cmd-1", 0); len(chunk.Data) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	s.stopAll()
	waitForFinish(t, s, "cmd-1")

	chunk := s.read("cmd-1", 0)
	assert.Check(t, !chunk.Running, "stopAll must terminate in-flight streamers")
}

// A partial eviction that then runs out of finished victims must still write the
// shortened index back, or the project keeps an evicted ID and a duplicated tail.
func TestOutputStoreEvictionWriteBackSurvivesEarlyExit(t *testing.T) {
	s := newOutputStore()
	block := make(chan struct{})
	defer close(block)
	runner := func(ctx context.Context, _ string, _ func([]byte)) (*circleci.ExecResponse, error) {
		<-block
		return &circleci.ExecResponse{}, nil
	}
	for i := 0; i < MaxCommands+1; i++ {
		s.register(CommandReg{CommandID: string(rune('a' + i)), ProjectRoot: "/p"}, runner)
	}
	s.cmds["a"].buf.finish(nil, "")
	s.register(CommandReg{CommandID: "z", ProjectRoot: "/p"}, runner)

	seen := map[string]int{}
	for _, cs := range s.commandsFor("/p") {
		seen[cs.CommandID]++
		if cs.CommandID == "a" {
			t.Error("evicted command still listed")
		}
	}
	for id, n := range seen {
		if n > 1 {
			t.Errorf("command %q listed %d times, want 1", id, n)
		}
	}
}
