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
├── run                             # Was: task — pipeline operations
│   ├── setup                       # Was: task config — setup wizard for .chunk/run.json
│   └── (default action)            # Was: task run — trigger a pipeline run
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

## Default Output Path

The `--output` flag for `build-prompt` defaults to `.chunk/context/review-prompt.md`, placing the generated prompt where AI coding agents auto-discover it.

## Naming Rules

- Top-level commands are verbs or nouns: `build-prompt`, `run`, `auth`, `config`, `skills`, `hook`, `upgrade`
- `run` is the parent for pipeline operations; when called with `--definition` and `--prompt` flags it triggers a run directly (no `task` subcommand needed)
- `setup` is the subcommand under `run` that configures `.chunk/run.json`
- Examples: `chunk run --definition dev --prompt "Fix the test"`, `chunk run setup`

## Deprecation

The old `task` top-level command is preserved as a hidden alias that prints a deprecation notice directing users to `chunk run`.
