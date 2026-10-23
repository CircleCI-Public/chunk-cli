package factory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"gotest.tools/v3/assert"
	"gotest.tools/v3/poll"
)

// fakeFactory puts a factory on PATH, so StartAgent's check that factory is
// installed passes without a real factory daemon. `factory daemon claim list`
// prints $FAKE_CLAIMS, or fails with $FAKE_CLAIMS_ERR when that is set. It
// also fails unless told not to retry while the daemon isn't running, as a
// real factory would for a minute.
func fakeFactory(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	script := `#!/bin/sh
if [ "$1 $2 $3" = "daemon claim list" ]; then
  if [ "$FACTORY_DAEMON_NOT_RUNNING_RETRY" != "0s" ]; then echo "would retry for a minute" >&2; exit 1; fi
  if [ -n "$FAKE_CLAIMS_ERR" ]; then echo "$FAKE_CLAIMS_ERR" >&2; exit 1; fi
  printf '%s' "$FAKE_CLAIMS"
fi
exit 0
`
	err := os.WriteFile(filepath.Join(dir, "factory"), []byte(script), 0o755)
	assert.NilError(t, err)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// agentFakeEnv points the agentFakeScript's files at a fresh temp directory and
// gives it a successful init to report, which tests override to fail init
// checks. It returns the paths the script logs to.
func agentFakeEnv(t *testing.T) (argsFile, sigsFile, stdinLog string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	sigsFile = filepath.Join(dir, "sigs")
	stdinLog = filepath.Join(dir, "stdin.log")
	t.Setenv("ARGS", argsFile)
	t.Setenv("SIGS", sigsFile)
	t.Setenv("STDIN_LOG", stdinLog)
	t.Setenv("FAKE_PLUGINS", `[{"name":"f"}]`)
	t.Setenv("FAKE_MCP", `[{"name":"plugin:f:factory","status":"connected"}]`)
	return argsFile, sigsFile, stdinLog
}

// agentFakeScript is a long-lived fake claude -p --input-format stream-json
// session. It logs every line sent to it in $STDIN_LOG and answers each with a
// turn, except for a few sentinel substrings. As Claude Code 2.1.309 does, it
// reports init only once a message arrives, and again on every turn.
const agentFakeScript = `
echo "$@" > "$ARGS"
echo "hooks=$CHUNK_HOOKS_DISABLED" >> "$ARGS"
pwd >> "$ARGS"
trap 'echo INT >> "$SIGS"; [ -n "$FAKE_IGNORE_INT" ] || exit 130' INT
trap 'echo TERM >> "$SIGS"; exit 143' TERM
init() {
  echo "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s1\",\"plugins\":$FAKE_PLUGINS,\"mcp_servers\":$FAKE_MCP}"
}
readloop() {
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$STDIN_LOG"
  init
  case "$line" in
    *FLOOD*)
      i=0
      while [ $i -lt 300 ]; do
        echo '{"type":"assistant","message":{"content":[{"type":"text","text":"working"}]}}'
        i=$((i+1))
      done
      ;;
  esac
  echo '{"type":"assistant","message":{"content":[{"type":"text","text":"ok"}]}}'
  echo '{"type":"result","subtype":"success","result":"reply","total_cost_usd":0.01,"session_id":"s1"}'
  case "$line" in
    *START_BG*)
      # A background task whose notification, once it ends, starts a turn
      # nobody sent a message for.
      echo '{"type":"system","subtype":"task_started","task_id":"b1","tool_use_id":"tb1","task_type":"local_bash"}'
      ( sleep 1
        echo '{"type":"system","subtype":"task_notification","task_id":"b1","tool_use_id":"tb1","status":"completed"}'
        init
        echo '{"type":"assistant","message":{"content":[{"type":"text","text":"task"}]}}'
        echo '{"type":"result","subtype":"success","result":"did task","session_id":"s1"}'
      ) &
      ;;
  esac
done
}
readloop
if [ -n "$FAKE_IGNORE_INT" ]; then
  # Outlive stdin's EOF, since Stop closes it right after sending SIGINT: only
  # a signal this fake does not ignore should end it. Redirected away from
  # this process's own stdout/stderr, so the background sleep never holds
  # those pipes open past this shell's own exit.
  sleep 3600 >/dev/null 2>&1 &
  wait
fi
`

// setVar sets *v to val for the duration of the test.
func setVar[T any](t *testing.T, v *T, val T) {
	t.Helper()
	orig := *v
	*v = val
	t.Cleanup(func() { *v = orig })
}

// collect drains a's events until until reports true for one of them, or the
// channel closes, or timeout passes.
func collect(t *testing.T, a *Agent, until func(AgentEvent) bool, timeout time.Duration) []AgentEvent {
	t.Helper()
	var got []AgentEvent
	deadline := time.After(timeout)
	for {
		select {
		case ev, ok := <-a.Events():
			if !ok {
				return got
			}
			got = append(got, ev)
			if until != nil && until(ev) {
				return got
			}
		case <-deadline:
			t.Fatalf("timed out waiting for events; got %+v", got)
		}
	}
}

func isProblem(ev AgentEvent) bool { return ev.Problem != nil }

func nthTurnEnded(n int) func(AgentEvent) bool {
	seen := 0
	return func(ev AgentEvent) bool {
		if ev.TurnEnded == nil {
			return false
		}
		seen++
		return seen >= n
	}
}

func problems(events []AgentEvent) []error {
	var out []error
	for _, ev := range events {
		if ev.Problem != nil {
			out = append(out, ev.Problem)
		}
	}
	return out
}

func turns(events []AgentEvent) []*AgentTurn {
	var out []*AgentTurn
	for _, ev := range events {
		if ev.TurnEnded != nil {
			out = append(out, ev.TurnEnded)
		}
	}
	return out
}

// stdinLines is every message the fake was sent, decoded.
func stdinLines(t *testing.T) []string {
	t.Helper()
	path := os.Getenv("STDIN_LOG")
	raw := strings.TrimRight(readFile(t, filepath.Dir(path), filepath.Base(path)), "\n")
	var out []string
	for _, l := range strings.Split(raw, "\n") {
		var msg stdinMessage
		assert.NilError(t, json.Unmarshal([]byte(l), &msg))
		out = append(out, msg.Message.Content)
	}
	return out
}

func startTestAgent(ctx context.Context, t *testing.T, cfg AgentConfig, first string) *Agent {
	t.Helper()
	if cfg.Dir == "" {
		cfg.Dir = t.TempDir()
	}
	if cfg.SessionID == "" {
		cfg.SessionID = "s1"
	}
	a, err := StartAgent(ctx, cfg, first)
	assert.NilError(t, err)
	t.Cleanup(func() { _ = a.Stop() })
	return a
}

func TestAgentArgs(t *testing.T) {
	agentFakeEnv(t)
	fakeClaude(t, agentFakeScript)
	fakeFactory(t)
	dir := t.TempDir()

	startTestAgent(context.Background(), t, AgentConfig{
		Dir: dir, SessionID: "s-123", PluginDir: "/plugins", Model: "sonnet",
	}, "hello")

	gotArgs := readFile(t, filepath.Dir(os.Getenv("ARGS")), "args")
	assert.Assert(t, strings.Contains(gotArgs, "--session-id s-123"), gotArgs)
	assert.Assert(t, !strings.Contains(gotArgs, "--resume"), gotArgs)
	assert.Assert(t, strings.Contains(gotArgs, "--allowedTools Read,Grep,Glob,Bash(factory daemon wait),mcp__plugin_f_factory"), gotArgs)
	assert.Assert(t, strings.Contains(gotArgs, "--plugin-dir /plugins"), gotArgs)
	assert.Assert(t, strings.Contains(gotArgs, "--model sonnet"), gotArgs)
	assert.Assert(t, strings.Contains(gotArgs, "--input-format stream-json"), gotArgs)
	// Planning only: no edits to the working tree, whatever the developer's
	// own settings allow.
	assert.Assert(t, strings.Contains(gotArgs, "--permission-mode default"), gotArgs)
	assert.Assert(t, strings.Contains(gotArgs, "--disallowedTools Edit,Write,NotebookEdit"), gotArgs)
	assert.Assert(t, !strings.Contains(gotArgs, "acceptEdits"), gotArgs)
	assert.Assert(t, strings.Contains(gotArgs, "hooks=1"), gotArgs)
	assert.Assert(t, strings.Contains(gotArgs, dir), gotArgs)
}

func TestAgentArgsResume(t *testing.T) {
	agentFakeEnv(t)
	fakeClaude(t, agentFakeScript)
	fakeFactory(t)

	startTestAgent(context.Background(), t, AgentConfig{SessionID: "s-456", Resume: true}, "hello")

	gotArgs := readFile(t, filepath.Dir(os.Getenv("ARGS")), "args")
	assert.Assert(t, strings.Contains(gotArgs, "--resume s-456"), gotArgs)
	assert.Assert(t, !strings.Contains(gotArgs, "--session-id"), gotArgs)
}

// claude reports init only once it has a message, so a session can't start
// without one.
func TestAgentNeedsAFirstMessage(t *testing.T) {
	argsFile, _, _ := agentFakeEnv(t)
	fakeClaude(t, agentFakeScript)
	fakeFactory(t)

	_, err := StartAgent(context.Background(), AgentConfig{SessionID: "s1"}, "")
	assert.ErrorContains(t, err, "first message")
	_, statErr := os.Stat(argsFile)
	assert.Assert(t, os.IsNotExist(statErr), "claude should never have run")
}

func TestAgentSend(t *testing.T) {
	agentFakeEnv(t)
	fakeClaude(t, agentFakeScript)
	fakeFactory(t)
	a := startTestAgent(context.Background(), t, AgentConfig{}, "first")

	assert.NilError(t, a.Send("say \"hi\"\nthere"))
	collect(t, a, nthTurnEnded(2), 5*time.Second)

	assert.DeepEqual(t, stdinLines(t), []string{"first", "say \"hi\"\nthere"})
}

func TestAgentByTask(t *testing.T) {
	agentFakeEnv(t)
	fakeClaude(t, agentFakeScript)
	fakeFactory(t)
	a := startTestAgent(context.Background(), t, AgentConfig{}, "START_BG")

	got := turns(collect(t, a, nthTurnEnded(2), 5*time.Second))
	assert.Equal(t, len(got), 2, "%+v", got)
	assert.Equal(t, got[0].Reply, "reply")
	assert.Assert(t, !got[0].ByTask)
	assert.Equal(t, got[1].Reply, "did task")
	assert.Assert(t, got[1].ByTask)
}

func TestAgentInitErrors(t *testing.T) {
	for _, tc := range []struct {
		name         string
		plugins, mcp string
		wantErr      error
	}{
		{name: "no plugin", plugins: `[]`, wantErr: ErrNoFactoryPlugin},
		{name: "mcp not connected", mcp: `[{"name":"plugin:f:factory","status":"failed"}]`, wantErr: ErrFactoryMCP},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentFakeEnv(t)
			if tc.plugins != "" {
				t.Setenv("FAKE_PLUGINS", tc.plugins)
			}
			if tc.mcp != "" {
				t.Setenv("FAKE_MCP", tc.mcp)
			}
			fakeClaude(t, agentFakeScript)
			fakeFactory(t)
			setVar(t, &stopInterruptWait, 200*time.Millisecond)
			setVar(t, &stopTerminateWait, 200*time.Millisecond)

			_, err := StartAgent(context.Background(), AgentConfig{SessionID: "s1"}, "hello")
			assert.Assert(t, errors.Is(err, tc.wantErr), "got %v", err)
		})
	}
}

func TestAgentNoFactoryOnPath(t *testing.T) {
	argsFile, _, _ := agentFakeEnv(t)
	// No fakeFactory, and nothing else: only claude is on PATH, not even a
	// factory installed on the machine running the tests.
	t.Setenv("PATH", t.TempDir())
	fakeClaude(t, agentFakeScript)

	_, err := StartAgent(context.Background(), AgentConfig{SessionID: "s1"}, "hello")
	assert.Assert(t, errors.Is(err, ErrNoFactory), "got %v", err)
	_, statErr := os.Stat(argsFile)
	assert.Assert(t, os.IsNotExist(statErr), "claude should never have run")
}

func TestAgentStop(t *testing.T) {
	_, sigsFile, _ := agentFakeEnv(t)
	fakeClaude(t, agentFakeScript)
	fakeFactory(t)
	a, err := StartAgent(context.Background(), AgentConfig{SessionID: "s1"}, "hello")
	assert.NilError(t, err)

	assert.NilError(t, a.Stop())
	assert.Equal(t, strings.TrimSpace(readFile(t, filepath.Dir(sigsFile), filepath.Base(sigsFile))), "INT")

	collect(t, a, nil, 5*time.Second) // drains until the channel closes

	// A second Stop returns the same result at once.
	assert.NilError(t, a.Stop())
}

func TestAgentStopEscalatesWhenIntIsIgnored(t *testing.T) {
	_, sigsFile, _ := agentFakeEnv(t)
	t.Setenv("FAKE_IGNORE_INT", "1")
	fakeClaude(t, agentFakeScript)
	fakeFactory(t)
	setVar(t, &stopInterruptWait, 200*time.Millisecond)
	a, err := StartAgent(context.Background(), AgentConfig{SessionID: "s1"}, "hello")
	assert.NilError(t, err)

	assert.NilError(t, a.Stop())
	assert.Equal(t, strings.TrimSpace(readFile(t, filepath.Dir(sigsFile), filepath.Base(sigsFile))), "INT\nTERM")
}

// Cancelling the context stops claude with one SIGINT: exec must not send its
// own on top of Stop's.
func TestAgentStopOnContextCancel(t *testing.T) {
	_, sigsFile, _ := agentFakeEnv(t)
	fakeClaude(t, agentFakeScript)
	fakeFactory(t)
	ctx, cancel := context.WithCancel(context.Background())
	// startTestAgent registers a Cleanup that calls a.Stop(): since Stop runs
	// under a sync.Once, that call blocks until the context.AfterFunc-driven
	// Stop below has fully finished, however it is interleaved. Without that,
	// this test could return while that goroutine is still mid-Stop, racing
	// on the package vars other tests shorten with setVar.
	a := startTestAgent(ctx, t, AgentConfig{SessionID: "s1"}, "hello")

	cancel()
	collect(t, a, nil, 5*time.Second)
	assert.Equal(t, strings.TrimSpace(readFile(t, filepath.Dir(sigsFile), filepath.Base(sigsFile))), "INT")
}

func TestAgentUnexpectedExit(t *testing.T) {
	fakeClaude(t, `
read -r line
echo '{"type":"system","subtype":"init","session_id":"s1","plugins":[{"name":"f"}],"mcp_servers":[{"name":"plugin:f:factory","status":"connected"}]}'
echo boom >&2
exit 3
`)
	fakeFactory(t)
	a, err := StartAgent(context.Background(), AgentConfig{SessionID: "s1"}, "hello")
	assert.NilError(t, err)

	got := problems(collect(t, a, isProblem, 5*time.Second))
	assert.Equal(t, len(got), 1)
	assert.Assert(t, strings.Contains(got[0].Error(), "code 3"), got[0])
	assert.Assert(t, strings.Contains(got[0].Error(), "boom"), got[0])

	collect(t, a, nil, 5*time.Second) // the channel then closes
}

// A real claude is never on PATH in this suite: every other test's fakeClaude
// puts one there first. This is the one test that leaves PATH with nothing
// claude-shaped on it at all, so the ErrNoClaude branch is actually driven.
func TestAgentNoClaudeOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	_, err := StartAgent(context.Background(), AgentConfig{SessionID: "s1"}, "hello")
	assert.Assert(t, errors.Is(err, ErrNoClaude), "got %v", err)
}

// A claude that never reports init exercises the agentInitTimeout branch of
// StartAgent's select, and that Stop is called on the way out: the fake
// traps SIGINT and logs it, so a forgotten Stop call would leave SIGS empty.
func TestAgentInitTimeout(t *testing.T) {
	_, sigsFile, _ := agentFakeEnv(t)
	// The trap comes first, and the timeout leaves the fake time to reach it
	// under load: a SIGINT before it would kill the fake without a trace.
	fakeClaude(t, `
trap 'echo INT >> "$SIGS"; exit 130' INT
echo "$@" > "$ARGS"
# Backgrounded and redirected away from this process's own stdout/stderr, so
# a blocking "wait" reliably sees the signal (unlike a foreground sleep) and
# the sleep never holds those pipes open past this shell's own exit.
sleep 3600 >/dev/null 2>&1 &
wait
`)
	fakeFactory(t)
	setVar(t, &agentInitTimeout, time.Second)
	setVar(t, &stopInterruptWait, 200*time.Millisecond)

	start := time.Now()
	_, err := StartAgent(context.Background(), AgentConfig{SessionID: "s1"}, "hello")
	assert.ErrorContains(t, err, "claude did not report its init")
	assert.Assert(t, time.Since(start) < 5*time.Second, "StartAgent took %s to return", time.Since(start))
	assert.Equal(t, strings.TrimSpace(readFile(t, filepath.Dir(sigsFile), filepath.Base(sigsFile))), "INT",
		"StartAgent must Stop the process on a timeout")
}

// A claude that exits before ever sending its init line exercises the
// <-a.exited branch of StartAgent's select, distinct from
// TestAgentUnexpectedExit, whose fake always sends init first.
func TestAgentExitsBeforeInit(t *testing.T) {
	fakeClaude(t, `
echo boom >&2
exit 1
`)
	fakeFactory(t)

	start := time.Now()
	_, err := StartAgent(context.Background(), AgentConfig{SessionID: "s1"}, "hello")
	assert.ErrorContains(t, err, "claude exited before it started")
	assert.Assert(t, strings.Contains(err.Error(), "boom"), err)
	assert.Assert(t, time.Since(start) < 5*time.Second, "StartAgent took %s to return", time.Since(start))
}

// A claude that ignores both SIGINT and SIGTERM drives Stop all the way to
// SIGKILL and its "killed it" error, which no other test reaches. The kill
// takes claude's process group with it, as a background wait claude started
// would otherwise outlive it.
func TestAgentStopKillsWhenBothSignalsAreIgnored(t *testing.T) {
	_, sigsFile, _ := agentFakeEnv(t)
	child := filepath.Join(t.TempDir(), "child")
	t.Setenv("CHILD", child)
	fakeClaude(t, `
echo "$@" > "$ARGS"
read -r line
echo '{"type":"system","subtype":"init","session_id":"s1","plugins":[{"name":"f"}],"mcp_servers":[{"name":"plugin:f:factory","status":"connected"}]}'
trap 'echo INT >> "$SIGS"' INT
trap 'echo TERM >> "$SIGS"' TERM
sleep 3600 >/dev/null 2>&1 &
bgpid=$!
echo "$bgpid" > "$CHILD"
# wait is interrupted by a trapped signal even when the trap does not exit,
# so this loops back into it rather than falling through once the trap runs.
while kill -0 "$bgpid" 2>/dev/null; do
  wait "$bgpid" 2>/dev/null
done
`)
	fakeFactory(t)
	setVar(t, &stopInterruptWait, 200*time.Millisecond)
	setVar(t, &stopTerminateWait, 200*time.Millisecond)
	a, err := StartAgent(context.Background(), AgentConfig{SessionID: "s1"}, "hello")
	assert.NilError(t, err)
	var pid int
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		b, err := os.ReadFile(child)
		if err != nil || len(b) == 0 {
			return poll.Continue("the fake has not started its child")
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return poll.Error(err)
		}
		return poll.Success()
	}, poll.WithTimeout(5*time.Second))

	err = a.Stop()
	assert.ErrorContains(t, err, "claude did not stop after SIGINT and SIGTERM; killed it")
	assert.Equal(t, strings.TrimSpace(readFile(t, filepath.Dir(sigsFile), filepath.Base(sigsFile))), "INT\nTERM")
	collect(t, a, nil, 5*time.Second)

	poll.WaitOn(t, func(poll.LogT) poll.Result {
		if processGone(pid) {
			return poll.Success()
		}
		return poll.Continue("claude's child %d is still running", pid)
	}, poll.WithTimeout(5*time.Second))
}

// A command claude started can outlive a claude that exits cleanly on SIGINT,
// as a background factory daemon wait would, holding the session's wait. Stop
// kills what is left in claude's process group.
func TestAgentStopKillsWhatOutlivesClaude(t *testing.T) {
	_, sigsFile, _ := agentFakeEnv(t)
	child := filepath.Join(t.TempDir(), "child")
	t.Setenv("CHILD", child)
	fakeClaude(t, `
read -r line
echo '{"type":"system","subtype":"init","session_id":"s1","plugins":[{"name":"f"}],"mcp_servers":[{"name":"plugin:f:factory","status":"connected"}]}'
trap 'echo INT >> "$SIGS"; exit 130' INT
sleep 3600 >/dev/null 2>&1 &
echo "$!" > "$CHILD"
wait
`)
	fakeFactory(t)
	a, err := StartAgent(context.Background(), AgentConfig{SessionID: "s1"}, "hello")
	assert.NilError(t, err)
	var pid int
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		b, err := os.ReadFile(child)
		if err != nil || len(b) == 0 {
			return poll.Continue("the fake has not started its child")
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		if err != nil {
			return poll.Error(err)
		}
		return poll.Success()
	}, poll.WithTimeout(5*time.Second))

	assert.NilError(t, a.Stop())
	assert.Equal(t, strings.TrimSpace(readFile(t, filepath.Dir(sigsFile), filepath.Base(sigsFile))), "INT")
	collect(t, a, nil, 5*time.Second)

	poll.WaitOn(t, func(poll.LogT) poll.Result {
		if processGone(pid) {
			return poll.Success()
		}
		return poll.Continue("claude's child %d outlived it", pid)
	}, poll.WithTimeout(5*time.Second))
}

// A long session's stderr keeps what claude wrote last, which is why it
// exited, not what it wrote first.
func TestTailWriterKeepsTheEnd(t *testing.T) {
	var w tailWriter
	_, _ = w.Write([]byte(strings.Repeat("early warning\n", outputTail)))
	_, _ = w.Write([]byte("fatal: out of memory\n"))

	got := w.tail()
	assert.Assert(t, len(got) <= outputTail, "kept %d bytes", len(got))
	assert.Assert(t, strings.HasSuffix(got, "fatal: out of memory"), got)
}

// A consumer that falls behind must not stall claude's reader: Send and Stop
// still return while hundreds of events wait to be read.
func TestAgentSlowConsumerDoesNotBlockSendOrStop(t *testing.T) {
	agentFakeEnv(t)
	fakeClaude(t, agentFakeScript)
	fakeFactory(t)
	a := startTestAgent(context.Background(), t, AgentConfig{}, "FLOOD")

	// Nothing reads Events until the fake's flood is all queued.
	poll.WaitOn(t, func(poll.LogT) poll.Result {
		a.mu.Lock()
		defer a.mu.Unlock()
		if len(a.queue) > 300 {
			return poll.Success()
		}
		return poll.Continue("%d events queued", len(a.queue))
	}, poll.WithTimeout(5*time.Second))

	done := make(chan error, 1)
	go func() {
		if err := a.Send("hello"); err != nil {
			done <- err
			return
		}
		done <- a.Stop()
	}()
	select {
	case err := <-done:
		assert.NilError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("Send or Stop blocked behind an undrained Events channel")
	}

	events := collect(t, a, nil, 5*time.Second)
	assert.Assert(t, len(events) > 300, "got %d events", len(events))
}

// Claims asks the daemon, and keeps only this session's claims.
func TestAgentClaims(t *testing.T) {
	agentFakeEnv(t)
	t.Setenv("FAKE_CLAIMS", `[{"plan":"FNDSF-40","session":"s1","dir":"/w"},{"plan":"FNDSF-41","session":"s2","dir":"/w2"}]`)
	fakeClaude(t, agentFakeScript)
	fakeFactory(t)
	a := startTestAgent(context.Background(), t, AgentConfig{SessionID: "s1"}, "hello")

	got, err := a.Claims(context.Background())
	assert.NilError(t, err)
	assert.DeepEqual(t, got, []string{"FNDSF-40"})
}

// Claims matches on the session claude reports, not the one asked for, in case
// a resumed session comes back under another ID.
func TestAgentClaimsUseTheReportedSession(t *testing.T) {
	agentFakeEnv(t)
	t.Setenv("FAKE_CLAIMS", `[{"plan":"FNDSF-40","session":"s1","dir":"/w"},{"plan":"FNDSF-41","session":"asked","dir":"/w2"}]`)
	fakeClaude(t, agentFakeScript)
	fakeFactory(t)
	a := startTestAgent(context.Background(), t, AgentConfig{SessionID: "asked", Resume: true}, "hello")

	got, err := a.Claims(context.Background())
	assert.NilError(t, err)
	assert.DeepEqual(t, got, []string{"FNDSF-40"})
}

func TestAgentClaimsWhenTheDaemonFails(t *testing.T) {
	agentFakeEnv(t)
	t.Setenv("FAKE_CLAIMS_ERR", "factory daemon isn't running")
	fakeClaude(t, agentFakeScript)
	fakeFactory(t)
	a := startTestAgent(context.Background(), t, AgentConfig{SessionID: "s1"}, "hello")

	_, err := a.Claims(context.Background())
	assert.ErrorContains(t, err, "factory daemon isn't running")
}

// A Send stuck writing to a claude that stopped reading its stdin must not hold
// up Stop.
func TestAgentStopWhileSendIsBlocked(t *testing.T) {
	fakeClaude(t, `
trap 'exit 130' INT
read -r line
echo '{"type":"system","subtype":"init","session_id":"s1","plugins":[{"name":"f"}],"mcp_servers":[{"name":"plugin:f:factory","status":"connected"}]}'
while :; do sleep 0.1; done
`)
	fakeFactory(t)
	a, err := StartAgent(context.Background(), AgentConfig{SessionID: "s1"}, "hello")
	assert.NilError(t, err)
	go func() {
		for range a.Events() {
		}
	}()

	sent := make(chan error, 1)
	go func() { sent <- a.Send(strings.Repeat("x", 1<<20)) }() // far more than a pipe holds
	select {
	case err := <-sent:
		t.Fatalf("Send returned before claude stopped: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	stopped := make(chan error, 1)
	go func() { stopped <- a.Stop() }()
	select {
	case err := <-stopped:
		assert.NilError(t, err)
	case <-time.After(10 * time.Second):
		t.Fatal("Stop waited on a blocked Send")
	}
	assert.ErrorContains(t, <-sent, "send to claude")
}
