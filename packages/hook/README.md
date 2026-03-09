# chunk hook

A Bun-based CLI that provides configurable exec, task, sync, state, scope, repo, and env commands for
[Claude Code](https://docs.anthropic.com/en/docs/claude-code) and compatible IDEs.

## Supported Environments

| IDE | Status | Notes |
| --- | --- | --- |
| **Claude Code** (CLI / terminal) | Fully supported | Canonical provider — all features work natively |
| **Cursor** | Supported | Reads `.claude/settings.json` directly; event/tool names are auto-normalized (see [Cursor Compatibility](#cursor-compatibility)) |
| **VS Code** (GitHub Copilot with Claude) | Supported with workarounds | Ignores `matcher` field, fires all hooks for all repos in multi-root workspaces (see [VS Code Compatibility](#vs-code-compatibility)) |

All environments share the same hook protocol (stdin JSON + exit codes 0/1/2) and the same
`.claude/settings.json` configuration. Provider-specific differences are handled internally
by the compatibility layer (`compat.ts`) — no per-IDE configuration is needed.

## Overview

`chunk hook` enforces quality checks before Claude Code commits, stops, or completes tasks.
It communicates via stdin/stdout JSON and uses a subcommand-based interface.

Execs and tasks are **named and user-defined** — configure as many as you need (tests, lint, format,
typecheck, code review, etc.) in a single YAML file.

Activation: Exec and Task are disabled by default and require `CHUNK_HOOK_ENABLE` (or per-command overrides).
The State command is infrastructure and always available — it does not require `CHUNK_HOOK_ENABLE`.

## Installation

### 1. Install the `chunk` binary

The hook commands ship as part of the `chunk` CLI. Install it first:

```bash
# Production install (downloads pre-built binary via gh CLI):
gh api -H "Accept: application/vnd.github.v3.raw" \
  "/repos/CircleCI-Public/chunk-cli/contents/install.sh" | bash

# Or build from source (for development):
cd chunk-cli
./install-local.sh
```

Both methods install the binary to `~/.local/bin/chunk`. Verify:

```bash
chunk --version
```

### 2. Configure the shell environment

Set up `CHUNK_HOOK_*` environment variables and shell sourcing:

```bash
# Default setup (all hooks enabled):
chunk hook env update

# Enable only tests + lint (no review):
chunk hook env update --profile tests-lint

# Enable only review:
chunk hook env update --profile review

# Disable all hooks (install binary but leave hooks inactive):
chunk hook env update --profile disable
```

Restart your terminal (or `source ~/.config/chunk-hook/env`) after setup.

#### Environment options

```bash
# Custom log directory:
chunk hook env update --set-log-dir ~/my-logs

# Verbose mode:
chunk hook env update --set-verbose
```

#### Quick enable/disable without re-running setup

```bash
echo 'export CHUNK_HOOK_ENABLE=0' > ~/.config/chunk-hook/env   # disable all
echo 'export CHUNK_HOOK_ENABLE=1' > ~/.config/chunk-hook/env   # enable all
```

### 3. Initialize a repo

See [Quick Start](#quick-start) below.

## Quick Start

Once installed, initialize a repo with hooks and config:

```bash
chunk hook repo init
```

This creates:

```text
.chunk/hook/                        ← agent-agnostic config
  config.yml                        ← edit command: fields for your repo
  code-review-instructions.md       ← review prompt template
  code-review-schema.json           ← review result schema
.claude/                            ← Claude Code hooks
  settings.json                     ← hook wiring (tests, lint, review)
```

If files already exist, templates are saved as `<name>.example.<ext>` instead of overwriting.

**Next steps after `repo init`:**

1. Edit `.chunk/hook/config.yml` — set the `command:` fields for your repo's tools:

   ```yaml
   execs:
     tests:
       command: "go test ./..."      # or: npm test, pytest, etc.
       fileExt: ".go"                # or: .ts, .py, etc.
   ```

2. Review `.claude/settings.json` — adjust hook matchers and timeouts if needed.
3. Review `.chunk/hook/code-review-instructions.md` — customize the review prompt.
4. Run Claude Code — the hooks fire automatically.

```bash
# Manual test (from any repo)
CHUNK_HOOK_ENABLE=1 echo '{}' | chunk hook exec run tests --cmd "go test ./..."
```

## Commands

| Command | Subcommands | Purpose |
| --- | --- | --- |
| `exec` | `run`, `check` | Run any named shell command (tests, lint, etc.) |
| `task` | `check` | Delegate a task and enforce the result (e.g., code review) |
| `sync` | `check` | Group multiple exec/task checks into a single ordered sequence |
| `state` | `save`, `load`, `clear` | Manage per-project state for cross-event data sharing |
| `scope` | `activate`, `deactivate` | Per-repo activity gate for multi-repo workspaces |
| `repo` | `init` | Initialize a repository with config templates |
| `env` | `update` | Configure the user's shell environment (env file, PATH, login sourcing) |

### `exec run <name>`

Run the command, save the result, check → **fails on failure**.

- `--no-check` — Save the result but skip the check. Always exits 0. Use `exec check` later to enforce.

### `exec check <name>`

Deferred check: read a saved result (from `run --no-check`) and **fail on failure**.
Intended for Claude Code event handlers (PreToolUse, TaskCompleted, Stop).

- `--on <trigger>` — Only check when the event matches a named trigger group (e.g., `--on pre-commit`).
  Non-matching events are silently allowed.
- `--limit <n>` — Max consecutive blocks before auto-allowing. Default: 0 (unlimited).
  See [Blocking Behavior](#blocking-behavior-and-limits).
- `--matcher <pattern>` — Auto-allow events whose tool name does not match the regex pattern.
  **VS Code workaround:** VS Code Copilot ignores the hook `matcher` field and sends all tool
  events through all hooks. This flag moves filtering into the CLI. Always include `--matcher`
  when the hook uses a `matcher` field.

### `task check <name>`

Deferred check: read a previously saved task result and **fail on failure**. Intended for Claude Code
event handlers (PreToolUse, TaskCompleted, Stop). On first call (no saved result), blocks with task
instructions, result schema, and the file path where the subagent should write its result.

- `--on <trigger>` — Only check when the event matches a named trigger group (e.g., `--on pre-commit`).
  Non-matching events are silently allowed.
- `--limit <n>` — Max consecutive blocks before auto-allowing. Default: 3.
  See [Blocking Behavior](#blocking-behavior-and-limits).
- `--matcher <pattern>` — Same as `exec check --matcher` (see above).

> **Caveat:** Standalone `exec check` and `task check` self-consume their sentinel on pass. This
> works correctly when only **one** delegated check exists per hook event. When multiple standalone
> checks share the same event, they race and ping-pong (one allows, the other sees "missing" and
> re-blocks). Use `sync check` to group them.

### `sync check exec:<name> [task:<name>] ...`

Group multiple exec/task checks into a single ordered sequence. Use this whenever **two or more**
delegated checks share the same hook event.

```bash
chunk hook sync check exec:tests task:review --on pre-commit
```

Behavior:

1. Maintains a **group sentinel** tracking which specs have already passed.
2. Processes specs left-to-right. On pass: consumes the individual sentinel, advances.
3. On missing/pending: blocks with a directive to run that command. Resumes here next time.
4. On fail (default): removes the group sentinel, restarts the entire sequence on next invocation.
   With `--on-fail retry`: only the failed spec is removed from the group — previously-passed
   specs are preserved and the agent only needs to fix and re-run the failed command.
5. When all pass: removes the group sentinel, exits 0.

By default, sync evaluates all specs and combines non-pass results into a single block message,
giving the agent a complete picture of everything that needs attention in one round-trip.
With `--bail`, sync stops at the first non-pass spec and blocks immediately.

All flags (`--on`, `--trigger`, `--matcher`, `--limit`, `--staged`, `--always`, `--on-fail`,
`--bail`) are parsed once and passed through to all specs. Per-task `instructions` and `schema`
are read from `config.yml` — they cannot be overridden per-spec via CLI flags on `sync check`.

### `state save`

Read event input from stdin and save to per-project state, namespaced by event name.
The event name is read from the input JSON. All input fields are stored under the event namespace.

### `state load [field]`

Load a field from state and write to stdout. Supports dot notation to access event-namespaced fields
(e.g., `UserPromptSubmit.prompt`). Without a field argument, dumps the entire state as JSON.

### `state clear`

Remove all saved state for the project.

### `scope activate`

Read stdin JSON and activate the scope if the payload contains file paths referencing the current
project directory AND a session ID. Writes `.chunk/hook/.chunk-hook-active` with the session ID and
timestamp. Always exits 0 (exits 1 only on fatal write errors).

> **Note:** The `exec` and `task` handlers auto-activate the scope internally, so explicit `scope
> activate` hooks are not needed in the default template. Use this command only for hook groups that
> have no exec/task entry but still need to activate the scope.

### `scope deactivate`

Remove `.chunk/hook/.chunk-hook-active`. Use on `SessionStart` and `SessionEnd` to
reset the activity gate. Always exits 0.

### `repo init [dir]`

Initialize a repository with chunk hook configuration templates. Creates the `.chunk/hook/` and
`.claude/` directory structure with starter files:

- `.chunk/hook/config.yml` — hook configuration (execs, tasks, triggers)
- `.chunk/hook/.gitignore` — ignores runtime files (`.chunk-hook-active`, `*.result`, `*.state`)
- `.chunk/hook/code-review-instructions.md` — review prompt template
- `.chunk/hook/code-review-schema.yml` — review result schema
- `.claude/settings.json` — Claude Code hook registrations
  (with `__PROJECT__` replaced by the repo's directory name)

When a file already exists, a `.example` copy is written instead (e.g., `config.example.yml`),
so existing configuration is never overwritten. Use `--force` to overwrite all files unconditionally.

```bash
# Initialize current directory
chunk hook repo init

# Initialize a specific directory
chunk hook repo init /path/to/repo

# Force overwrite existing files
chunk hook repo init --force
```

### `env update`

Configure the user's shell environment for chunk hook. Creates an env file sourced on login
that exports `CHUNK_HOOK_*` variables.

- `--profile <name>` — Predefined variable set: `enable` (default), `disable`, `tests-lint`, `review`.
- `--env-file <path>` — Override env file location (default: `~/.config/chunk-hook/env`).
- `--set-log-dir <dir>` — Log directory to write into the ENV file
  (default: `~/Library/Logs/chunk-hook` on macOS,
  `~/.local/share/chunk-hook/logs` on Linux).
- `--set-project-root <dir>` — Multi-repo project root to write into the ENV file.
- `--set-verbose` — Enable verbose logging in the generated ENV.

The command:

1. Creates the log directory if it doesn't exist.
2. Writes the env file with profile-appropriate `CHUNK_HOOK_*` exports.
3. Adds a `source` line to shell startup files so the env file is loaded on login.

```bash
# Default setup (enable profile)
chunk hook env update

# Review-only profile
chunk hook env update --profile review

# Disable hooks
chunk hook env update --profile disable

# Custom log directory
chunk hook env update --set-log-dir /tmp/hook-logs
```

**Profiles:**

| Profile | Variables |
| --- | --- |
| `disable` | _(none — all CHUNK_HOOK_* vars removed)_ |
| `enable` | `CHUNK_HOOK_ENABLE=1` |
| `tests-lint` | `CHUNK_HOOK_ENABLE=1`, `CHUNK_HOOK_ENABLE_TESTS=1`, `CHUNK_HOOK_ENABLE_LINT=1` |
| `review` | `CHUNK_HOOK_ENABLE=1`, `CHUNK_HOOK_ENABLE_REVIEW=1` |

### Multi-Repo Workspace Support

In VS Code multi-root workspaces, Claude Code merges all `.claude/settings.json` files, so hooks fire
for every repo — even ones the agent hasn't touched. The scope gate prevents expensive checks
from running in inactive repos:

1. The `exec`, `task`, and `sync` handlers automatically call `activateScope()` before the
   `--matcher` filter and gate check — if the stdin payload contains file paths matching the
   project and a session ID, the scope is activated as a side effect. The default template sets
   the native `matcher` to `"*"` so hooks fire for **every** tool event (not just shell tools),
   ensuring file edits and reads keep the scope alive.
2. If `activateScope()` returns `false`, `exec` and `task` allow silently (exit 0), skipping
   expensive work.
3. Agent-invoked commands (`exec run --no-check`) skip the scope gate entirely — they run in
   the target repo via `process.cwd()` and do not need scope validation.
4. `SessionStart`/`SessionEnd` hooks call `chunk hook scope deactivate` to clear the gate.
5. For hook groups with no exec/task entry, `chunk hook scope activate` can be added explicitly.
6. The marker file stores a session ID — mismatched markers (from a different session, possibly
   a parallel agent) are treated as inactive for the current session.
7. **Subagent safety:** When the parent agent spawns a subagent (e.g., for code review), the
   subagent gets a different session ID. Its tool calls trigger the normal hook chain and call
   `activateScope()`, but the existing marker is **not overwritten** — the subagent is treated
   as active without clobbering the parent's session. This prevents a scope gap when control
   returns to the parent. The marker is only cleared by explicit `scope deactivate`.

In single-repo workspaces, every tool call matches, so the scope is always active — no behavior change.

## State and Placeholders

The **state** command and **placeholders** work together to enable cross-event data sharing — most
commonly capturing the user's original prompt and injecting it into task instructions.

State is **event-namespaced**: each `state save` stores the full event input under the event name
(e.g., `UserPromptSubmit`, `Stop`). Placeholders use **dot notation** to reference fields:
`{{UserPromptSubmit.prompt}}`.

### Example: Prompt-Aware Code Review

1. A `UserPromptSubmit` event saves the entire input to state:

   ```json
   { "command": "chunk hook state save" }
   ```

2. Task instructions reference the saved prompt via dot notation:

   ```markdown
   Review the changes for: {{UserPromptSubmit.prompt}}
   Focus on correctness, style, and potential issues.
   ```

3. A `Stop` event runs the task check (placeholders expand automatically):

   ```json
   { "command": "chunk hook task check review" }
   ```

4. A `SessionEnd` event cleans up state:

   ```json
   { "command": "chunk hook state clear" }
   ```

## Placeholders

Placeholders are `{{Key.path}}` patterns expanded in task and exec commands.
If a template contains no `{{...}}` patterns, expansion is a no-op.
Task and exec support **different scopes**:

| Scope | Task | Exec |
| --- | --- | --- |
| Triggering event (e.g. `{{Stop.transcript_path}}`) | Yes | No |
| Saved state (e.g. `{{UserPromptSubmit.prompt}}`) | Yes | No |
| Git (`{{CHANGED_FILES}}`, `{{CHANGED_PACKAGES}}`) | Yes | Yes |

### Built-in Placeholders (Task)

| Placeholder | Source | Description |
| --- | --- | --- |
| `{{Stop.transcript_path}}` | Event | Path to the conversation JSON |
| `{{Stop.session_id}}` | Event | Current session identifier |
| `{{Stop.stop_hook_active}}` | Event | Whether stop hook is already active |
| `{{CHANGED_FILES}}` | Git | Space-separated list of changed file paths (excludes deletions) |
| `{{CHANGED_PACKAGES}}` | Git | Deduplicated parent directories (excludes deletions) |
| `{{UserPromptSubmit.prompt}}` | State | The user's original prompt (requires `state save`) |
| `{{EventName.field.nested}}` | State | Any saved event field (dot notation) |

### Triggering Event (Implicit — Task only)

The current event's input is **always available** as placeholders — no `state save` needed. When a
`Stop` hook runs `chunk hook task check`, all fields from the `Stop` event input are automatically
accessible:

```markdown
# Instructions template
Transcript: {{Stop.transcript_path}}
Session: {{Stop.session_id}}
Changed files: {{CHANGED_FILES}}
```

This works because hooks on the same event run in parallel, so a separate `state save` hook cannot
reliably complete before the current command reads state. The triggering event is overlaid in-memory
without modifying the persisted state file.

### Cross-Event State (Explicit — Task only)

To reference data from a **different** event (e.g., the user's prompt in a `Stop` hook), use
`state save` on the earlier event:

```markdown
# Instructions template
User asked: {{UserPromptSubmit.prompt}}
Changed files: {{CHANGED_FILES}}
```

Deeply nested fields are supported: `{{PreToolUse.tool_input.command}}`.

### Exec Placeholders

Exec commands only support git placeholders: `{{CHANGED_FILES}}` and `{{CHANGED_PACKAGES}}`. State
fields and the triggering event overlay are not available in exec.

### Resolution Order (Task)

1. **Triggering event** (in-memory overlay of the current event's input)
2. **Saved state fields** (from earlier `state save` calls)
3. **Git placeholders** (`CHANGED_FILES`, `CHANGED_PACKAGES`)
4. **Unresolved** — replaced with empty string

When the triggering event and saved state have the same field, the live event value wins.

## Exit Codes

Aligned with [Claude Code hook conventions](https://code.claude.com/docs/en/hooks-guide#hook-output):

| Code | Meaning | Examples |
| --- | --- | --- |
| `0` | Pass / allow | Command succeeded, skipped (no changes), task passed |
| `2` | Block / fail | Tests failed, lint failed, command non-zero exit |
| `1` | Infra error | Cannot write result file, missing config, unexpected crash |

**Enforcing** commands (`check`, `run` without `--no-check`) use exit 0 with structured JSON decisions
per the Claude Code spec. **Non-enforcing** commands (`run --no-check`) always exit 0 — the result is
saved for later enforcement via `check`. Only infra errors (config issues, write failures) exit 1.

## Named Trigger Groups

Trigger groups let you control **when** a command fires. A trigger group is a list of substring
patterns matched against the command field of PreToolUse:Bash events.

Built-in triggers (always available, can be overridden):

| Trigger | Patterns |
| --- | --- |
| `pre-commit` | `"git commit"`, `"git push"` |

Custom triggers are defined in YAML and referenced by name with `--on`:

```yaml
triggers:
  pre-deploy:
    - "deploy"
    - "kubectl apply"
```

```bash
chunk hook exec check tests --on pre-deploy
```

When `--on` is omitted, the command fires on every event.

## Blocking Behavior and Limits

When a command blocks, the behavior depends on the event type and the `limit` setting:

| Scenario | `limit` unset (0) | `limit` = N |
| --- | --- | --- |
| **Stop event** | 1 block, then allow | N blocks, then auto-allow |
| **Other events** | Block indefinitely | N blocks, then auto-allow |

**Stop events are special.** When a Stop event blocks, Claude Code re-fires it with
`stop_hook_active=true`. When `limit > 0`, the CLI defers to the normal block-counter logic — Stop
events follow the same N-block limit as any other event. When `limit = 0` (unlimited), the CLI
auto-allows on the re-fired Stop to prevent an infinite loop, giving Stop a default "1 block, then
allow" behavior.

For non-Stop events (PreToolUse, PostToolUse, TaskCompleted), there is no built-in recursion guard.
Set `limit` to cap how many times a command can consecutively block before auto-allowing. The counter
resets whenever the command passes or is re-run.

**Counter semantics:** Only actionable failures (command failed, task blocked) and
timeouts increment the block counter. Transient states — "missing" (no result yet)
and "pending" (command still running) — block without counting. This prevents the
counter from exhausting the limit while the agent is still working to produce a result.

**Pending timeout:** If a command has been pending longer than `timeout` seconds (default 300 for exec,
600 for task), the check treats it as a timeout failure — removes the stale sentinel, increments the
block counter, and blocks with an explicit timeout message.

**Self-consuming checks:** Standalone `exec check` and `task check` consume their sentinel immediately
on pass. When using `sync check`, individual sentinels are consumed as each spec passes, and a group
sentinel coordinates the sequence.

**Defaults:**

- Exec: `limit: 0` (unlimited — block until resolved), `timeout: 300`
- Task: `limit: 3` (auto-allow after 3 consecutive blocks), `timeout: 600`

## Known Limitations

- **Stop event recursion:** With `limit = 0`, a re-fired Stop auto-allows after the first block to
  prevent infinite loops (set `--limit` to change this behavior).
- **Standalone check ping-pong:** Multiple standalone `exec check` / `task check` commands on the
  same hook event will race — one self-consumes and allows while the other sees "missing" and
  re-blocks, repeating indefinitely. Use `sync check` to group them into a single ordered sequence.

## Cursor Compatibility

Cursor reads `.claude/settings.json` and auto-maps Claude Code conventions to its own. The CLI
normalizes these differences back to canonical form via `compat.ts` — no per-IDE config needed.

| Difference | Cursor behavior | Normalization |
| --- | --- | --- |
| Event names | camelCase: `preToolUse`, `stop`, `sessionStart` | Case-insensitive matching via `normalizeEventName()` |
| Event rename | `UserPromptSubmit` → `beforeSubmitPrompt` | Alias map in `EVENT_NAME_CANONICAL` |
| Tool names | `Bash` → `Shell` | `isShellTool()` accepts both; `--matcher` includes `Shell` |
| State keys | Events saved under Cursor name (e.g., `beforeSubmitPrompt`) | `stateKey()` normalizes to canonical name (`UserPromptSubmit`) |
| Input fields | Same stdin JSON format | No additional normalization needed |

## VS Code Compatibility

Several features exist specifically to work around known VS Code Copilot behaviors:

| Feature | VS Code Issue | Workaround |
| --- | --- | --- |
| `--matcher` flag | VS Code ignores the hook `matcher` field and sends all tool events through all hooks ([docs](https://code.visualstudio.com/docs/copilot/customization/hooks#_how-does-vs-code-handle-claude-code-hook-configurations)) | CLI-side regex filtering after scope activation (scope runs first so file-editing tools keep the scope alive, then `--matcher` restricts which tools trigger checks) |
| `--project` flag | `process.cwd()` is set per-repo but is the same for every hook invocation — it cannot distinguish which repo the agent is editing (bugs #8559, #12808 are now fixed but CWD is still ambiguous) | Explicit per-repo project resolution via `CHUNK_HOOK_PROJECT_ROOT` |
| Scope gate | All hooks fire for all repos in multi-root workspaces | `activateScope()` inspects `tool_input` file paths to determine the target repo |
| Subagent safety | Subagents receive different session IDs; their tool calls trigger the parent's hooks | Existing scope markers are preserved (not overwritten) when a different session activates the same project |
| Tool names | VS Code uses `run_in_terminal` instead of `Bash` | `isShellTool()` accepts both; `--matcher` includes `run_in_terminal` |

## Flag Pass-through (exec)

All exec flags can be set on `check`. When `check` finds no saved result, it tells the agent to run
`exec run <name> --no-check` and **passes through** the run-affecting flags (`--cmd`, `--timeout`,
`--file-ext`, `--staged`, `--always`) so the delegated run uses the same overrides. Check-only flags
(`--on`, `--trigger`, `--limit`) are not passed through.

For example, `chunk hook exec check tests --timeout 60 --on pre-commit` will match only pre-commit
events, and when it tells the agent to run the tests, the generated command includes `--timeout 60`.

## Exec-only Flags

The `--file-ext` flag is available only for `exec`, not `task`:

- **`--file-ext`** — Execs run shell commands against specific file types. Task operates on the full
  diff via instructions; filtering by extension doesn't apply.

Both exec and task support `timeout` (configurable in YAML). For exec, it caps how long the child
process runs. For task, it sets how long a "pending" result is tolerated before the check treats it
as a timeout failure.

## Skip-if-no-changes (default behavior)

By default, commands **skip execution when no matching files have changed**:

- Exec: skips if no files matching `fileExt` have changed (or no changes at all if `fileExt` is empty).
- Task: skips if there are no uncommitted changes.

**Deleted files are excluded** from `{{CHANGED_FILES}}` and `{{CHANGED_PACKAGES}}`
because the paths no longer exist on disk. Passing deleted paths to commands like
`go test {{CHANGED_FILES}}` would cause "file not found" errors. When `--staged`
is set, the staged diff uses `--diff-filter=ACMR` (added, copied, modified, renamed).
The non-staged path filters out `D` status codes from `git status --porcelain`.

Use `--always` or set `always: true` in YAML to force execution regardless of changes.

## Configuration

### Per-repo YAML: `.chunk/hook/config.yml`

```yaml
triggers:
  pre-commit:
    - "git commit"
    - "git push"

execs:
  tests:
    command: "go test ./..."
    fileExt: ".go"
    always: false    # default — skip if no .go files changed
    timeout: 300
  lint:
    command: "golangci-lint run"
    always: false    # default — skip if no changes
    timeout: 60

tasks:
  review:
    instructions: ".chunk/hook/code-review-instructions.md"
    schema: ".chunk/hook/code-review-schema.json"
    limit: 3          # max consecutive blocks before auto-allowing
    timeout: 600      # seconds before a pending task is considered timed out (default: 600)
    # Placeholders ({{EventName.field}}) expand automatically in instructions
```

### Environment Variables

| Variable | Description | Default |
| --- | --- | --- |
| `CHUNK_HOOK_ENABLE` | Enable all commands (`1`/`true`/`yes`) — applies to Exec/Task; State ignores this | `false` |
| `CHUNK_HOOK_ENABLE_{NAME}` | Enable a specific command by name | inherits from `CHUNK_HOOK_ENABLE` |
| `CHUNK_HOOK_TIMEOUT_{NAME}` | Override timeout (seconds) | exec default |
| `CHUNK_HOOK_SENTINELS_DIR` | Custom result-file directory | `$TMPDIR/chunk-hook/sentinels/` |
| `CHUNK_HOOK_CONFIG` | Custom config file path | `.chunk/hook/config.yml` |
| `CHUNK_HOOK_VERBOSE` | Verbose logging to stderr | off |
| `CHUNK_HOOK_PROJECT_ROOT` | Parent directory for `--project` name resolution | unset |
| `CHUNK_HOOK_LOG_DIR` | Custom log directory | `$TMPDIR/chunk-hook/logs/` |
| `CLAUDE_PROJECT_DIR` | Project directory | `$PWD` |

Env vars always take precedence over YAML config values.

## Integration

Add to your `.claude/settings.json` (or `.claude/settings.local.json`):

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Bash",
        "hooks": [{
          "type": "command",
            "command": "chunk hook exec check tests --on pre-commit",
          "timeout": 10
        }]
      }
    ],
    "PostToolUse": [
      {
        "matcher": "Edit|Write",
        "hooks": [{
          "type": "command",
            "command": "chunk hook exec run lint",
          "timeout": 60
        }]
      }
    ],
    "Stop": [
      {
        "hooks": [{
          "type": "command",
            "command": "chunk hook exec check tests",
          "timeout": 10
        }]
      }
    ]
  }
}
```

## Development

```bash
bun install                     # Install dependencies (from repo root)
bun test packages/hook/         # Run hook tests
bun run typecheck               # Type check
bunx biome check packages/hook/ # Lint
```

## Architecture

```text
packages/hook/
├── src/
│   ├── index.ts            # Entry point: Commander-based command registration
│   ├── commands/
│   │   ├── env-update.ts   # Env update command (shell environment configuration)
│   │   ├── exec.ts         # Exec command (check/run)
│   │   ├── repo-init.ts    # Repo init command (template file installation)
│   │   ├── scope.ts        # Scope command (activate/deactivate — per-repo activity gate)
│   │   ├── state.ts        # State command (save/load/clear)
│   │   ├── sync.ts         # Sync command (grouped sequential checks)
│   │   └── task.ts         # Delegated task (check)
│   ├── lib/
│   │   ├── adapter.ts      # HookAdapter strategy pattern (provider abstraction)
│   │   ├── compat.ts       # IDE compatibility (event/tool name normalization)
│   │   ├── env.ts          # CHUNK_HOOK_* env var handling
│   │   ├── config.ts       # YAML config loader (triggers + execs + tasks)
│   │   ├── hooks.ts        # Event I/O, decisions, trigger matching
│   │   ├── placeholders.ts # {{Key.path}} placeholder expansion for tasks
│   │   ├── sentinel.ts     # Result-file CRUD, coordinated consumption, block counter
│   │   ├── shell-env.ts    # Shell environment utilities (env file, startup files, profiles)
│   │   ├── state.ts        # Per-project state (event-namespaced persistence)
│   │   ├── templates.ts    # Embedded template files for repo init
│   │   ├── check.ts        # Shared check helpers (evaluate, block, guard)
│   │   ├── task-result.ts  # Task result validation and conversion
│   │   ├── proc.ts         # Bun.spawn wrapper with timeout
│   │   ├── git.ts          # Changed files, placeholders
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
│   ├── .chunk/hook/config.yml
│   └── .claude/
│       ├── settings.review-example.json
│       └── settings.test-lint-example.json
├── AGENTS.md
├── README.md
├── package.json
└── tsconfig.json
```
