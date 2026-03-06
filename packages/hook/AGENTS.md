# chunk hook — Coding Guidelines

A Bun-based CLI that provides configurable exec, task, sync, state, scope, repo, and env
commands for Claude Code hooks.
Seven commands: `exec` (run shell commands like tests/lint),
`task` (delegated tasks like code review),
`sync` (grouped sequential checks), `state` (cross-event data sharing), `scope` (per-repo activity gate),
`repo` (repository initialization), and `env` (shell environment configuration).
Communicates via stdin JSON and exit codes per the Claude Code hooks spec.

## How It Works

The CLI is designed for **Claude Code hooks** — the host agent invokes it, and it responds
via exit codes: **exit 0** (allow) lets the agent continue, **exit 2** (block) feeds the
stderr message back to the agent as an error prompt. Exit 1 signals an infra error.

### Exec: direct invocation

`chunk hook exec run <name>` — executes a shell command (tests, lint, etc.) inline, evaluates the
result, and returns immediately.

- **Exit 0:** Command passed. Agent continues.
- **Exit 2:** Command failed. Stderr contains failure output and a directive to fix and retry.

Best for short-running commands at major workflow checkpoints (pre-commit, pre-push).

### Exec: delegation pattern (long-running commands)

Hooks enforce a strict timeout (10 min max). For commands that may exceed this, the delegation pattern
moves execution out of the hook and onto the agent itself. The hook only runs `check`; the agent runs
the command when told to.

Flow:

1. The hook calls `chunk hook exec check <name>`.
2. On first call (no result file), `check` **blocks** with a directive telling the agent to run
    `chunk hook exec run <name> --no-check`.
3. The agent executes `run --no-check` in its own terminal — no hook timeout applies. The command
    result is saved to a sentinel file, and `run --no-check` **always exits 0**.
4. On the next invocation, `check` reads the saved sentinel and enforces: exit 0 (pass) or exit 2
    (fail with output on stderr).

**Flag pass-through:** All flags can be set on `check`. When `check` builds the `run --no-check`
directive, it passes through `--cmd`, `--timeout`, `--file-ext`, `--staged`, and `--always` so the
delegated run uses the same overrides. Flags not relevant to `--no-check` (`--on`, `--trigger`,
`--matcher`, `--limit`) are omitted — `--no-check` always exits 0 and does not enforce limits,
trigger matching, or matcher filtering.

### Task: delegated work via subagent

Tasks delegate complex work (code review) to a subagent and enforce the result.

1. `chunk hook task check <name>` — the primary entry point. On first call
   (no result file), blocks with:
   - A directive to spawn a subagent.
   - Task instructions loaded from a file (e.g., `code-review-instructions.md`).
   - The JSON schema for the result.
   - The file path where the subagent must write its result.

    The agent spawns a subagent, which writes `{ "decision": "allow" | "block", "reason": "..." }`
    to the sentinel path. On the next invocation, `task check <name>` reads and validates the result:
   - `"allow"` → exit 0.
   - `"block"` → exit 2 with the full raw JSON on stderr, so the agent can act on structured feedback.

**Flags:** `--instructions`, `--always`, `--staged`, `--limit`, `--on`, `--trigger`, `--matcher`,
`--schema` — all apply to `task check`.

### Sync: grouped sequential checks

`chunk hook sync check exec:<name> [task:<name>] [flags]` — runs multiple exec/task checks as
a single ordered group. Use `sync check` whenever **two or more** delegated checks share the same
hook event (e.g., `exec check tests` + `task check review` on `Stop`). It ensures correct ordering
and prevents the ping-pong problem that standalone checks would cause.

Behavior:

1. Maintains a **group sentinel** tracking which specs have already passed.
2. Runs specs left-to-right. For each: reads the individual sentinel, enforces the result.
3. On **pass**: updates group sentinel, moves to next spec. The individual sentinel **persists** as
   a cache — it is not consumed. Staleness is detected via session ID and content-hash checks.
4. On **missing/pending**: blocks with a directive to run/complete that command. Resumes here next time.
5. On **fail** (default `--on-fail restart`): removes the group sentinel and the individual sentinel,
   blocks. The entire group restarts from the beginning on the next invocation.
   With `--on-fail retry`: only the failed spec is removed from the group's passed list —
   previously-passed specs are preserved and only the failed command needs to re-run.
6. When **all pass**: removes the group sentinel, exits 0.

By default, sync evaluates all specs and combines non-pass results into a single block message.
This gives the agent a complete picture of everything that needs attention in one round-trip.
With `--bail`, sync stops at the first non-pass spec and blocks immediately.

Flags (`--on`, `--trigger`, `--matcher`, `--limit`, `--staged`, `--always`, `--instructions`,
`--schema`, `--on-fail`, `--bail`) are parsed once and applied to all specs in the group.

**Note — standalone `exec check` / `task check`:** Sentinels persist on pass (they are not
consumed). Staleness is detected via session ID and content-hash mismatches, so stale sentinels
from a previous session or against different code are automatically treated as missing.
**Use `sync check` to group multiple delegated checks on the same event** to ensure correct
ordering and a single combined block message.

### State: cross-event data sharing

`chunk hook state <subcommand>` manages per-project state that persists across events. The state
command does **not** require `CHUNK_HOOK_ENABLE` — it is infrastructure, always available.

- `state save` — reads event input from stdin and stores it under the event name. Internally this
    is a clear + append: it replaces all existing entries with a single-entry `__entries` array.
    Each entry records the current `HEAD` SHA and a composite fingerprint
    (`sha256(HEAD + "\n" + git_diff)`) — used by change detection to determine whether code has
    changed since the session started.
- `state append` — like `state save`, but accumulates entries instead of replacing them. Successive
    appends preserve earlier entries (e.g., the original prompt and any "Continue" prompts). Each
    entry carries its own `head` and `fingerprint`; the first entry serves as the baseline reference.
- `state load [field]` — reads a field from state using dot or bracket notation
    (e.g., `UserPromptSubmit.prompt`, `UserPromptSubmit[0].prompt`).
    Without a field, dumps entire state as JSON.
- `state clear` — removes all saved state for the project.

**Data shape:** All events store an `__entries` array — both `save` (1 entry) and `append` (N entries)
produce the same structure:
```json
{
  "UserPromptSubmit": { "__entries": [{ "prompt": "...", "head": "abc123", "fingerprint": "sha256..." }, ...] },
  "Stop": { "__entries": [{ ... }] }
}
```
Stored in the sentinel directory alongside sentinel files, using a sha256 hash of the project dir.
Saving an event overwrites all entries for that event; other events are preserved.

**Array-indexed templating:** Use bracket notation to access specific entries:
`{{UserPromptSubmit[0].prompt}}` for the first entry, `{{UserPromptSubmit[1].prompt}}` for the
second. Plain dot notation `{{UserPromptSubmit.prompt}}` is syntactic sugar for
`{{UserPromptSubmit[0].prompt}}` (first entry, not concatenation).

**Baseline fingerprint tracking:** The first entry's `fingerprint` field provides the composite hash
at session start. Change detection compares the current fingerprint against the baseline — a single
comparison that covers both commit-level and file-level changes. Used by both `sync check` and
standalone `task check` to skip tasks when no code changes have occurred since the session started
(see [Change detection](#change-detection) below).

### Scope: per-repo activity gate

`chunk hook scope <subcommand>` manages a per-repo activity gate for multi-repo workspaces.
In VS Code multi-root workspaces, Claude Code merges all `.claude/settings.json` files, so **all
hooks fire for all repos** — even repos the agent hasn't touched. The scope command prevents
expensive hooks (tests, lint, review) from running in inactive repos.

> **Why this exists:** Ideally, Claude Code would set `CLAUDE_PROJECT_DIR` (or the hook payload's
> `cwd`) to the repo that the current tool call targets. If it did, a simple `cwd`-vs-config check
> would be enough and the scope gate would be unnecessary. VS Code now sets CWD per-repo (bugs
> #8559 and #12808 are fixed), but `process.cwd() === projectDir` is true for **every** repo — it
> tells us "a hook launched here" not "the agent is working here". The scope command works around
> this by inspecting the actual file paths in `tool_input` to determine which repo is being touched.
>
> **The `--project` flag** further mitigates the CWD bug: each per-repo `settings.json` passes
> `--project <repo-name>` to all commands, so the CLI knows which repo _should_ be the target
> regardless of what `event.cwd` reports. `resolveProject()` in `env.ts` resolves the flag value:
> absolute paths are used directly; relative names are joined with `CHUNK_HOOK_PROJECT_ROOT`.
> This replaces `event.cwd` for config loading and
> project-dir resolution.

**No CWD trust:** The scope gate does **not** use `process.cwd()` as a discriminator.
VS Code now correctly sets CWD per-repo in multi-root workspaces (bugs #8559, #12808 are
fixed), which means `process.cwd() === projectDir` is true for **every** repo — it cannot
distinguish "agent worked here" from "VS Code launched a hook here". Activation relies
exclusively on file paths extracted from `tool_input` matching the project directory.

- `scope activate` — reads stdin JSON, checks if any file paths in `tool_input` reference the
    project directory AND a session ID is present. If both conditions are met, writes
    `.chunk/hook/.chunk-hook-active` with the session ID and timestamp. Always exits 0 (exits 1
    only on fatal write errors). Available for explicit use when no exec/task hook is present
    in a hook group.
- `scope deactivate` — removes `.chunk/hook/.chunk-hook-active`. Always exits 0.

**Activation requires context:** The marker is only written when the raw payload contains file
paths that **match** the project and a session ID. Events with no extractable paths (e.g., `Stop`,
`SessionStart`) never auto-activate — they only check an existing marker. Events whose paths all
target a **different** repo return `false` immediately (definitive rejection — the marker is
irrelevant). `matchesProject()` returns a tri-state: `"match"` (paths hit this repo), `"no-paths"`
(no paths to inspect), or `"mismatch"` (all paths hit other repos). Agent-invoked commands
(`exec run --no-check`) and direct CLI invocations without stdin context do not activate. The `exec`
handler skips the scope gate entirely for `--no-check` since those run in the target repo via
`process.cwd()`.

**Auto-activate:** The `exec`, `task`, and `sync` handlers call `activateScope()` automatically
before both the `--matcher` filter and the gate check — if the stdin payload contains matching
file paths and a session ID, the scope is activated as a side effect and the function returns
`true`. This runs for **every** tool event (the native `matcher` is `"*"`), not just shell tools,
so file edits and reads keep the scope alive. No separate `scope activate` hook entry is needed
in the default template.

**Session binding:** The marker file stores `{ sessionId, timestamp }` as JSON. When a session
ID is available in a subsequent event, it is compared to the stored one. The comparison has two
distinct behaviors depending on the code path:

- **Activation path** (paths match this project): If a marker already exists with a
  _different_ session ID, the existing marker is **preserved** — the new session is treated
  as active (returns `true`) without overwriting. This is the subagent safety mechanism.
- **Validation path** (no paths or paths target a different repo): A session ID mismatch means
  the marker belongs to a different session (possibly a parallel agent) and is treated as
  **inactive** (returns `false`).

When no session ID is present, session validation is skipped and only file existence is checked.
Both `exec` and `task` handlers use the same `activateScope()` call with identical parameters,
so session binding behavior is consistent across commands.

**Subagent safety:** When the parent agent spawns a subagent (e.g., via `runSubagent` for code
review), the subagent receives a **different session ID** from VS Code. The subagent's tool calls
(reading files, searching code) trigger the normal hook chain — `exec check` / `task check` —
which calls `activateScope()` with the subagent's session ID. Without protection, this would
overwrite the parent's marker, causing a brief scope gap when control returns to the parent
(the parent's session ID no longer matches the marker). The "first writer wins" rule in the
activation path prevents this: once a marker exists, it is not overwritten by a different session.
The subagent is treated as active (hooks still run for it) but the parent retains ownership.
The marker is only cleared by explicit `scope deactivate` (on `SessionStart`/`SessionEnd`).

**Internal gate:** `activateScope()` returns a boolean. If `false`, `exec` and `task` allow
silently (exit 0).

**Hook wiring:** Only `scope deactivate` needs explicit hook entries (`SessionStart` / `SessionEnd`).

In single-repo workspaces (including Claude Code CLI), tool-call events that reference project
files activate the marker as a side effect. Once the marker exists, no-paths events (Stop) find
it and the scope is active — behavior is identical to multi-repo workspaces.

The marker file lives inside `.chunk/hook/` (chunk hook's own directory, typically gitignored).
It does not require `CHUNK_HOOK_ENABLE` — it is infrastructure, always available (like `state`).

### Repo init

`chunk hook repo init [dir] [--force]` — scaffolds the `.chunk/hook/` and `.claude/` directory
structure in a repository. Template files are embedded as TypeScript string constants in
`src/lib/templates.ts` (not loaded from disk). The `TEMPLATE_FILES` manifest array defines the
output path, content, and a flag for whether `__PROJECT__` substitution should be applied.

Behavior:

- Creates directories as needed.
- If a target file already exists, copies to `.example.<ext>` (e.g., `config.example.yml`,
  `settings.example.json`) instead of overwriting.
- Files without an extension get `.example` appended (e.g., `.gitignore` → `.gitignore.example`).
- `--force` overwrites all files unconditionally.
- `__PROJECT__` in `settings.json` is replaced with the repo directory's `basename`.

The command does not require `CHUNK_HOOK_ENABLE` — it is a setup utility.

### Env update

`chunk hook env update [--profile <name>] [--env-file <path>] [--set-log-dir <dir>]
[--set-project-root <dir>] [--set-verbose]` — configures the user's shell environment so that
`CHUNK_HOOK_*` variables are available in every terminal session.

Steps performed:

1. **Log directory:** Creates `--set-log-dir` if it doesn't exist.
2. **Env file:** Writes `--env-file` (default `~/.config/chunk-hook/env`) with profile-appropriate
   exports via `generateEnvContent()`.
3. **Login sourcing:** Adds a `source <env-file>` line to shell startup files via
   `ensureLoginSourcing()` so the env file is loaded on login.

**Profiles** (defined in `PROFILES` array in `shell-env.ts`):

- `disable` — no `CHUNK_HOOK_*` variables exported (effectively disables hooks).
- `enable` — `CHUNK_HOOK_ENABLE=1`.
- `tests-lint` — `CHUNK_HOOK_ENABLE=1`, `CHUNK_HOOK_ENABLE_TESTS=1`, `CHUNK_HOOK_ENABLE_LINT=1`.
- `review` — `CHUNK_HOOK_ENABLE=1`, `CHUNK_HOOK_ENABLE_REVIEW=1`.

**Shell startup file management** uses idempotent marker+value blocks:

- `upsertManagedBlock(file, marker, value)` — finds a line starting with `marker`, and on the
  next line writes `value`. If the marker doesn't exist, both lines are appended. If the value
  has changed, only the value line is replaced.
- Marker: `# chunk-hook env` (for `source` line).

The command does not require `CHUNK_HOOK_ENABLE` — it is a setup utility.

### Placeholders

Both task and exec expand `{{...}}` placeholders (no flag needed — if a template contains
no patterns,
expansion is a no-op), but they support **different scopes**:

**Task** (instructions and check-block messages) — full resolution chain:

1. **Triggering event overlay** — the current event's input is merged in-memory under its event name
   (e.g., `Stop`). This means `{{Stop.transcript_path}}`, `{{Stop.session_id}}`, etc. resolve
   automatically without an explicit `state save` hook. Hooks on the same event run in
   parallel, so a
   separate `state save` cannot reliably precede the current command — the overlay guarantees
   availability. The overlay is in-memory only; the persisted state file is not modified.
2. **Saved state fields** — dot or bracket-notation path into event-namespaced state persisted by
   earlier `state save` / `state append` calls. Bracket notation accesses specific entries:
   `{{UserPromptSubmit[0].prompt}}` (first entry), `{{UserPromptSubmit[1].prompt}}` (second).
   Dot notation `{{UserPromptSubmit.prompt}}` is sugar for `{{UserPromptSubmit[0].prompt}}`.
3. **Git placeholders** — `{{CHANGED_FILES}}` and `{{CHANGED_PACKAGES}}` (computed from git,
   deletions excluded — see [Git helpers](#git-helpers-gitts) below).
4. **Unresolved** — replaced with empty string.

When the triggering event and saved state have the same field, the live event value wins.

**Exec** (shell commands) — git placeholders only:

- `{{CHANGED_FILES}}` and `{{CHANGED_PACKAGES}}` are substituted in the command string.
- State fields and event overlay are **not** available in exec commands.

### Block limits and counter semantics

`--limit N` caps consecutive blocks before auto-allowing. The counter resets only when a check
evaluates the result as `pass` — **not** on re-run. This ensures the block limit is reachable even
in the delegation pattern (check → block → re-run → check).

**Only actionable failures increment the counter.** The check result is a 4-state discriminated union:

| Result | Meaning | Counter | Helper |
| --- | --- | --- | --- |
| `pass` | Command succeeded | Reset | — (exit 0) |
| `fail` | Command failed / task blocked | Increment | `blockWithLimit()` |
| `missing` | No result file, or stale (session/content mismatch) | No change | `blockNoCount()` |
| `pending` | Command still running | No change | `blockNoCount()` |

`blockNoCount(tag, adapter, reason)` blocks (exit 2) **without** touching the
counter — for transient states where the agent needs to wait, not fix anything.
`blockWithLimit(tag, config, name, limit, reason)` increments the counter and
auto-allows when the limit is exceeded. On auto-allow, it records `"pass"`
in coordination so that the group sentinel can be updated when all commands have passed
(including via auto-allow).

**Pending timeout:** If a pending command exceeds `timeout` seconds (default 300 for exec, 600 for
task), the check removes the stale sentinel, increments the block counter, and blocks with an explicit
timeout message. This prevents stuck commands from blocking indefinitely.

| Scenario | `limit` unset (0) | `limit` = N |
| --- | --- | --- |
| **Stop event** | 1 block, then allow | N blocks, then auto-allow |
| **Other events** | Block indefinitely | N blocks, then auto-allow |

**Stop events** are special: Claude Code re-fires Stop with `stop_hook_active=true` when a Stop event
blocks. When `limit > 0`, the CLI defers to `blockWithLimit` and the Stop event follows the same
N-block limit as any other event. When `limit = 0` (unlimited), `guardStopEvent()` auto-allows to
prevent an infinite loop — giving Stop a default "1 block, then allow" behavior.

Defaults: exec `limit: 0` (unlimited), task `limit: 3`.

### Change detection

By default, execs and tasks **skip (exit 0) when no relevant changes exist:**

- **Exec:** Skips if no files matching `--file-ext` have changed
  (or no changes at all if `--file-ext` is omitted).
- **Task:** Skips if the composite fingerprint has not changed since the baseline recorded on the
  first `state save`/`append` for `UserPromptSubmit`. This prevents review from firing on
  question-only interactions. **This logic is consistent between standalone `task check` and `task`
  specs inside `sync check`.**

Modifiers:

- `--always` — force execution regardless of changes.
- `--staged` — narrow to staged changes only (both exec and task).
- `--file-ext` — filter by file extension (exec only; tasks operate on full diff via instructions).

### Content-hash staleness

Sentinels record a `contentHash` — a SHA-256 digest of the working-tree diff at the time the
command was executed. When a sentinel is later evaluated (via `evaluateSentinel` in `check.ts`),
the current diff hash is compared against the stored hash. If they differ, the sentinel is
treated as **missing** (stale), forcing the command to re-run against the current code.

This prevents **bait-and-switch** attacks: an agent cannot run tests against harmless code,
obtain a "pass" sentinel, then swap in different code and have the sentinel still pass.

**How it works:**

1. `computeFingerprint({ cwd, staged?, fileExt? })` in `git.ts` computes
   `sha256(HEAD + "\n" + git_diff)` — optionally using `--cached` for staged changes and a
   pathspec filter for `--file-ext`. The HEAD commit is always included, so moving to a new
   commit invalidates the fingerprint even without working-tree changes.
2. `exec run --no-check` computes the fingerprint after the command completes and stores it in
   the sentinel's `contentHash` field.
3. `exec check`, `sync check`, and `exec run` (full mode) recompute the fingerprint and pass it
   to `evaluateSentinel()`, which returns `"missing"` when the fingerprints differ.
4. Task sentinels do not use content hashes — tasks operate on full-diff instructions and use
   session-based staleness instead.

### Matcher filter (`--matcher`)

`--matcher <pattern>` restricts a hook to specific tool names — the CLI-side equivalent of the
hook configuration's `matcher` field. When set, the CLI auto-allows (exit 0) any event whose
tool name does not match the pattern — after scope activation but before trigger matching or
any other logic.

**Why this exists:** Claude Code's hook configuration supports a `matcher` field that filters
`PreToolUse` / `PostToolUse` hooks by tool name (e.g., `"matcher": "Bash"`). However, **VS Code
Copilot ignores matcher values and sends all tool events through all hooks.** This is officially
documented: _"Currently, VS Code ignores matcher values, so hooks apply to all tools."_
([source](https://code.visualstudio.com/docs/copilot/customization/hooks#_how-does-vs-code-handle-claude-code-hook-configurations)).
Without `--matcher`, a `PreToolUse/Bash` hook would fire for `read_file`, `create_file`,
`run_in_terminal`, `runSubagent`, etc. — causing total deadlock when the hook blocks.

The `--matcher` flag moves the filtering from the host (which ignores it) into the CLI itself.

**Pattern syntax:**

- Single tool name: `--matcher Bash` — matches tool names containing "Bash".
- Pipe-separated: `--matcher TaskUpdate|TodoWrite` — same `|` syntax as the hook `matcher` field.
- Any valid JS regex: `--matcher "Edit|Write|MultiEdit"`.

The pattern is tested via `RegExp.test()` against the event's tool name. It is a contains-match,
not an exact match (e.g., `--matcher Edit` matches both `Edit` and `MultiEditTool`).

**Placement:** `--matcher` is parsed in `index.ts` (not in command modules). It runs **after**
scope activation — `activateScope()` is called first for every tool event, then `--matcher`
filters non-matching tools. This ordering is deliberate: in VS Code multi-root workspaces, many
tool calls (file edits, reads, searches) carry file paths that identify the target repo but are
not shell tools. If `--matcher` ran first, those events would exit before `activateScope()` could
inspect their paths, and the scope would never re-activate after an external marker deletion.
By running activation first, every tool call with repo-matching paths keeps the scope alive,
while `--matcher` still prevents non-matching tools from triggering expensive checks.

**Native matcher:** The default template sets the PreToolUse native `matcher` to `"*"` (match all
tools). This ensures hooks fire for every tool event so `activateScope()` always runs. The CLI
`--matcher` flag then narrows which tools trigger the actual check/run logic. In Claude Code
`"*"` is the documented wildcard; in VS Code Copilot the native matcher is ignored anyway.

**Both commands:** Available on both `exec check` and `task check`. Not passed through to
`exec run --no-check` (the delegated run has no tool context).

**When to use:** Always include `--matcher` when a hook targets specific tool types (e.g., shell
commands). This ensures correct behavior across all IDEs: Claude Code (where native `matcher`
works), VS Code Copilot (where it doesn't), and Cursor (which reads settings.json directly).
Hooks without a tool-type filter (e.g., `Stop`, `SessionStart`, `UserPromptSubmit`) do not
need `--matcher`.

## Development Environment

- **Runtime:** Bun 1.x (not Node.js). Use `bun` for all commands.
- **Package manager:** `bun install` (lockfile: `bun.lock`).
- **Type-check:** `bun run typecheck` — **Lint:** `bunx biome check`
- **Test:** `bun test packages/hook/` — **Build:** `bun run build` (from root)
- All generated code must pass typecheck and biome lint without errors before completing work.

## Code Style

- Use `type` over `interface` for object shapes (project convention).
- Use `export type` for type-only exports (`verbatimModuleSyntax` is enabled).
- No `.js` extensions in import specifiers (Bun resolves `.ts` directly).
- Relative imports only — no path aliases.
- Place helper functions and types **after** their first usage
  (public → private, caller → callee).
- Keep dependencies minimal. The only runtime dependency is `yaml`.
- Formatter: Biome (tabs, 100-char line width).
- Use `as Type` casts instead of `!` non-null assertions (biome `noNonNullAssertion` rule).

### Logging conventions

- **Name-qualified tags.** Each command uses `ntag(name)` to produce tags like `exec:<name>` or
  `task:<name>`. This makes log output filterable by command name in multi-hook setups.
- **Entry log.** Every command entry logs `subcommand=<sub> event=<event> tool=<tool>` for immediate
  context.
- **Result format.** Check paths log `Result: <kind> → action: <allow|block>` so the outcome and
  enforcement action are visible in a single line. Include parenthetical detail when helpful (e.g.,
  `Result: fail (exit 1) → action: block (agent must fix and re-run)`).
- **Trigger mismatch.** When a trigger pattern doesn't match, log the patterns and (for Bash events)
  a truncated command summary so mismatches are diagnosable.
- **Block counter.** Log `Action: block (<count>/<limit>) — agent must re-run`
  so the counter state is visible. For transient blocks,
  log `Action: block (no counter increment — transient state)`.

## Architecture

```text
packages/hook/
├── src/
│   ├── index.ts            # Entry point: Commander-based command registration
│   ├── commands/
│   │   ├── env-update.ts   # Env update command (shell environment configuration)
│   │   ├── exec.ts         # Exec command (check/run subcommands)
│   │   ├── repo-init.ts    # Repo init command (template file installation)
│   │   ├── scope.ts        # Scope command (activate/deactivate — per-repo activity gate)
│   │   ├── state.ts        # State command (save/load/clear subcommands)
│   │   ├── sync.ts         # Sync command (grouped sequential checks)
│   │   └── task.ts         # Delegated task command (check subcommand)
│   ├── lib/
│   │   ├── adapter.ts      # HookAdapter strategy pattern (provider abstraction)
│   │   ├── compat.ts       # IDE compatibility — event/tool/field normalization (Cursor, VS Code)
│   │   ├── env.ts          # CHUNK_HOOK_* env vars, resolveProject() (--project flag resolution)
│   │   ├── config.ts       # YAML config loader (.chunk/hook/config.yml; execs + tasks)
│   │   ├── hooks.ts        # Low-level stdin JSON parsing (consumed by adapter.ts)
│   │   ├── placeholders.ts # {{Key.path}} placeholder expansion for task instructions
│   │   ├── sentinel.ts     # Result-file CRUD, persistent sentinels, block counters, contentHash
│   │   ├── shell-env.ts    # Shell environment utilities (env file, startup files, profiles)
│   │   ├── state.ts        # Per-project state (event-namespaced persistence)
│   │   ├── templates.ts    # Embedded template files for repo init
│   │   ├── check.ts        # Shared check helpers (evaluate, block, guard, trigger matching)
│   │   ├── task-result.ts  # Task result validation and sentinel conversion
│   │   ├── proc.ts         # Bun.spawn wrapper with timeout
│   │   ├── git.ts          # Changed files, placeholder substitution, fingerprinting
│   │   └── log.ts          # File-based logger
│   └── __tests__/
│       ├── adapter.test.ts
│       ├── check.test.ts
│       ├── compat.test.ts
│       ├── config.test.ts
│       ├── env.test.ts
│       ├── git.test.ts
│       ├── hooks.test.ts
│       ├── integration.test.ts
│       ├── log.test.ts
│       ├── placeholders.test.ts
│       ├── repo-init.test.ts
│       ├── scope.test.ts
│       ├── sentinel.test.ts
│       ├── shell-env.test.ts
│       ├── state.test.ts
│       └── task-result.test.ts
├── examples/               # Example configurations
│   ├── .chunk/hook/config.yml                    # Fully commented Go config example
│   └── .claude/
│       ├── settings.review-example.json           # Review-only hook config
│       └── settings.test-lint-example.json        # Test+lint-only hook config
├── package.json
└── tsconfig.json
```

### Git helpers (`git.ts`)

`getChangedFiles()` returns file paths that exist on disk. Key design decisions:

- **Deleted files are excluded.** `{{CHANGED_FILES}}` feeds paths to shell commands (e.g.,
  `go test {{CHANGED_FILES}}`). Including deleted paths causes "file not found" errors.
  - Staged path: `git diff --cached --name-only --diff-filter=ACMR` — only added, copied,
    modified, renamed.
  - Non-staged path: `git status --porcelain -uall | grep -vE '^D.|^.D'` — excludes any line
    with `D` in either the index or worktree status column.
- **Quoted filenames are unquoted.** `git status --porcelain` wraps filenames containing spaces
  or special characters in double-quotes. The parser strips surrounding quotes so that `fileExt`
  filtering works correctly.
- **Rename handling differs by path.** Staged uses `--name-only` which outputs only the new name.
  Non-staged uses `sed 's/.* -> //'` to extract the new name from `git status`'s `old -> new` format.
- **Pipeline exit codes.** When `grep -vE` filters out ALL lines (all changes are deletions), it
  exits 1, but `sed` at the end of the pipeline exits 0. This produces empty output → `[]`, which
  is correct.

When modifying `getChangedFiles`, always test both paths (staged and non-staged) against: renames,
deletions, filenames with spaces, and the all-deletions edge case. The output-parsing code is shared
between both paths, so unit tests should verify parsing once (non-staged) and use command-string
assertions to confirm the correct git command is selected per path — avoid duplicating parsing tests
for both `stagedOnly: true` and `false`.

`hasUncommittedChanges()` uses `git status --porcelain -uall` — **not** `git diff HEAD --stat`.
`git diff HEAD` only compares _tracked_ files against HEAD and misses brand-new untracked files.
`git status --porcelain` covers staged, unstaged, and untracked files, which is critical for
skip-if-no-changes logic. `hasStagedChanges()` correctly uses `git diff --cached --stat` because
staged files are tracked by definition.

**Design rules:**

- `lib/` modules are pure utilities — they do not call `process.exit()` except via `HookAdapter`
    (`allow()`/`block()` in `adapter.ts`).
- `commands/` orchestrate: enable check → trigger match → change detection → sentinel read/write → response.
- Both commands share the same state machine via `check.ts`: missing → pending → pass → fail.
- `index.ts` handles all arg parsing via Commander — commands receive typed flag objects.
- Core logic never inspects event names, tool names, or provider env vars — it calls adapter
    behavioral methods instead (see [HookAdapter](#hookadapter-strategy-pattern) below).

## Agent-Facing Messages

`respondBlock(reason)` writes to stderr and exits 2. Stderr content is fed directly
to the agent as an error prompt — **all block messages are agent-facing prompts.**

When composing block reasons:

- **Directive tone:** "Fix the issues and retry." not "Please fix the issues."
- **Structured:** Use labeled sections (`Instructions:`, `Output format:`,
  `Output:`) for multi-part messages.
- **Concise:** Every word costs tokens. Remove filler.
- **Actionable:** Include file paths, commands, schema references.
- **Clear next step:** "Retry after the command completes." or "Fix the issues and retry."

## Configuration

- Per-repo YAML config: `.chunk/hook/config.yml` (execs, triggers, task settings).
- Environment variables (`CHUNK_HOOK_*`) always override YAML values.
- Disabled by default — require `CHUNK_HOOK_ENABLE=1` to activate.

### Directory conventions

Config files live in `.chunk/hook/` at the repo root — **not** inside `.claude/` or any other
agent-specific directory. This keeps chunk hook agent-agnostic: the same config works whether
hooks are driven by Claude Code, OpenAI Codex, or any future agent runtime.

Only agent-specific hook wiring (e.g., `.claude/settings.json`) belongs in the agent's own directory.
Everything else — YAML config, instruction files, schemas — goes in `.chunk/hook/`.

The `.chunk/hook/` namespace prevents collision with existing `.chunk/context/` (AI agent context
files) and future `.chunk/` subdirectories.

### Example invocation modes

**Exec — direct, fast command** (`PostToolUse` lint):
Run inline with no delegation. Best for sub-minute commands.

```sh
chunk hook exec run lint
```

**Exec — delegated full suite** (`Stop` tests):
Full test suite via the delegation pattern — `check` blocks until the agent runs the command.

```sh
chunk hook exec check tests
```

**Exec — delegated with template** (`PreToolUse` tests-changed):
Incremental tests using `{{CHANGED_PACKAGES}}` — only changed Go packages are tested.

```sh
chunk hook exec check tests-changed
```

**Exec — hook-level flag override** (`PreToolUse/Bash` with `--staged`):
The `--staged` flag narrows change detection and placeholder expansion to staged files only. Set on
the hook command, not in YAML — the same exec can run with or without `--staged` depending on the
event.

```sh
chunk hook exec check tests-changed --staged --on pre-commit
```

**Task — delegated review with state** (`Stop` review):
State is appended on `UserPromptSubmit` to capture the original prompt (and any subsequent
"Continue" prompts). On `Stop`, a task check blocks with instructions that expand
`{{UserPromptSubmit.prompt}}` and `{{CHANGED_FILES}}` placeholders. A subagent performs the review
and writes a structured result.

```sh
chunk hook state append                         # UserPromptSubmit hook
chunk hook task check review                    # Stop hook
chunk hook state clear                          # SessionEnd hook
```

## HookAdapter (strategy pattern)

The `HookAdapter` abstraction (`adapter.ts`) encapsulates all provider-specific hook I/O and event
semantics. Core logic (exec, task, state, check) calls adapter methods — it never inspects event
names, tool names, or provider env vars directly. Adding a new provider means implementing one
adapter; no changes to core logic.

### Why

Claude Code, Cursor, and GitHub Copilot all share the same hook protocol (stdin JSON, exit codes
0/1/2) but differ in event names (`PreToolUse` vs `preToolUse`), tool names (`Bash` vs `Shell`),
and env vars (`CLAUDE_PROJECT_DIR` vs TBD). Without the adapter, these strings were hardcoded
throughout `check.ts`, `hooks.ts`, `state.ts`, and `placeholders.ts`.

### Key types

- **`AgentEvent`** — normalized event shape with `eventName`, `toolName`, `toolInput`, `cwd`, and
  `raw` (full provider-specific payload). All providers map into this shape.
- **`HookAdapter`** — the strategy interface. Methods:
  - **I/O:** `readEvent()`, `allow()`, `block(reason)`
  - **Behavioral queries:** `isStopRecursion(event)`, `isShellToolCall(event)`,
    `getShellCommand(event)`, `stateKey(event)`, `commandSummary(event)`
  - **Env:** `getProjectDir()`

### Shared I/O base

`createStdinExitCodeBase()` returns the three I/O methods (`readEvent`, `allow`, `block`) that are
identical across all current providers. Provider-specific adapters spread this base and add behavioral
methods. If a future provider uses a different transport, it implements `readEvent` from scratch.

### Project directory resolution

The project directory is resolved via the **`--project` flag** (preferred) or the hook payload's
`cwd` field. In multi-root VS Code workspaces, `event.cwd` and `CLAUDE_PROJECT_DIR` are pinned to
a single session-wide repo (bugs #8559, #12808), so `--project` is the reliable source.

Resolution order in `index.ts` handlers (via `resolveProject()` in `env.ts`):

1. **`--project <value>`** — if absolute path, use directly; if relative name, join with
   `CHUNK_HOOK_PROJECT_ROOT`.
2. **`event.cwd`** — from stdin JSON (fallback when no `--project` flag).
3. **`CLAUDE_PROJECT_DIR` → `process.cwd()`** — last resort.

### IDE Compatibility (`compat.ts`)

The `compat.ts` module centralizes all provider-specific normalization. The adapter delegates to
compat helpers instead of hardcoding string comparisons. Each workaround is annotated with the
provider it addresses.

**Event name normalization** — `normalizeEventName(name)`:

| Provider | Input | Output |
| --- | --- | --- |
| Claude Code | `PreToolUse`, `Stop`, `UserPromptSubmit` | Pass-through (canonical) |
| Cursor | `preToolUse`, `stop`, `beforeSubmitPrompt` | `PreToolUse`, `Stop`, `UserPromptSubmit` |
| VS Code Copilot | `PreToolUse`, `Stop`, `UserPromptSubmit` | Pass-through (same as Claude Code) |

Cursor renames `UserPromptSubmit` → `beforeSubmitPrompt` — this is a full alias, not just a
casing change. The `EVENT_NAME_CANONICAL` map handles this.

**Tool name normalization** — `isShellTool(toolName)`:

| Provider | Shell tool name | Matched? |
| --- | --- | --- |
| Claude Code | `Bash` | Yes |
| Cursor | `Shell` | Yes |
| VS Code Copilot | `run_in_terminal` | Yes |

The `--matcher` regex pattern in `settings.json` must include all three variants:
`Bash|Shell|run_in_terminal`.

**Hook input field normalization** — `mapHookInputToEvent(input)`:

VS Code Copilot sends camelCase for some fields (`hookEventName`, `sessionId`) while keeping
others in snake_case (`tool_name`, `tool_input`, `cwd`). This function normalizes both variants
into a single `AgentEvent` shape, preferring snake_case and falling back to camelCase.

**Session ID extraction** — `extractSessionId(raw)`:

Normalizes provider-specific session ID fields:
- Claude Code: `session_id` (snake_case, highest priority)
- VS Code Copilot: `sessionId` (camelCase)
- Cursor: `conversation_id` (stable conversation identifier, lowest priority)

Cursor's `sessionStart` event sends **both** `session_id` and `conversation_id` (same UUID value).
However, `preToolUse` and most other events send only `conversation_id` — no `session_id` field.
The fallback chain ensures scope activation works for all Cursor events.

**Cursor hook payload — common schema** (per [official docs](https://cursor.com/docs/agent/hooks)):

All Cursor hook events receive these base fields (in addition to event-specific fields):
`conversation_id`, `generation_id`, `model`, `hook_event_name`, `cursor_version`,
`workspace_roots`, `user_email`, `transcript_path`.

**Verbose payload logging:**

When `CHUNK_HOOK_VERBOSE` is set, `activateScope()` logs the full raw stdin JSON payload via
`logVerbose()`. This makes future field-mapping issues immediately diagnosable without code
changes — run with verbose mode and the complete payload is visible in the log.

**Stop-hook-active flag** — `isStopHookActive(raw)`:

VS Code Copilot sends `stopHookActive` (camelCase) instead of `stop_hook_active` (snake_case).
This helper normalizes both forms, preferring snake_case.

**Where compat is used:**

- `adapter.ts` — `isStopRecursion()`, `isShellToolCall()`, `getShellCommand()`, `stateKey()`,
  `mapHookInputToEvent()`, and `isStopHookActive()` all delegate to compat helpers.
- `index.ts` — `extractSessionId()` replaces inline `session_id ?? sessionId` patterns.

**Adding new provider workarounds:**

1. Add the mapping to `compat.ts` with a comment naming the provider.
2. Add tests in `compat.test.ts` under a describe block for the provider.
3. If a new tool name is needed, add it to the appropriate `Set` in `compat.ts` and update
   `--matcher` patterns in hook configuration.

### Adapter vs compat: when to use which

Use **`compat.ts`** when a new provider sends the same data under a different field name or casing
(field normalization). Use a **new adapter** when a provider differs in transport, event semantics,
or behavioral logic (e.g., different stdin format, different exit-code meaning, new side effects).

### Adding a new provider

1. Create `createFooAdapter()` in `adapter.ts` — spread `createStdinExitCodeBase()`, implement
   behavioral methods with the provider's event/tool names.
2. Update `getAdapter()` with detection logic (e.g., env-var sniffing).
3. No changes to `exec.ts`, `task.ts`, `state.ts`, `check.ts`, or `placeholders.ts`.

### Design decisions

- **Behavioral methods, not identity checks.** The adapter exposes `isShellToolCall(event)` rather
  than `getProviderName()`. Core logic asks "is this a shell tool call?" — it never asks "which
  provider are we using?" This prevents `if (provider === "cursor")` branches in core logic.
- **`stateKey()` owns namespace mapping.** State is saved under `adapter.stateKey(event)` (currently
  `event.eventName`). A future adapter could map `beforeSubmitPrompt` → `UserPromptSubmit` to keep
  state keys stable across providers, or use provider-native names — the decision is per-adapter.
- **Placeholders resolve from `event.raw`.** The triggering event overlay uses `event.raw` (the full
  provider-specific payload) so all fields are available in templates.

## Documentation

- Exported symbols must have JSDoc comments with proper capitalization and punctuation.
- Private/internal symbols: document only when non-obvious.
- File-level JSDoc block at the top of each module.
- Use `// ---` section separators with labels in longer files.
- Do not generate separate markdown docs unless explicitly asked.

## Testing

- Test runner: `bun test`. Test files: `src/__tests__/*.test.ts`. Import from `"bun:test"`.
- One behavior per test case. **Test exported/public functions** — private functions are tested indirectly.
- Temp directories: `os.tmpdir()` + unique path, clean up in `afterEach`.
- Env var mutation: save/restore in `afterEach` using a `saved` map pattern (see existing tests).
- No HTTP mocking needed — no HTTP calls. Unit tests focus on pure logic.
- Functions that call `process.exit()` are not directly unit-testable —
  test the logic that feeds into them.

## Sentinel System

Sentinels are JSON files in a temp directory that record exec/task outcomes:

- **Deterministic IDs:** `sha256(projectDir:commandName)` — same command + project
    always maps to the same file.
- **Persistent sentinels:** `exec check` and `task check` **do not** consume (delete) their
    sentinel on pass — sentinels persist as a cache. Staleness is detected by comparing the
    sentinel's `sessionId` and `contentHash` against the current session and working-tree state.
    A mismatch causes the sentinel to be treated as missing, forcing a re-run. Fail sentinels
    also persist — they remain for the next check to report the failure and prompt a re-run.
- **Session-aware staleness:** Sentinels carry a `sessionId` copied from the scope marker
    at write time. When a check runs, it compares the sentinel's `sessionId` to the current
    scope marker's `sessionId`. A missing or mismatched `sessionId` means the sentinel is stale
    and is treated as missing, forcing the command to re-run with fresh context.
- **Group sentinels (sync):** `sync check` maintains a separate `sync-<hash>.json` tracking which
    specs have passed. Individual sentinels persist (not consumed) as each spec passes. On fail,
    the default behavior (`--on-fail restart`) removes the group sentinel and the individual
    sentinel, restarting the entire sequence. With `--on-fail retry`, only the failed spec's
    sentinel is removed from the group — previously-passed specs are preserved.
- **Block counters:** Separate `.blocks` files track consecutive blocks for `--limit`
    enforcement. Only actionable failures (fail/timeout) increment the counter; transient
    states (missing/pending) do not.
- **Task results:** The agent writes
    `{ "decision": "allow" | "block", "reason": "..." }` to the sentinel path.
    `readTaskResult()` reads without deleting.
    Only `decision` is validated; the rest is opaque pass-through.
- **Named commands:** Both exec and task commands are named — each name gets its own
    sentinel file and block counter.

**Note — ordering with multiple checks:** Sentinels persist on pass, so standalone checks no
longer race or ping-pong. However, **use `sync check` to group multiple checks on the same
event** — it ensures correct ordering and combines non-pass results into a single block message.

## Common Patterns

### Adding a new CLI flag

1. Add to the `*Flags` type in the command file (`exec.ts` or `task.ts`).
2. Parse in `index.ts` in the appropriate Commander command definition.
3. Document in `README.md` under the relevant command section.

**Global flags** (parsed in the parent command, available to all subcommands): `--project <name|path>`.
The `--project` flag is not command-specific — it is extracted before command dispatch and used
by `resolveProject()` to override the project directory for config loading and scope checks.

### Adding a new lib module

1. Create `src/lib/<name>.ts` with a file-level JSDoc block.
2. Export only what consumers need — keep internals private.
3. Create `src/__tests__/<name>.test.ts` for exported functions.
4. Update the Architecture section in `README.md`.

### Adding a new config option

1. Add to the YAML type in `config.ts` and the resolved config type.
2. Set a sensible default in the config loader.
3. Add env-var override if applicable (in `env.ts`).
4. Document in `README.md` under Configuration.

### Adding a new template

1. Add the template content as an exported `string` constant in `src/lib/templates.ts`.
2. Add an entry to the `TEMPLATE_FILES` manifest array with `path`, `content`, and `substitute` flag.
3. If the template contains `__PROJECT__`, set `substitute: true` so `repo init` replaces it.
4. Add a test case in `src/__tests__/repo-init.test.ts` verifying the file is created.
5. Update the `repo init` section in `README.md`.

## Commit Messages

- Title: semantic commit style, imperative mood, ≤ 50 chars, no trailing period
- Body: open with a concise summary of the change, then details/bullets
- ≤ 72 char lines, valid Markdown (backticks for code/config)
- Explain **what** and **why**, not how — bullet per significant change
- Flag breaking changes, compatibility issues, or caveats when applicable

## Security Model

### `contentHash` (fingerprint) enforcement

Sentinels record `contentHash = sha256(HEAD + "\n" + git diff HEAD)`. At check time the current
fingerprint is recomputed and compared. Mismatch → sentinel treated as stale ("missing") →
command must re-run. This blocks bait-and-switch, staged-swap, and history-rewrite attacks.

### Push-time detection

Exec `detectChanges()` uses `git status --porcelain` — always empty at push time. Push events
(`git push`) bypass the `detectChanges` short-circuit and fall through directly to
sentinel/fingerprint validation. Task is not affected: it uses fingerprint-based comparison
(`precomputeTaskNoChanges`) which includes HEAD in the hash, so a changed HEAD after
revert/plumbing is detected automatically.

### `detectChanges` is a performance optimisation, not a security gate

It skips sentinel I/O and hash computation for tool calls that touch unrelated files. The
fingerprint (`contentHash`) is the security-critical mechanism.

### Known limitations

- **`fileExt` gap:** `--file-ext .go` only monitors `.go` files; other extensions are unprotected.
- **Config self-sabotage:** Agent can modify `.chunk/hook/config.yml` or `.claude/settings.json`.
- **Trigger allowlist:** Unlisted commands (`git revert`, `git cherry-pick`) bypass hooks entirely.
- **Git plumbing:** `hash-object`/`write-tree`/`commit-tree`/`update-ref` create commits without
  triggering tool calls.
