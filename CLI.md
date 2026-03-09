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
│       --definition <name>         # Definition name or UUID (required)
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
├── hook                            # Agent-facing, unchanged
│   └── (exec, task, sync, state, scope, env-update, repo-init)
│
└── upgrade                         # Update to the latest version
```

## Flag Conventions

- **Required flags**: Use `.requiredOption()` in Commander
- **Comma-separated lists**: Parse with `value.split(",")`
- **Date formats**: `YYYY-MM-DD`, parsed with `new Date(value)`
- **Boolean toggles**: Default to `false` unless noted (e.g., `--pipeline-as-tool` defaults to `true`)
- **Model flags**: Reference defaults from `config/index.ts`

## Behavior Decisions

- `build-prompt` should support org auto-detection from the git remote when `--org` is omitted
- If `--org` is provided, `--repos` remains optional; omitting it means "all repos in the org"
- The CLI help text, README, and implementation must all describe the same `build-prompt` behavior
- `task` should remain an explicit command group because it matches the product terminology used in the UI

## Default Output Path

The `--output` flag for `build-prompt` defaults to `.chunk/context/review-prompt.md`, placing the generated prompt where AI coding agents auto-discover it.

## Naming Rules

- Top-level commands are verbs or nouns: `build-prompt`, `task`, `auth`, `config`, `skills`, `hook`, `upgrade`
- `task` is the parent for pipeline task operations
- `config` is the setup subcommand under `task`
- `run` is the execution subcommand under `task`
- Examples: `chunk task run --definition dev --prompt "Fix the test"`, `chunk task config`

## Deprecation

No command rename is planned for `task`; improve the existing command tree rather than adding a migration layer.
