# chunk hook

Lifecycle hooks for AI coding agents. Enforces quality checks before commits,
on stop, and at other agent events. Communicates via stdin JSON and exit codes.

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

# 2. Configure environment and initialize repo hooks
chunk hook setup

# 3. Restart terminal (or source the env file)
source ~/.config/chunk/hook/env

# 4. Edit .chunk/hook/config.yml to set your test/lint commands
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

### Per-repo config: `.chunk/hook/config.yml`

```yaml
# Named trigger groups — patterns that activate hooks
triggers:
  pre-commit:
    - "git commit"
    - "git push"

# Exec definitions — shell commands to run
execs:
  tests:
    command: "npm test"
    fileExt: ".js"
    always: false
    timeout: 300           # seconds (default: 300)
    limit: 3               # max consecutive blocks

  tests-changed:
    command: "npm test -- --changed"
    fileExt: ".js"
    timeout: 300
    limit: 3

  lint:
    command: "npm run lint"
    always: false
    timeout: 60
    limit: 3

# Task definitions — delegate complex work to subagents
tasks:
  review:
    instructions: ".chunk/hook/review-instructions.md"
    schema: ".chunk/hook/review-schema.json"
    limit: 3               # max consecutive blocks (default: 3)
    timeout: 600           # seconds (default: 600)

# Override sentinel storage location
sentinels:
  dir: "/custom/path/to/sentinels"
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

### `chunk hook setup [dir]`

One-shot setup: runs `env update` then `repo init`.

| Flag | Default | Description |
|---|---|---|
| `--profile` | `enable` | Environment profile |
| `--skip-env` | `false` | Skip env file creation |
| `--force` | `false` | Overwrite existing files |
| `--env-file` | auto | Custom env file path |

### `chunk hook repo init [dir]`

Creates template files in the target directory:

- `.chunk/hook/.gitignore` — ignores runtime marker files
- `.chunk/hook/config.yml` — per-repo hook configuration
- `.claude/settings.json` — IDE hook definitions

If files already exist and `--force` is not set, writes `.example` variants
instead.

| Flag | Default | Description |
|---|---|---|
| `--force` | `false` | Overwrite existing files |

### `chunk hook env update`

Writes the environment file with profile-based configuration.

| Flag | Default | Description |
|---|---|---|
| `--profile` | `enable` | Profile: `disable`, `enable`, `tests-lint` |
| `--env-file` | auto | Custom env file path |
| `--set-log-dir` | auto | Override log directory |
| `--set-verbose` | `false` | Enable verbose logging |
| `--set-project-root` | — | Project root for multi-repo workspaces |

Default log directories:
- macOS: `~/Library/Logs/chunk-hook`
- Linux: `~/.local/share/chunk-hook/logs`

### `chunk hook exec run <name>`

Execute a configured shell command and save the result as a sentinel file.

| Flag | Default | Description |
|---|---|---|
| `--cmd` | — | Command override (takes precedence over config) |
| `--timeout` | `300` | Timeout in seconds |
| `--file-ext` | — | File extension filter |
| `--staged` | `false` | Only staged files |
| `--always` | `false` | Run even without matching changes |
| `--no-check` | `false` | Save result but always exit 0 |
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
6. Exit 0 if `--no-check`; exit 1 if command failed

### `chunk hook exec check <name>`

Read a saved sentinel and enforce the result.

Same flags as `exec run` except `--cmd` and `--no-check`.

**Delegation pattern:** For long-running commands that may exceed the hook
timeout, use `check` as the hook entry point. On first call (no sentinel),
`check` blocks with a directive telling the agent to run
`chunk hook exec run <name> --no-check`. The agent runs the command in its
own terminal (no timeout). On the next hook invocation, `check` reads the
sentinel and enforces the result.

### `chunk hook task check <name>`

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
1. `task check <name>` is called by the hook
2. On first call (no result file), blocks with a directive to spawn a subagent
3. The subagent writes `{ "decision": "allow"|"block", "reason": "..." }` to
   the sentinel path
4. On next invocation, `task check` reads and validates the result

### `chunk hook sync check <specs...>`

Run multiple exec/task checks as a single ordered group.

Specs use `type:name` format, e.g. `exec:tests task:review`.

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

Use `sync check` when two or more delegated checks share the same hook event
(e.g. `exec:tests` + `task:review` on `Stop`). It ensures correct ordering.

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
2. `sentinels.dir` in config.yml
3. `{TMPDIR}/chunk-hook/sentinels`

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

`chunk hook repo init` generates `.claude/settings.json` with hook
definitions for the following lifecycle events:

| Event | Hooks |
|---|---|
| `SessionStart` | `scope deactivate` (clean up previous session) |
| `UserPromptSubmit` | `state append` (capture prompt input) |
| `PreToolUse` | `exec check tests-changed`, `exec run lint` (with matcher) |
| `Stop` | `exec check tests`, `exec run lint` |
| `SessionEnd` | `scope deactivate`, `state clear` |

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
| `CHUNK_HOOK_CONFIG` | Custom config file path |
| `CHUNK_HOOK_SENTINELS_DIR` | Custom sentinel directory |
| `CHUNK_HOOK_PROJECT_ROOT` | Multi-repo workspace root |
| `CHUNK_HOOK_LOG_DIR` | Log directory |
| `CHUNK_HOOK_VERBOSE` | Verbose logging |
| `CLAUDE_PROJECT_DIR` | IDE-set project directory |
