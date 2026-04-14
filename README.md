# chunk

CLI for remote validation of changes — run code in a cloud environment before pushing — and generating agent context from code review patterns.

## Features

- **Context Generation** — Mines PR review comments from GitHub, analyzes them with Claude, and outputs a markdown prompt file tuned to your team's standards
- **Hook Automation** — Wire tests and lint into your AI coding agent's lifecycle (Claude Code, Cursor, VS Code Copilot)
- **Environment Detection** — Auto-detect tech stack, generate Dockerfiles, and set up sandboxes with the right dependencies
- **Sandbox Environments** — Validate changes in a clean cloud environment on CircleCI

## Requirements

- **macOS** (arm64 or x86_64) or **Linux** (arm64 or x86_64)

## Installation

```bash
brew install CircleCI-Public/circleci/chunk
```

## Quick Start

### Project Setup

Initialize your project for hook automation and validation:

```bash
# Detect test commands, configure hooks, set up .claude/settings.json
chunk init

# Run configured validations
chunk validate              # all commands
chunk validate tests        # specific command
chunk validate --list       # list configured commands
```

### Context Generation

Generate a review context prompt from your org's GitHub PR comments:

```bash
# From inside a git repo — org and repos are auto-detected
chunk build-prompt

# Or specify explicitly
chunk build-prompt --org myorg --repos api,backend --top 10

# Output lands in .chunk/context/review-prompt.md

# Install the review skill so Claude Code uses the prompt during reviews
chunk skill install
```

### Sandbox Environments (private preview, email ai-feedback@circleci.com)

Create and work in cloud sandbox environments:

```bash
# Authenticate
chunk auth login

# Create a sandbox
chunk sandbox create --name my-sandbox --image ubuntu:22.04

# Sync local files and SSH in
chunk sandbox sync --sandbox-id <id>
chunk sandbox ssh --sandbox-id <id>

# Or run a command directly
chunk sandbox ssh --sandbox-id <id> -- make test
```

#### Environment Detection and Setup

Auto-detect your tech stack and set up a sandbox with the right dependencies:

```bash
# Detect environment — saves to .chunk/config.json
chunk sandbox env --dir .

# Set up a sandbox from the detected environment
chunk sandbox env setup --name my-sandbox

# Or build a local Docker test image
chunk sandbox env | chunk sandbox build --dir .
```

#### Templates

Create reusable sandbox templates from container images:

```bash
chunk sandbox template create --image ubuntu:22.04 --tag my-template
chunk sandbox create --name my-sandbox --template-id <template-id>
```

## Commands

```
chunk auth login|status|logout       Authentication
chunk sandbox list|create|exec|ssh   Manage cloud sandbox environments
chunk sandbox sync|env|build         Sync files, detect env, build images
chunk sandbox env setup              Create sandbox and run setup steps
chunk sandbox template create        Create sandbox templates
chunk init                           Initialize project configuration
chunk validate [name]                Run quality checks
chunk skill install|list             Manage AI agent skills
chunk task config|run                Configure and trigger CI tasks
chunk build-prompt                   Generate review context from PR comments
chunk completion install|uninstall   Shell completions
chunk upgrade                        Update CLI
```

See [docs/CLI.md](docs/CLI.md) for the full command and flag reference.

## Configuration

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Anthropic API key (required for `build-prompt`; optional for `init`) |
| `GITHUB_TOKEN` | GitHub PAT with `repo` scope (for `build-prompt`) |
| `CIRCLE_TOKEN` | CircleCI personal API token (for `sandbox` and `task`) |
| `CHUNK_HOOK_ENABLE` | Enable/disable hook automation (`0`/`1`) |

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the complete environment variable reference.

## Platform Support

| Platform | Status |
|----------|--------|
| macOS (Apple Silicon) | Supported |
| macOS (Intel) | Supported |
| Linux (arm64) | Supported |
| Linux (x86_64) | Supported |
| Windows | Not supported |

## Development

### Prerequisites

- Go 1.26+
- [Task](https://taskfile.dev/) (task runner)
- [golangci-lint](https://golangci-lint.run/) (optional, for linting)

### Building

```bash
task build              # Build binary -> dist/chunk
task test               # Run tests
task lint               # Run linters
task acceptance-test    # Run acceptance tests
```

To build and install from source:

```bash
task build && cp dist/chunk ~/.local/bin/
```

Acceptance tests that clone repositories are skipped by default. Set `CHUNK_ENV_BUILDER_ACCEPTANCE=1` to enable them. To avoid re-cloning on repeated runs, set `CHUNK_SANDBOX_CACHE_DIR` to a persistent directory.

---

See [AGENTS.md](AGENTS.md) for AI agent instructions.
