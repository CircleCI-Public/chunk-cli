# Design: Interactive Logs for Sidecars

Status: proposal
Author: Jesse de Guzman

## Summary

Give developers live output from commands running on a sidecar, and let them
read back the output of commands that already finished. The watch daemon becomes
the owner of a bounded per-command output buffer; `chunk watch` gains a
scrollback pane that tails a running command and replays a finished one.

Alongside it, the same daemon samples CPU, memory and disk from each running
sidecar and the dashboard displays them live.

**This ships as a single PR, separate from any other work in flight.** It is
deliberately self-contained: it depends on no other branch, and it touches no
signature that another open PR is currently changing.

A caveat to record up front, because it shapes half the design: **there is no
server-side metrics API.** Live resource usage therefore has to be sampled over
SSH from inside the sidecar, which is the single riskiest thing in this PR — a
persistent connection per sidecar that the daemon must supervise. It is
specified in full in [Resource usage](#7-resource-usage), including the
handshake-per-call trap that the obvious implementation falls into. If the PR
needs to shed weight during review, this is the part to shed, because logs stand
alone without it and it is the part most likely to need a second pass.

## Motivation

A sidecar run is currently opaque in two directions.

**While it runs.** `chunk validate` streams remote output to its own stdout
(`newExecFn` in `internal/cmd/validate.go:812` writes each frame straight to
`streams.Out`), so a developer who invoked `chunk validate` by hand sees
everything. But the common case is not a hand-invoked validate — it is the
`PreToolUse` / Stop hook firing inside an agent session, where nobody is
watching that stdout. The run happens, the process exits, and the output is
gone.

**After it finishes.** `events.jsonl` records only status *messages* — the
`iostream.StatusFunc` calls wrapped by `eventlog.Wrap`
(`internal/eventlog/eventlog.go:118`). So `chunk watch` can render
`lint  ✗  4.2s` in its activity pane and cannot render a single line of the
compiler error that caused it. The dashboard tells you that something failed and
then has nothing further to say, which is the point at which the developer
leaves the dashboard and re-runs the command by hand — the exact loop the
sidecar was supposed to remove.

Both gaps have the same root cause: chunk never retains command output anywhere
that outlives the process which produced it.

## What already exists

Most of the machinery needed here is built. Establishing this precisely matters,
because it determines how much of this design is new code versus routing.

### The output endpoint is already replayable and resumable

`internal/circleci/client.go:429` calls
`GET /api/v3/sidecar/commands/{id}/output`. It is a Server-Sent Events stream
whose frames are:

| Event | Payload |
|---|---|
| `start` | acknowledged, no payload used |
| `stdout` / `stderr` | base64-encoded raw bytes |
| `exit` | `{exit_code, signal, pid}` — terminal |
| `error` | `{code, message, retryable}` — terminal |

`streamOnce` (`internal/circleci/client.go:362`) tracks the last SSE event ID and
`streamCommandOutput` (`:271`) reconnects with `Last-Event-ID` whenever a
connection drops before a terminal frame. The comment on `maxStreamAttempts`
(`:214`) states the reason plainly: the server caps each connection at roughly
fifteen seconds, so reconnect-and-resume is the *normal* way this API serves a
long-running command, not an error path.

That resume behaviour is the important discovery. A server that can serve frames
from an arbitrary cursor is a server that retains frames, which means the same
endpoint answers both halves of this feature:

- attach with an empty cursor while the command runs → **live tail**
- attach with an empty cursor after it exits → **replay**

No new API surface is required for logs.

### The daemon already owns per-project tailing state

`internal/watchd` polls every project every five seconds (`PollInterval`,
`internal/watchd/load.go:18`), reads new event log entries via
`eventlog.TailFrom` using a byte offset it keeps per project
(`daemon.updateProject`, `internal/watchd/daemon.go:160`), keeps a capped window
of the 300 most recent events (`RecentEvents`), and serves the whole thing as
JSON over a Unix socket (`newServer`, `internal/watchd/ipc.go:13`).

The offset-based tailing pattern this design proposes for output is therefore not
a new idea in the daemon — it is the pattern the daemon already uses for events,
applied to a second data source.

## Open question to resolve before building

**What is the server-side retention window for command output?**

Nothing in the client tells us. The cursor machinery proves retention exists; it
does not reveal for how long, or whether retention survives the sidecar instance
being reaped.

This changes the design materially:

- **Retention measured in hours** — the daemon's buffer is a latency and
  fan-out optimisation. A cold `chunk watch` could lazily fetch output for any
  command it knows about, and the buffer can be small.
- **Retention measured in minutes** — the daemon's buffer is the actual durable
  store, and it must start streaming a command *as it runs* or the output is
  lost forever. Replay of anything older than the buffer becomes impossible, and
  the buffer probably needs to spill to disk under `config.ProjectDataDir`.

The design below is written to work in either case, because it streams eagerly at
submit time rather than lazily on user request. But the second case adds a
disk-spill requirement that is worth knowing about before, not after.

**Action:** confirm with the sandbox-provisioner team, and confirm whether output
outlives instance deletion.

## Design

### 1. Command identity: submit and stream must be separable

`Client.Exec` (`internal/circleci/client.go:235`) submits a command and consumes
its entire output stream in one call, returning the command ID only in the final
`ExecResponse`. The ID therefore becomes available *after* the command has
finished — too late to tail it.

Split the two operations:

```go
// SubmitExec submits a command and returns its ID without consuming output.
func (c *Client) SubmitExec(
    ctx context.Context, sidecarID, command string, args []string, env map[string]string,
) (commandID string, err error)

// StreamOutput consumes a command's output stream from cursor to termination,
// resuming across the server's connection cap. cursor "" starts from the
// beginning of retained output.
func (c *Client) StreamOutput(
    ctx context.Context, commandID, cursor string, onOutput OutputFn,
) (*ExecResponse, error)
```

`Exec` keeps its current signature and becomes the composition of the two, so
every existing caller — `sidecar.Exec`, `internal/cmd/sidecar.go:397`,
`newExecFn` — is untouched. This is the only change in the PR that is a pure
refactor, and it should be its own commit.

### 2. Command discovery: register with the daemon, do not mine the event log

The daemon has to learn that a command exists in order to stream it. There are
two ways in, and the choice is what keeps this PR self-contained.

**Rejected: read the command ID out of `events.jsonl`.** PR #545
(`fact-451-record-sidecar-command-id-in-event-log`, `schurchleycci`) adds
`Event.CommandID` and threads a `commandID` return through
`validate.RunRemote` / `RunRemoteInline`. Discovering commands from the event log
would be the natural fit for a daemon that already tails it. Rejected on three
grounds:

- It makes this PR depend on a branch we do not own, currently in a
  `CONFLICTING` merge state with review outstanding. #535 (`danmux`) is changing
  the same `validate` terminal-event surface.
- Even once landed, #545 stamps the ID onto *terminal* events, so the daemon
  learns a command existed only after it finished. That is enough for replay and
  useless for a live tail.
- Discovery would be gated on the five-second poll, so a tail could open up to
  five seconds late.

**Chosen: the submitting process tells the daemon directly.** Add one write route
to the socket API:

```
POST /command
    {"command_id", "sidecar_id", "project_root", "op", "name", "submitted_at"}
```

`newExecFn` calls it immediately after `SubmitExec` returns, before consuming any
output. The daemon starts a streamer for that command at once.

This is better than event-log mining on its own merits, not just for scheduling
reasons. `events.jsonl` is a best-effort, human-readable status log that rotates
at 2000 lines (`internal/eventlog/eventlog.go:50`); using it as an IPC channel
for machine identifiers was always a bit of a smuggle. A direct notification is
honest about what it is, and it is immediate.

**Registration must be best-effort.** If the daemon is not running, `POST
/command` fails and is ignored: the command runs exactly as it does today and
its output streams to stdout as today, with no buffer. In particular
`chunk validate` must **not** call `watchd.EnsureRunning` — spawning a daemon as
a side effect of a hook firing is intrusive, and a hook that hangs because a
daemon launch hung is a much worse failure than a missing logs pane. Dial,
give up quietly, move on.

### 3. The daemon owns the buffer

**Decision: the watch daemon streams command output and buffers it. The TUI never
talks to the CircleCI API.**

Consequences, stated plainly because they are the cost of this choice:

- **The daemon needs a CircleCI token.** It has never needed one; it reads only
  local files today. See [Daemon authentication](#4-daemon-authentication).
- **The daemon gains outbound network I/O and long-lived connections.** Its
  failure modes today are all local-filesystem failures. After this it can fail
  because of a 401, a 5xx, or a hung connection, and none of those may stall the
  five-second project poll — streamers run as independent goroutines, never on
  the poll path.
- **Buffers must be bounded in bytes, not lines.** `RecentEvents = 300` is the
  existing precedent for capping, but events are small and uniform while command
  output is neither; a verbose test suite emits megabytes in seconds. Cap per
  command (proposal: 256 KiB, keeping the tail — the end of a failed run is what
  anyone wants) and cap retained commands per project (proposal: 20, evicting
  oldest-completed first). A running command is never evicted.
- **The command registry is in-memory, so a daemon restart loses it.** Output
  already streamed is lost with it. This is the one place where #545 is genuinely
  complementary rather than redundant — see
  [Relationship to #545](#relationship-to-545).

**Rejected alternative: the TUI streams from the API directly.** Roughly half the
code — no new IPC, no daemon auth, no buffer lifecycle. Rejected because it
re-fetches every command's output on every dashboard restart, opens one stream
per attached terminal, and cannot capture output for a run that finished while no
dashboard was open, which is the single most common case. It also puts outbound
API calls on the render path of a BubbleTea model, where a slow request is a
frozen UI.

### 4. Daemon authentication

The daemon resolves a token the same way commands do, via
`config.ResolveCircleCI` and `authprompt.ResolveCircleCIClient`.

`ResolveCircleCIClient` returns `authprompt.ErrNeedsAuth` rather than prompting —
prompting lives in the `cmd` layer (`ensureCircleCIClient`,
`internal/cmd/authhelper.go:58`). The daemon treats `ErrNeedsAuth` as "output
streaming unavailable" and carries on serving snapshots exactly as it does today.
It must never prompt: it has no terminal, and a background process blocking on a
hidden prompt is indistinguishable from a hang.

**Keychain risk.** `config.Resolve` may read the token from the OS keychain
(`internal/config/config.go:179`). The daemon is launched detached
(`launchDaemon`, `internal/watchd/client.go:72`, with `detachProcess`), and
keychain access from a detached process is not guaranteed to behave the way it
does from a foreground CLI — on macOS it may fail or, worse, raise a UI prompt
attributed to no visible application. **Test this on macOS first**, before
building on it: if it is unreliable, the fallback is for `POST /command` to carry
the resolved token, which is less clean but keeps the token off any process
command line.

The daemon must surface an auth failure in `Snapshot` rather than swallowing it,
so a developer whose logs pane is mysteriously empty learns why.

### 5. IPC: extend the request/response API, do not add SSE

The socket API is request/response JSON with a five-second client timeout
(`unixClient`, `internal/watchd/ipc.go:35`). Add one read route in the same
style, alongside the `POST /command` write route from §2:

```
GET /output?command_id=<id>&offset=<n>
    → {"data": "<base64>", "next_offset": <n>, "running": <bool>,
       "exit_code": <int|null>, "truncated": <bool>}
```

`offset` is a byte offset into the daemon's buffer for that command — the same
cursor discipline `eventlog.TailFrom` already uses for events, not the server's
opaque SSE event ID. Keeping the server cursor private to the daemon means the
TUI never has to reason about reconnects, and `truncated` tells the TUI honestly
that the head of the output was evicted rather than silently showing a partial
run as if it were whole.

`Snapshot` also grows a per-project `Commands []CommandState` carrying
`{CommandID, SidecarID, Name, SubmittedAt, EndedAt, ExitCode}`, so the TUI can
find the commands belonging to the selected sidecar without a second round trip.

**Why not SSE over the Unix socket.** It would mean a second transport, a
streaming client in the TUI, and abandoning the client timeout that currently
makes every daemon call trivially bounded. Polling a local Unix socket is cheap,
and the TUI is already a polling architecture. One transport, one mental model.

### 6. TUI: a scrollback pane on the existing activity view

`renderActivityPane` (`internal/tui/watch/model.go:458`) groups events into
invocations and renders them collapsible; `Enter`/`Space` already toggles the
selected invocation (`:222`).

Extend that rather than adding a third pane:

- An invocation that resolves to a known command renders an affordance
  indicating output is available.
- `Enter` on such an invocation opens a scrollback view fed by `GET /output`.
- While the command is running, the pane tails: `pollInterval`
  (`internal/tui/watch/model.go:20`) drops from 5 s to ~200 ms while a tail is
  open, and returns to 5 s when it closes. Only the output request polls fast;
  the snapshot request stays on its five-second tick.
- `Esc` closes the pane and restores the previous selection.

**Joining an invocation to a command.** Without `Event.CommandID` the join is a
heuristic: match on sidecar ID, then on `SubmittedAt` falling within the
invocation group's time span. This is good enough — a sidecar runs validate
commands sequentially, so overlapping candidates are rare — but it is a
heuristic, and when it finds no match the affordance simply does not appear. It
becomes exact once #545 lands.

Two rendering details that will otherwise cause bugs:

- **Output contains ANSI and carriage returns.** `newExecFn` deliberately passes
  raw bytes through so "carriage-return redraws and ANSI colour render as the
  remote command intended". Inside a BubbleTea view those same bytes will corrupt
  the layout unless the pane interprets `\r` (line rewrite) and either passes SGR
  sequences through to lipgloss or strips them. A progress bar from a test runner
  is the realistic worst case and should be an explicit test fixture.
- **Long lines must wrap or be clipped deliberately**, not left to the terminal,
  since the pane is one half of a split layout.

### 7. Resource usage

**There is no metrics endpoint.** The full API surface used by
`internal/circleci/client.go` is instances (list/create/delete), SSH key add,
exec, command get, command output, prune, and snapshots. Nothing exposes CPU,
memory, or disk for a running sidecar.

So this has to be sampled from inside the sidecar, and the obvious
implementation is a trap: `ExecOverSSH` opens and closes a fresh WebSocket *and*
SSH handshake on every call — "Each call to ExecOverSSH opens and closes its own
SSH connection" (`internal/sidecar/session.go:70`). A two-second sample interval
implemented that way means a full handshake every two seconds per sidecar, which
is both wasteful and a good way to get rate-limited.

**Design: one persistent connection per sidecar, one long-lived remote sampler.**
The daemon holds a `sshConn` and starts a single remote loop whose stdout it
parses incrementally:

```sh
while :; do
  echo "=== $(date +%s)"
  cat /proc/stat /proc/meminfo
  df -P .
  sleep 2
done
```

Parsing notes, because each is a place to get a plausible-looking wrong number:

- **CPU is a delta, not a level.** `/proc/stat`'s `cpu` line is cumulative
  jiffies since boot. A percentage requires differencing consecutive samples;
  reporting the raw counter, or the first sample alone, yields nonsense.
- **Memory must be read cgroup-aware.** The sidecar is containerised, so
  `/proc/meminfo` reports the *host's* memory, not the container's limit.
  Prefer `/sys/fs/cgroup/memory.max` and `memory.current` (cgroup v2), falling
  back to `memory.limit_in_bytes` / `usage_in_bytes` (v1), and only then to
  `/proc/meminfo`. Getting this wrong makes every sidecar look like it has
  hundreds of gigabytes free.
- **`df -P .`** is relative to the sampler's working directory, which is not
  necessarily the workspace. Sample the resolved workspace path instead.

**Lifecycle is the hard part**, not the parsing. The daemon must start a sampler
when a sidecar appears in a poll, reconnect with backoff when the connection
drops, stop sampling when the sidecar goes away or is reaped, and never let any
of that block the five-second project poll. Samplers are per-sidecar goroutines
with their own contexts, cancelled when the sidecar leaves `loadSidecars`.

**Sampling is opt-out and idle-aware.** A persistent SSH connection per sidecar
is a real resource cost for someone with several sidecars, so: sample only while
a `chunk watch` dashboard is actually attached (the daemon already knows,
because snapshots are being requested), and stop when none is. This keeps the
cost proportional to the benefit and means a developer who never opens the
dashboard pays nothing.

`Snapshot` grows a per-sidecar `Resources` field —
`{CPUPercent, MemUsedBytes, MemLimitBytes, DiskUsedBytes, DiskTotalBytes, SampledAt}`
— nil when no sample has arrived yet. The dashboard renders it as a compact line
in the left pane under the selected sidecar, and stale samples (older than, say,
three intervals) render dimmed rather than disappearing, so a stalled sampler
looks stalled rather than looking like an idle sidecar.

**This is the part to replace.** Ask the sandbox-provisioner team for
`GET /api/v3/sidecar/instances/{id}/metrics`. If it arrives, the daemon swaps
the sampler for a cheap poll, deletes the persistent connection and all of the
parsing above, and the `Resources` field and dashboard rendering stay exactly as
they are. Worth raising now so the SSH sampler is understood as a bridge rather
than the destination.

## Relationship to #545

This PR does not depend on #545 and does not conflict with it: #545 changes
`eventlog.Wrap` and `validate.RunRemote` signatures, neither of which this design
touches. They compose, in whichever order they land.

What #545 adds on top, once merged:

- **A durable command record.** `Event.CommandID` in `events.jsonl` survives a
  daemon restart, where this PR's in-memory registry does not. A follow-up can
  have the daemon fall back to event-log command IDs for commands it has no
  registration for, restoring replay across restarts.
- **An exact invocation join**, replacing the sidecar-ID-plus-time-window
  heuristic in §6.

Neither is worth blocking on. If #545 lands first, the follow-up is small; if it
lands second, nothing here needs revisiting.

## Scope of this PR

In, as ordered commits so the diff can be reviewed in passes:

1. `SubmitExec` / `StreamOutput` split in `internal/circleci`. Pure refactor,
   no caller changes, no behaviour change.
2. Daemon: token resolution with graceful `ErrNeedsAuth` degradation.
3. Daemon: per-command byte-bounded output buffer and streamer lifecycle.
4. Daemon: `POST /command` and `GET /output` socket routes; `Commands` on
   `Snapshot`.
5. `newExecFn` registers each command with the daemon, best-effort.
6. TUI: scrollback pane, fast-poll-while-tailing, ANSI and `\r` handling.
7. Daemon: persistent-connection resource sampler, cgroup-aware parsing,
   attached-dashboard gating; `Resources` on `Snapshot`.
8. TUI: resource line in the left pane, dimmed when the sample is stale.
9. Docs: `docs/CLI.md` keybindings, `docs/ARCHITECTURE.md` daemon
   responsibilities and the new socket routes.

Commits 1–6 are logs; 7–8 are resource usage and depend on nothing in 1–6. If
review says the PR is too big, dropping 7–8 leaves a coherent feature and costs
nothing already written.

Out, deliberately:

- **`chunk sidecar logs <command-id>`.** Nearly free once the split in (1)
  exists, and genuinely useful for scripts and for agents with no terminal, but
  it is a new user-facing command with its own flags, help text, and acceptance
  tests. It does not belong in a PR this size. Fold it in only if review shows
  the TUI work shrinking.
- **Making `SidecarState.Running` a fact.** Once the daemon streams commands it
  knows which are genuinely running, so `Running` could stop being inferred from
  "most recent event is non-terminal and newer than `RunningTimeout`"
  (`annotateActivity`, `internal/watchd/load.go:134`). Worth doing, but it
  changes existing dashboard behaviour for every user and deserves its own
  review.
- **Persisting buffers across daemon restarts**, unless the retention answer
  forces a disk spill.
- **Interactive *input* to a running command.** `InteractiveShell`
  (`internal/sidecar/ssh.go:219`) already covers "I need a terminal in there";
  this feature is read-only observation.
- **Changing how `chunk validate` prints to its own stdout.** That path works and
  developers rely on it.

This is a large PR — a client refactor, new daemon networking and auth, two new
socket routes, a persistent SSH sampler, and two TUI modes. The commit ordering
above is the mitigation: each commit stands alone and the tree builds at every
step, so a reviewer can walk it in passes rather than facing it whole. If it
still reads as too much in review, commits 7–8 are the clean cut line.

## Risks

| Risk | Impact | Mitigation |
|---|---|---|
| Server retention window is short | Replay of older runs impossible | Stream eagerly at submit; spill buffer to `ProjectDataDir` if needed |
| Keychain unreadable from detached daemon | No output at all, silently | Test on macOS before building; fall back to token in `POST /command`; surface auth failure in `Snapshot` |
| Daemon restart loses the command registry | Running tails drop, replay unavailable | Accepted for this PR; #545 follow-up restores it from the event log |
| Output volume from a verbose suite | Daemon memory growth | Byte-capped tail-keeping buffers, capped command count |
| ANSI / `\r` corrupt the TUI layout | Visibly broken dashboard | Interpret `\r`, handle SGR explicitly, progress-bar test fixture |
| Daemon streamers stall the poll loop | Dashboard freezes for all projects | Streamers strictly off the poll path, per-command contexts |
| Registration slows down `validate` | Hook latency, worst case a hang | Best-effort dial with a short timeout, never `EnsureRunning`, failure is a no-op |
| Fast poll while tailing burns CPU | Laptop fan, battery | Only the output request polls fast; revert on pane close |
| Persistent SSH connection per sidecar | Resource cost, possible rate limiting | One connection per sidecar, gated on an attached dashboard, backoff on reconnect |
| Memory read from host instead of cgroup | Plausible but wrong numbers, silently | cgroup v2 → v1 → `/proc/meminfo` fallback, tested against captured fixtures |
| Sampler dies and dashboard shows last value | Idle-looking sidecar that is actually busy | Dim samples older than three intervals rather than hiding them |

## Testing

Consistent with the project's integration-over-mocks and fakes-over-mocks rules:

- **`StreamOutput`** — the existing SSE fake in
  `internal/circleci/exec_stream_test.go` already models dropped streams, empty
  streams, and cursor resume. Extend it rather than writing a new one, and add a
  case for attaching to an already-exited command.
- **Daemon buffer** — unit tests on eviction: byte cap keeps the tail, command
  cap evicts oldest-completed, a running command is never evicted, `truncated`
  is set exactly when the head was dropped.
- **Socket routes** — real Unix socket against a real daemon in a temp
  `CHUNK_WATCHD_DIR`, following `internal/watchd/daemon_test.go`. Include
  `POST /command` for an unknown project and a duplicate command ID.
- **Registration is best-effort** — `newExecFn` with no daemon running must
  produce byte-identical behaviour to today. This is the regression that matters
  most, because it is on the hook path.
- **Auth degradation** — daemon with no resolvable token still serves
  `/snapshot` and reports the auth failure in it.
- **TUI** — table-driven render tests over raw byte fixtures containing ANSI,
  `\r` redraws, and over-wide lines, in the style of
  `internal/tui/watch/model_test.go`.
- **Resource parsing** — table-driven tests over captured `/proc/stat`,
  `/proc/meminfo`, cgroup v1 and v2, and `df -P` fixtures from a real sidecar.
  Cover the two failure modes that produce plausible wrong answers: a single
  sample yielding no CPU percentage rather than a bogus one, and a cgroup limit
  being preferred over the host's `MemTotal`.
- **Sampler lifecycle** — a fake SSH sampler that can be made to drop mid-stream
  must reconnect; a sidecar disappearing from `loadSidecars` must cancel its
  goroutine; no dashboard attached must mean no sampler running.
- **Race detector** on everything, per the project rule; the daemon gains
  concurrent buffer writes and reads plus per-sidecar sampler goroutines, which
  is exactly what it catches.
