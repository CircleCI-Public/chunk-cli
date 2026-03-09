# Proposed CLI Command Tree

> **Note**: This document describes the *target* CLI structure, not necessarily the current state. It serves as a reference for all restructuring work.

## Command Tree

```
chunk
├── build-prompt                    # Main pipeline (unchanged)
│   --org <org>                     # GitHub org (auto-detected from git remote if omitted)
│   --repos <items>                 # Comma-separated list of repo names
│   --top <number>                  # Number of top reviewers to analyze (default: 5)
│   --since <date>                  # Start date YYYY-MM-DD (default: 3 months ago)
│   --output <path>                 # Output path (default: .chunk/context/review-prompt.md)
│   --max-comments <number>         # Max comments per reviewer
│   --analyze-model <model>         # Claude model for analysis step
│   --prompt-model <model>          # Claude model for prompt generation
│   --include-attribution           # Include reviewer attribution
│
├── task                            # Pipeline task operations
│   ├── config                      # Setup wizard for .chunk/run.json
│   └── run                         # Trigger a pipeline run
│       --definition <name|uuid>    # Definition name or raw UUID (required)
│       --prompt <text>             # Prompt to send to the agent (required)
│       --branch <branch>           # Branch to check out (overrides definition default)
│       --new-branch                # Create a new branch for the run
│       --pipeline-as-tool          # Run the pipeline as a tool call (default: true)
│
├── auth
│   ├── login                       # Store API key
│   ├── status                      # Check authentication status
│   └── logout                      # Remove stored credentials
│
├── config
│   ├── show                        # Display current configuration
│   └── set <key> <value>           # Set a configuration value
│
├── skills
│   ├── list                        # Merged: shows name + description + install state
│   └── install                     # Install or update all skills
│
├── hook                            # Agent-facing (implemented in packages/hook/)
│   └── (exec, task, sync, state, scope, repo, env)
│
└── upgrade                         # Update to the latest version
```

## Package Boundaries

The `hook` command group is implemented entirely in `packages/hook/` (`@chunk/hook`), not in `src/commands/`. The main CLI registers it via a single `registerHookCommands()` call. See `ARCHITECTURE.md` for the package boundary rationale.

> **Naming collision**: `chunk task` (CircleCI pipeline runs, `src/commands/task.ts`) and `chunk hook task` (delegated subagent work, `packages/hook/src/commands/task.ts`) are unrelated commands.

## Flag Conventions

- **Required flags**: Use `.requiredOption()` in Commander
- **Comma-separated lists**: Parse with `value.split(",")`
- **Date formats**: `YYYY-MM-DD`, parsed with `new Date(value)`
- **Boolean toggles**: Default to `false` unless noted (e.g., `--pipeline-as-tool` defaults to `true` — this is intentional because pipeline-as-tool is the standard mode; the flag exists to allow disabling it with `--no-pipeline-as-tool`)
- **Model flags**: Reference defaults from `config/index.ts`

## Behavior Decisions

- `build-prompt` should support org auto-detection from the git remote when `--org` is omitted
- If `--org` is provided, `--repos` is required (we have no way to enumerate all repos in an org)
- The CLI help text, README, and implementation must all describe the same `build-prompt` behavior
- `task` should remain an explicit command group because it matches the product terminology used in the UI

### `build-prompt` resolution matrix

| Invocation | Target behavior |
|------------|-----------------|
| `chunk build-prompt` | Auto-detect org and current repo from the git remote |
| `chunk build-prompt --repos repo1,repo2` | Auto-detect org from the git remote and analyze the specified repos |
| `chunk build-prompt --org myorg --repos repo1,repo2` | Analyze the specified repos in the specified org |
| `chunk build-prompt --org myorg` | **Error** — `--repos` is required when `--org` is provided |

If git remote auto-detection is needed and fails, the command should exit with a clear error telling the user to pass `--org` explicitly.

### `task run` definition behavior

`chunk task run` continues to require `.chunk/run.json` for repository-level CircleCI context (`org_id`, `project_id`, and related defaults). A raw UUID is only a shortcut for the definition lookup itself:

- `--definition dev` → resolve the named definition from `.chunk/run.json`
- `--definition <uuid>` → use the UUID directly as `definition_id`, while still reading the rest of the run context from `.chunk/run.json`

No separate config-less `task run` mode is part of this restructuring.

## Default Output Path

The `--output` flag for `build-prompt` defaults to `.chunk/context/review-prompt.md`, placing the generated prompt where AI coding agents auto-discover it.

> **Migration note**: The current default is `./review-prompt.md`. Changing it to `.chunk/context/review-prompt.md` is a Phase 2 task — see `tasks.md`. This is a user-facing behavior change:
> - `build-prompt` should auto-create the `.chunk/context/` directory if it doesn't exist
> - Consider printing a one-time deprecation warning if `./review-prompt.md` exists in the working directory, so users with scripts or workflows referencing the old path are aware of the change
> - Document the change in release notes

## Naming Rules

- Top-level commands are verbs or nouns: `build-prompt`, `task`, `auth`, `config`, `skills`, `hook`, `upgrade`
- `task` is the parent for pipeline task operations
- `config` is the setup subcommand under `task`
- `run` is the execution subcommand under `task`
- Examples: `chunk task run --definition dev --prompt "Fix the test"`, `chunk task run --definition 550e8400-e29b-41d4-a716-446655440000 --prompt "Fix the test"`, `chunk task config`

## Deprecation

No command rename is planned for `task`; improve the existing command tree rather than adding a migration layer.
