# chunk

CLI for generating AI agent context from real code review patterns. Mines PR review comments from GitHub, analyzes them with Claude, and outputs a context prompt file tuned to your team's standards.

## Features

- **Pattern Mining** - Discovers top reviewers in your GitHub org and fetches their review comments
- **AI Analysis** - Uses Claude (Sonnet, Opus, or Haiku) to identify recurring patterns and team standards
- **Context Generation** - Produces a markdown prompt file ready to use with AI coding agents
- **Hook Automation** - Wires tests, lint, and AI code review into your agent's lifecycle (Claude Code, Cursor, VS Code Copilot)
- **Self-Updating** - Built-in upgrade command for binary updates

## Requirements

- **macOS** (arm64 or x86_64) or **Linux** (arm64 or x86_64)
- **Fast path**: `gh` CLI installed and authenticated (`gh auth login`)

## Installation

### Homebrew

```bash
brew install CircleCI-Public/circleci/chunk
```

### Building a local development version

To build and install from source:

```bash
task build
cp dist/chunk ~/.local/bin/
```

## Quick Start

```bash
# Authenticate with your Anthropic API key
chunk auth login

# Generate a context prompt from your org's review patterns
chunk build-prompt --org myorg

# Use the generated context with an AI coding agent
# Place the output file in .chunk/context/ or pass it via --config
```

## Commands

### build-prompt

Mines PR review comments from GitHub to generate a custom review prompt tuned to your team's patterns. Runs a three-step pipeline:

1. **Discover** — finds the top reviewers in your org by review activity
2. **Analyze** — sends their comments to Claude to identify recurring patterns
3. **Generate** — produces a markdown prompt file that agents can use for context

```bash
chunk build-prompt --org <org> [options]
```

**Required:**

| Flag | Description |
|------|-------------|
| `--org <org>` | GitHub organization to analyze |

**Options:**

| Flag | Default | Description |
|------|---------|-------------|
| `--repos <repos>` | all repos in org | Comma-separated list of repo names to include |
| `--top <n>` | `5` | Number of top reviewers to analyze |
| `--since <date>` | 3 months ago | Start date in `YYYY-MM-DD` format |
| `--output <path>` | `.chunk/context/review-prompt.md` | Output path for the generated prompt |
| `--max-comments <n>` | all | Max comments per reviewer sent for analysis |
| `--analyze-model <model>` | `claude-sonnet-4-5-20250929` | Claude model for the analysis step |
| `--prompt-model <model>` | `claude-opus-4-5-20251101` | Claude model for prompt generation |
| `--include-attribution` | off | Include reviewer names in the generated prompt |

**Environment variables required:**

| Variable | Description |
|----------|-------------|
| `GITHUB_TOKEN` | GitHub personal access token with `repo` scope |
| `ANTHROPIC_API_KEY` | Anthropic API key |

**Output files** (written alongside the final prompt):

| File | Description |
|------|-------------|
| `<output>-details.json` | Raw review comments for the top reviewers |
| `<output>-analysis.md` | Claude's analysis of reviewer patterns |
| `<output>.md` | Final generated prompt |

**Examples:**

```bash
# Analyze all repos in an org
chunk build-prompt --org myorg

# Analyze specific repos, keeping the top 10 reviewers
chunk build-prompt --org myorg --repos api,backend,frontend --top 10

# Limit data sent to Claude and save output to a specific path
chunk build-prompt --org myorg --repos myrepo --max-comments 50 --output ./prompts/review.md
```

The default output path is `.chunk/context/review-prompt.md`, so AI coding agents (e.g., Claude Code) automatically pick it up as context. No manual copy step is needed.

### task

Triggers and configures chunk pipeline runs.

#### Prerequisites

You will need three identifiers from CircleCI before running setup:

| Identifier | Where to find it |
|------------|-----------------|
| **Organization ID** | CircleCI app → Organization Settings → Overview |
| **Project ID** | CircleCI app → Project Settings → Overview |
| **Definition ID** | CircleCI app → the chunk pipeline definition page (UUID in the URL or settings) |

You will also need a CircleCI personal API token set as `CIRCLE_TOKEN`:

```bash
export CIRCLE_TOKEN=your-token-here
```

#### Setup

Run the interactive setup wizard from your repository root to create `.chunk/run.json`:

```bash
chunk task config
```

The wizard will prompt you for your org ID, project ID, and at least one named pipeline definition. You can add multiple definitions (e.g. `dev`, `prod`) pointing to different CircleCI pipeline definitions.

The resulting `.chunk/run.json` looks like:

```json
{
  "org_id": "<circleci-org-uuid>",
  "project_id": "<circleci-project-uuid>",
  "org_type": "github",
  "definitions": {
    "dev": {
      "definition_id": "<pipeline-definition-uuid>",
      "default_branch": "main"
    }
  }
}
```

#### Usage

```bash
# Trigger a run using a named definition from .chunk/run.json
chunk task run --definition dev --prompt "Fix the flaky test in auth.spec.ts"

# Override the branch
chunk task run --definition dev --prompt "Refactor the payment module" --branch my-feature-branch

# Create a new branch for the run
chunk task run --definition dev --prompt "Add type annotations" --new-branch

# Use a raw definition UUID directly (no .chunk/run.json needed)
chunk task run --definition 550e8400-e29b-41d4-a716-446655440000 --prompt "Fix the flaky test"
```

**Options:**

| Flag | Default | Description |
|------|---------|-------------|
| `--definition <name\|uuid>` | required | Named definition from `.chunk/run.json`, or a raw definition UUID |
| `--prompt <text>` | required | Prompt to send to the agent |
| `--branch <branch>` | definition default | Branch to check out |
| `--new-branch` | `false` | Create a new branch for the run |
| `--no-pipeline-as-tool` | — | Disable running the pipeline as a tool call |

### Hook Automation

`chunk hook` automates test, lint, and code-review tasks by wiring them into your AI coding agent's lifecycle events (Claude Code, Cursor, VS Code Copilot). Hooks fire at the right moments — blocking commits when tests fail, running lint before the agent stops, and triggering an AI review pass at session end.

#### 1. Configure your shell environment

Run once to write `CHUNK_HOOK_*` exports to a dedicated env file and source it from your shell startup files:

```bash
chunk hook env update --profile tests-lint
```

Available profiles:

| Profile | What it enables |
|---------|-----------------|
| `disable` | All hooks disabled |
| `enable` | All hooks enabled |
| `tests-lint` | Tests and lint only |
| `review` | AI code review only |

Restart your shell (or `source ~/.zprofile`) after running for the first time.

#### 2. Initialize your repository

Run in your project root to set up the config and wire up `.claude/settings.json`:

```bash
chunk init
```

This detects your VCS org/repo, prompts for CircleCI org, detects test commands,
and generates `.chunk/config.json` and `.claude/settings.json`.
### Other Commands

```bash
chunk auth login      # Set up API key
chunk auth status     # Check authentication
chunk config show     # Display current configuration
chunk upgrade         # Update to latest version
```

### Hook Commands

The `chunk validate` command provides configurable quality checks for
[Claude Code hooks](https://docs.anthropic.com/en/docs/claude-code) — tests, lint, code review,
and more.

```bash
# Configure shell environment (PATH, CHUNK_HOOK_* vars):
chunk hook env update

# Initialize a repo:
chunk init

# Run a named shell command (tests, lint, etc.):
CHUNK_HOOK_ENABLE=1 echo '{}' | chunk validate tests --no-check --override-cmd "go test ./..."

# Grouped checks (tests + review on the same event):
chunk validate --sync exec:tests,task:review --on pre-commit
```

## Configuration

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Anthropic API key |
| `GITHUB_TOKEN` | GitHub personal access token (required for `build-prompt`) |

## Platform Support

| Platform | Status |
|----------|--------|
| macOS (Apple Silicon) | Supported |
| macOS (Intel) | Supported |
| Linux (arm64) | Supported |
| Linux (x86_64) | Supported |
| Windows | Not supported |

## Model Pricing

| Model | Input | Output |
|-------|-------|--------|
| claude-opus-4-6 | $5/M | $25/M |
| claude-opus-4-5-20251101 | $5/M | $25/M |
| claude-sonnet-4-5-20250929 | $3/M | $15/M |
| claude-sonnet-4-20250514 | $3/M | $15/M |
| claude-haiku-4-5-20251001 | $1/M | $5/M |
| claude-haiku-3-5-20241022 | $0.80/M | $4/M |

Cache tokens: writes 1.25x, reads 0.1x base input price.

## Development

### Prerequisites

- Go 1.25+
- [Task](https://taskfile.dev/) (task runner)
- [golangci-lint](https://golangci-lint.run/) (optional, for linting)

### Building

```bash
task build              # Build binary → dist/chunk
task test               # Run tests
task lint               # Run linters
task acceptance-test    # Run acceptance tests
```

## Changelog

### Unreleased

- **Breaking**: Default `--output` path changed from `./review-prompt.md` to `.chunk/context/review-prompt.md`. The new path places the generated prompt directly where AI coding agents auto-discover context files. Parent directories are created automatically. If a legacy `./review-prompt.md` file exists when using the new default, a deprecation notice is printed. Pass `--output ./review-prompt.md` to restore the old behavior.

---

See [AGENTS.md](AGENTS.md) for AI agent instructions.
