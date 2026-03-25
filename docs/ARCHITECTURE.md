# Architecture

Module layout and dependency rules for the `chunk` Go CLI.

## Directory Structure

```
chunk-cli/
├── main.go                    # Entry point: cobra bootstrap + usererr handling
├── httpcl/                    # HTTP client library (JSON + retries)
│   ├── client.go              # Retryable HTTP client (go-retryablehttp)
│   ├── request.go             # Fluent request builder
│   └── error.go               # HTTP error types
└── internal/
    ├── cmd/                   # Cobra command definitions (thin wrappers)
    │   ├── root.go            # Root command, registers all subcommands
    │   ├── auth.go            # auth status, auth logout
    │   ├── buildprompt.go     # build-prompt
    │   ├── completion.go      # completion install/uninstall/zsh
    │   ├── config.go          # config show/set
    │   ├── hook.go            # hook repo/setup/env/scope/state/exec/task/sync
    │   ├── sandboxes.go       # sandboxes list/create/exec/add-ssh-key/ssh/sync/prepare
    │   ├── skills.go          # skills install/list
    │   ├── task.go            # task run
    │   ├── upgrade.go         # upgrade
    │   └── validate.go        # validate, validate init
    ├── anthropic/             # Anthropic Messages API client
    ├── buildprompt/           # Three-step pipeline: discover → analyze → generate
    ├── circleci/              # CircleCI REST API client
    ├── config/                # User config (~/.chunk/config.json)
    ├── github/                # GitHub GraphQL client (reviews, repos)
    ├── gitremote/             # Git remote URL parsing for org/repo detection
    ├── hook/                  # Hook system: exec, task, sync, state, scope, env
    ├── sandbox/               # CircleCI sandbox operations
    ├── skills/                # Skill definitions (go:embed) and installation
    ├── task/                  # Task run config and CircleCI trigger
    ├── upgrade/               # CLI self-upgrade
    ├── usererr/               # User-facing error wrapper
    └── validate/              # Validation command logic
```

## Layering Rules

Dependencies flow strictly downward:

```
main.go → internal/cmd/ → internal/{business packages} → httpcl/
```

- `main.go` creates the root command and handles top-level errors
- `internal/cmd/` contains thin cobra wrappers that parse flags and delegate
- Business packages (`buildprompt/`, `hook/`, `task/`, etc.) contain the logic
- `httpcl/` is an independent library — no imports from `internal/`
- `config/` is a leaf — no imports from other `internal/` packages

No upward or lateral imports between business packages, except where a
package naturally composes another (e.g. `task/` uses `circleci/`).

## Entry Point

```go
main() → cmd.NewRootCmd(version) → rootCmd.Execute()
```

Errors are caught in `main()`. If the error is a `usererr.Error`, only the
user-facing message is printed (no stack trace). Otherwise the raw error
is printed. Both exit with code 1.

## Data Flow: `build-prompt`

Three-step pipeline orchestrated by `buildprompt.Run()`:

```
1. Discover          github/ → FetchReviewActivity() per repo
                     → AggregateActivity() → TopN reviewers
                     → FilterDetailsByReviewers()
                     → writes details.json, details-pr-rankings.csv

2. Analyze           GroupByReviewer(comments)
                     → anthropic/ → AnalyzeReviews() → Claude
                     → writes analysis.md

3. Generate          Read analysis.md
                     → anthropic/ → GenerateReviewPrompt() → Claude
                     → writes review-prompt.md
```

### Org and repo resolution

- If `--org` is provided, `--repos` is required
- If neither is provided, both are auto-detected from the git remote

### Model defaults

- Analysis step: `claude-sonnet-4-5-20250929`
- Generation step: `claude-opus-4-5-20251101`
- Overridable via `--analyze-model` / `--prompt-model` flags

## Configuration Resolution

User config lives at `~/.chunk/config.json`:

```json
{
  "apiKey": "sk-...",
  "model": "claude-sonnet-4-5-20250929"
}
```

Resolution priority:
- API key: flag > config file > `ANTHROPIC_API_KEY` env var
- Model: flag > config file > built-in default

## Hook System

See **[docs/HOOKS.md](HOOKS.md)** for the full hook system documentation.

The hook subsystem lives entirely in `internal/hook/` and is exposed via
`internal/cmd/hook.go`. It provides lifecycle automation for AI coding agents
through exec commands, task delegation, grouped checks, state persistence,
and multi-repo scope management.

## HTTP Client (`httpcl/`)

Shared HTTP infrastructure used by `anthropic/`, `circleci/`, and `github/`:

- JSON request/response encoding by default
- Automatic retry via `hashicorp/go-retryablehttp` (up to 3 retries)
- Configurable auth (Bearer token or custom header like `x-api-key`)
- Fluent request builder: `httpcl.NewRequest(method, path, opts...)`

## Error Handling

- Business logic returns `usererr.Error` for user-facing messages
- `fmt.Errorf("context: %w", err)` for error wrapping
- `main()` catches errors and prints the appropriate message
- Hook commands use exit codes as a protocol (0 = allow, 1 = error)

## Environment Variables

| Variable | Used by | Purpose |
|---|---|---|
| `ANTHROPIC_API_KEY` | anthropic, config, validate | Anthropic authentication |
| `ANTHROPIC_BASE_URL` | anthropic, validate | API endpoint override |
| `GITHUB_TOKEN` | github | GitHub authentication |
| `GITHUB_API_URL` | github | GitHub API endpoint override |
| `CIRCLE_TOKEN` / `CIRCLECI_TOKEN` | circleci | CircleCI authentication |
| `CIRCLECI_BASE_URL` | circleci | CircleCI endpoint override |
| `CHUNK_HOOK_ENABLE` | hook | Global hook enable (0/1) |
| `CHUNK_HOOK_ENABLE_{NAME}` | hook | Per-command enable override |
| `CHUNK_HOOK_CONFIG` | hook | Custom config file path |
| `CHUNK_HOOK_SENTINELS_DIR` | hook | Custom sentinel directory |
| `CHUNK_HOOK_PROJECT_ROOT` | hook | Multi-repo workspace root |
| `CHUNK_HOOK_LOG_DIR` | hook | Log directory |
| `CHUNK_HOOK_VERBOSE` | hook | Verbose logging |
| `CLAUDE_PROJECT_DIR` | hook | IDE-provided project directory |
