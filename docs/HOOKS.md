# chunk validate — Hook Mode

Lifecycle hooks for AI coding agents. Enforces quality checks before commits,
on stop, and at other agent events. Communicates via stdin JSON and exit codes.

Hook functionality is accessed through `chunk validate` flags (`--check`,
`--no-check`, `--task`, `--sync`). Session plumbing (`scope`, `state`) remains
under the hidden `chunk hook` command, invoked by IDE-generated settings.

## Supported Environments

| IDE | Status | Notes |
|---|---|---|
| **Claude Code** (CLI / terminal) | Fully supported | Canonical provider |
| **Cursor** | Supported | Reads `.claude/settings.json` directly |
| **VS Code** (GitHub Copilot with Claude) | Supported with workarounds | See VS Code notes below |

All environments share the same hook protocol (stdin JSON + exit codes) and
the same `.claude/settings.json` configuration.

## Quick Start

```bash
# 1. Install chunk (see README)
chunk --version

# 2. Initialize project (sets up config, hooks, and env)
chunk init

# 3. Restart terminal (or source the env file)
source ~/.config/chunk/hook/env

# 4. Edit .chunk/config.json to set your test/lint commands
```

## Exit Code Protocol

- **Exit 0 (allow)**: Check passed or command not enabled. Agent continues.
- **Exit 1 (error)**: Command failed or infrastructure error.
- **Exit 2 (block)**: Explicit block signal (reserved for delegation pattern).

Stderr output on exit 1/2 is fed back to the agent as an error prompt.

## Enable/Disable Control

Hooks are disabled by default. Enable via environment variables:

```bash
CHUNK_HOOK_ENABLE=1              # Enable all hooks
CHUNK_HOOK_ENABLE_TESTS=1        # Enable only "tests" command
CHUNK_HOOK_ENABLE_LINT=1         # Enable only "lint" command
```

Resolution order: `CHUNK_HOOK_ENABLE_{NAME}` > `CHUNK_HOOK_ENABLE` > disabled.

Values `1`, `true`, `yes` (case-insensitive) are truthy.

## Configuration

### Per-repo config: `.chunk/config.json`

All hook configuration lives in `.chunk/config.json` alongside other project
settings (VCS, CircleCI, validate commands):

```json
{
  "commands": [
    {"name": "tests", "run": "npm test", "fileExt": ".js", "timeout": 300, "limit": 3},
    {"name": "tests-changed", "run": "npm test -- --changed", "fileExt": ".js", "timeout": 300, "limit": 3},
    {"name": "lint", "run": "npm run lint", "timeout": 60, "limit": 3}
  ],
  "triggers": {
    "pre-commit": ["git commit", "git push"]
  },
  "tasks": {
    "review": {
      "instructions": ".chunk/hook/review-instructions.md",
      "schema": ".chunk/hook/review-schema.json",
      "limit": 3,
      "timeout": 600
    }
  }
}
```

### Environment profiles

Set up with `chunk hook env update --profile <name>`:

| Profile | Effect |
|---|---|
| `enable` (default) | `CHUNK_HOOK_ENABLE=1` |
| `disable` | `CHUNK_HOOK_ENABLE=0` |
| `tests-lint` | Global off, per-command enable for tests + tests-changed + lint |

The env file is written to `$XDG_CONFIG_HOME/chunk/hook/env`
(default: `~/.config/chunk/hook/env`). Source it from your shell startup.

## Commands

### `chunk validate <name> --no-check`

Run a configured command and save the result as a sentinel file (exit 0 regardless).

| Flag | Default | Description |
|---|---|---|
| `--cmd` | — | Command override (takes precedence over config) |
| `--staged` | `false` | Only staged files |
| `--always` | `false` | Run even without matching changes |
| `--on` | — | Trigger group name |
| `--trigger` | — | Inline trigger pattern |
| `--limit` | `0` | Max consecutive blocks |
| `--matcher` | — | Tool-name regex filter |
| `--project` | auto | Project directory |

**Flow:**
1. Check if enabled via `CHUNK_HOOK_ENABLE_{NAME}`
2. Resolve command from `--cmd` flag or config
3. Write "pending" sentinel
4. Execute via `sh -c` in project directory
5. Write final sentinel (pass/fail, exit code, output)
6. Exit 0 (result saved but not enforced)

### `chunk validate <name> --check`

Read a saved sentinel and enforce the result.

Same flags as `--no-check` mode.

**Delegation pattern:** For long-running commands that may exceed the hook
timeout, use `--check` as the hook entry point. On first call (no sentinel),
it blocks with a directive telling the agent to run
`chunk validate <name> --no-check`. The agent runs the command in its
own terminal (no timeout). On the next hook invocation, `--check` reads the
sentinel and enforces the result.

### `chunk validate <name> --task`

Check a task result written by a subagent.

| Flag | Default | Description |
|---|---|---|
| `--instructions` | — | Task instructions file path |
| `--schema` | — | Result JSON schema file path |
| `--always` | `false` | Run even without matching changes |
| `--staged` | `false` | Only staged files |
| `--on` | — | Trigger group name |
| `--trigger` | — | Inline trigger pattern |
| `--matcher` | — | Tool-name regex filter |
| `--limit` | `0` | Max consecutive blocks |
| `--project` | auto | Project directory |

**Task delegation flow:**
1. `chunk validate <name> --task` is called by the hook
2. On first call (no result file), blocks with a directive to spawn a subagent
3. The subagent writes `{ "decision": "allow"|"block", "reason": "..." }` to
   the sentinel path
4. On next invocation, reads and validates the result

### `chunk validate --sync <specs...>`

Run multiple exec/task checks as a single ordered group.

Specs use `type:name` format, e.g. `--sync exec:tests,task:review`.

| Flag | Default | Description |
|---|---|---|
| `--on` | — | Trigger group name |
| `--trigger` | — | Inline trigger pattern |
| `--matcher` | — | Tool-name regex filter |
| `--limit` | `0` | Max consecutive blocks |
| `--staged` | `false` | Only staged files |
| `--always` | `false` | Run even without matching changes |
| `--on-fail` | `restart` | Failure strategy: `restart`, `continue`, `bail` |
| `--bail` | `false` | Stop at first failure |
| `--project` | auto | Project directory |

Use `--sync` when two or more delegated checks share the same hook event
(e.g. `exec:tests` + `task:review` on `Stop`). It ensures correct ordering.

### Hidden session plumbing

These commands remain under `chunk hook` (hidden from `--help`) and are invoked
by IDE-generated `.claude/settings.json`:

- **`chunk hook scope activate/deactivate`** — marks active repo in IDE session
- **`chunk hook state save/append/load/clear`** — cross-event state persistence
- **`chunk hook setup`** / **`chunk hook repo init`** / **`chunk hook env update`** — setup (use `chunk init` instead)

### `chunk hook scope activate` / `deactivate`

Mark which repository is currently active in an IDE session.

| Flag | Default | Description |
|---|---|---|
| `--project` | auto | Project directory |

- **activate**: Reads `session_id` from stdin JSON, writes
  `.chunk/hook/.chunk-hook-active` marker
- **deactivate**: Removes the marker (requires `session_id` in stdin)

### `chunk hook state save` / `append` / `load` / `clear`

Cross-event state persistence. State is stored per-project as JSON in the
sentinel directory.

| Flag | Default | Description |
|---|---|---|
| `--project` | auto | Project directory |

- **save**: Replace state for the given `hook_event_name` (from stdin JSON)
- **append**: Append to `__entries` array for that event
- **load**: Output entire state as pretty-printed JSON
- **clear**: Delete the state file

**Session awareness:** If `session_id` in stdin differs from stored session,
state is cleared automatically (new session detected).

## Sentinel File System

Sentinels are JSON files that track command execution status.

**Location**: `{SENTINELS_DIR}/{safe_name}-{hash}.json`

Resolution order for sentinel directory:
1. `CHUNK_HOOK_SENTINELS_DIR` env var
2. `{TMPDIR}/chunk-hook/sentinels`

**Sentinel ID**: `{safe_name}-{sha256(projectDir:name)[:8]}`

**Sentinel format:**
```json
{
  "status": "pending|pass|fail",
  "startedAt": "2024-03-24T10:00:00Z",
  "finishedAt": "2024-03-24T10:00:05Z",
  "exitCode": 0,
  "command": "npm test",
  "output": "...",
  "project": "/path/to/project"
}
```

**State file**: `{SENTINELS_DIR}/state-{sha256(projectDir)[:8]}.json`

## IDE Integration

`chunk init` generates `.claude/settings.json` with hook definitions for the
following lifecycle events:

| Event | Hooks |
|---|---|
| `SessionStart` | `chunk hook scope deactivate` |
| `UserPromptSubmit` | `chunk hook state append` |
| `PreToolUse` | `chunk validate tests-changed --check`, `chunk validate lint --no-check` |
| `Stop` | `chunk validate tests --check`, `chunk validate lint` |
| `SessionEnd` | `chunk hook scope deactivate`, `chunk hook state clear` |

The generated settings grant `Bash(chunk:*)` permission so hook commands
run without prompting.

## Project Directory Resolution

The `--project` flag resolves in this order:
1. Explicit `--project` value (absolute or relative to `CHUNK_HOOK_PROJECT_ROOT`)
2. `CLAUDE_PROJECT_DIR` environment variable (set by the IDE)
3. Current working directory

## Environment Variables

| Variable | Purpose |
|---|---|
| `CHUNK_HOOK_ENABLE` | Global enable/disable (0/1) |
| `CHUNK_HOOK_ENABLE_{NAME}` | Per-command override |
| `CHUNK_HOOK_SENTINELS_DIR` | Custom sentinel directory |
| `CHUNK_HOOK_PROJECT_ROOT` | Multi-repo workspace root |
| `CHUNK_HOOK_LOG_DIR` | Log directory |
| `CHUNK_HOOK_VERBOSE` | Verbose logging |
| `CLAUDE_PROJECT_DIR` | IDE-set project directory |
