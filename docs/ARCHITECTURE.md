# Architecture

Module layout and dependency rules for the `chunk` Go CLI.

## Directory Structure

```
chunk-cli/
├── main.go                    # Entry point: cobra bootstrap + usererr handling
├── skills/                    # Skill definitions (go:embed) and skill subdirectories
├── acceptance/                # Acceptance tests
└── internal/
    ├── cmd/                   # Cobra command definitions (thin wrappers)
    │   ├── root.go            # Root command, registers all subcommands
    │   ├── auth.go            # auth set, auth status, auth remove
    │   ├── buildprompt.go     # build-prompt
    │   ├── completion.go      # completion install/uninstall/zsh
    │   ├── config.go          # config show/set
    │   ├── init.go            # init (project setup, settings.json generation)
    │   ├── sandbox.go         # sandbox list/create/exec/add-ssh-key/ssh/sync/env/build
    │   ├── skills.go          # skill install/list
    │   ├── task.go            # task run
    │   ├── upgrade.go         # upgrade
    │   └── validate.go        # validate
    ├── anthropic/             # Anthropic Messages API client
    ├── buildprompt/           # Three-step pipeline: discover → analyze → generate
    ├── circleci/              # CircleCI REST API client
    ├── config/                # User config (~/.chunk/config.json)
    ├── github/                # GitHub GraphQL client (reviews, repos)
    ├── gitremote/             # Git remote URL parsing for org/repo detection
    ├── gitutil/               # Git utility helpers
    ├── httpcl/                # HTTP client library (JSON + retries)
    ├── iostream/              # I/O stream abstraction
    ├── sandbox/               # CircleCI sandbox operations
    ├── skills/                # Skill definitions (go:embed) and installation
    ├── task/                  # Task run config and CircleCI trigger
    ├── testing/recorder/      # HTTP recorder for tests
    ├── tui/                   # Terminal UI components (confirm, input, select)
    ├── ui/                    # Colors, formatting, spinner
    ├── upgrade/               # CLI self-upgrade
    ├── usererr/               # User-facing error wrapper
    └── validate/              # Validation command logic
```

## Layering Rules

Dependencies flow strictly downward:

```
main.go → internal/cmd/ → internal/{business packages} → internal/httpcl/
```

- `main.go` creates the root command and handles top-level errors
- `internal/cmd/` contains thin cobra wrappers that parse flags and delegate
- Business packages (`buildprompt/`, `task/`, etc.) contain the logic
- `internal/httpcl/` is an independent library — no imports are allowed to other `internal/` packages
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

- Analysis step: `claude-sonnet-4-6`
- Generation step: `claude-opus-4-6`
- Overridable via `--analyze-model` / `--prompt-model` flags

## Configuration Resolution

User config lives at `~/.chunk/config.json`:

```json
{
  "apiKey": "sk-...",
  "model": "claude-sonnet-4-6"
}
```

Resolution priority:
- API key: flag > config file > `ANTHROPIC_API_KEY` env var
- Model: flag > config file > built-in default

## Pre-Commit Hooks

`chunk init` generates `.claude/settings.json` with a `PreToolUse` hook that
runs configured validation commands before the AI agent commits code. See
**[docs/HOOKS.md](HOOKS.md)** for details.

`chunk validate` runs those same commands manually, with SHA256-based content
caching so unchanged files skip re-execution.

## HTTP Client (`internal/httpcl/`)

Shared HTTP infrastructure used by `anthropic/`, `circleci/`, and `github/`:

- JSON request/response encoding by default
- Automatic retry via `hashicorp/go-retryablehttp` (up to 3 retries)
- Configurable auth (Bearer token or custom header like `x-api-key`)
- Fluent request builder: `httpcl.NewRequest(method, path, opts...)`

## Error Handling

- Business logic returns `usererr.Error` for user-facing messages
- `fmt.Errorf("context: %w", err)` for error wrapping
- `main()` catches errors and prints the appropriate message

## Environment Variables

| Variable | Used by | Purpose |
|---|---|---|
| `ANTHROPIC_API_KEY` | anthropic, config, validate | Anthropic authentication |
| `ANTHROPIC_BASE_URL` | anthropic, validate | API endpoint override |
| `GITHUB_TOKEN` | github | GitHub authentication |
| `GITHUB_API_URL` | github | GitHub API endpoint override |
| `CIRCLE_TOKEN` / `CIRCLECI_TOKEN` | circleci | CircleCI authentication |
| `CIRCLECI_BASE_URL` | circleci | CircleCI endpoint override |
| `CLAUDE_PROJECT_DIR` | init | IDE-provided project directory |
