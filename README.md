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
- **Fallback**: Bun 1.3+ (auto-installed via mise if available)

## Installation

### Flox

Add to `~/.flox/env/manifest.toml` under `[packages]`:

```toml
[packages]
chunk.flake = "github:CircleCI-Public/nur-packages#packages.aarch64-linux.chunk"
```

Replace `aarch64-linux` with `x86_64-linux` if you're on an x86_64 machine.

### Homebrew

```bash
brew install CircleCI-Public/circleci/chunk
```

### Install script

Requires `gh` (the GitHub CLI) installed and authenticated (`gh auth login`):

```bash
gh api -H "Accept: application/vnd.github.v3.raw" "/repos/CircleCI-Public/chunk-cli/contents/install.sh" | bash
```

This installs the binary to `~/.local/bin` and will warn you if that directory is not in your `$PATH`.

You can confirm the tool is installed by running:

```bash
chunk --version
```

### Building a local development version

To build and install from source:

```bash
./install-local.sh
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
| `--output <path>` | `./pr-review-prompt.md` | Output path for the generated prompt |
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

Once generated, place the output file in `.chunk/context/` so AI coding agents (e.g., Claude Code) automatically pick it up as context.

### task

Triggers and configures chunk pipeline runs.

#### Prerequisites

You will need three identifiers from CircleCI before running setup:

| Identifier | Where to find it |
|------------|-----------------|
| **Organization ID** | CircleCI app → Organization Settings → Overview |
| **Project ID** | CircleCI app → Project Settings → Overview |
| **Definition ID** | CircleCI app → the chunk pipeline definition page (UUID in the URL or settings) |

You will also need a CircleCI personal API token set as `CIRCLECI_TOKEN`:

```bash
export CIRCLECI_TOKEN=your-token-here
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

Run in your project root to scaffold the config files and wire up `.claude/settings.json`:

```bash
chunk hook repo init
```

This creates:

| File | Purpose |
|------|---------|
| `.chunk/hook/config.yml` | Per-repo hook configuration (commands, timeouts, triggers) |
| `.chunk/hook/code-review-instructions.md` | AI reviewer prompt |
| `.chunk/hook/code-review-schema.json` | Structured output schema for the review agent |
| `.chunk/hook/.gitignore` | Excludes runtime state files from git |
| `.claude/settings.json` | Hook wiring for Claude Code (and compatible IDEs) |

If any of these files already exist, the template is saved as a `.example` variant alongside the original so nothing is overwritten.

#### 3. Configure your commands

Edit `.chunk/hook/config.yml` to set the test and lint commands for your repo:

```yaml
execs:
  tests:
    command: "go test ./..."   # your test command
    fileExt: ".go"             # skip if no matching files changed
  lint:
    command: "golangci-lint run"
    timeout: 60
```

#### Other hook commands

```bash
chunk hook env update --profile <name>   # Update shell environment profile
chunk hook repo init --force             # Re-scaffold, overwriting existing files
```
### Other Commands

```bash
chunk auth login      # Set up API key
chunk auth status     # Check authentication
chunk config show     # Display current configuration
chunk upgrade         # Update to latest version
```

### Hook Commands

The `chunk hook` subcommand provides configurable quality checks for
[Claude Code hooks](https://docs.anthropic.com/en/docs/claude-code) — tests, lint, code review,
and more. See [packages/hook/README.md](packages/hook/README.md) for full documentation.

```bash
# Configure shell environment (PATH, CHUNK_HOOK_* vars):
chunk hook env update

# Initialize a repo with hook config templates:
chunk hook repo init

# Run a named shell command (tests, lint, etc.):
CHUNK_HOOK_ENABLE=1 echo '{}' | chunk hook exec run tests --cmd "go test ./..."

# Grouped checks (tests + review on the same event):
chunk hook sync check exec:tests task:review --on pre-commit
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

This repo uses [mise](https://mise.jdx.dev/) to manage tool versions.
`.mise.toml` at the repo root pins the required versions of Bun and Node.

Install mise (if you haven't already), then run:

```bash
mise install
```

With mise active, `bun` and `node` will resolve to the correct versions
automatically when you're inside this directory.

### Building

```bash
bun run build          # Build binaries for all platforms → dist/
bun run typecheck      # Type check without building
bun test               # Run test suite
```

### Releasing

Requires `gh` CLI authenticated (`gh auth login`).

```bash
# 1. Bump version in package.json
# 2. Build all platform binaries
bun run build

# 3. Validate (no side effects)
bun run release --dry-run

# 4. Tag + upload to GitHub Releases
bun run release

# Or create as draft for review first
bun run release --draft
```

The release script validates preflight checks (clean working dir, on `main`, tag available), generates SHA-256 checksums, creates an annotated git tag, and uploads all binaries to GitHub Releases.

---

See [AGENTS.md](AGENTS.md) for architecture and development documentation.
