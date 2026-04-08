# chunk

CLI for generating AI agent context from code review patterns and managing cloud sandbox development environments.

## Features

- **Context Generation** — Mines PR review comments from GitHub, analyzes them with Claude, and outputs a markdown prompt file tuned to your team's standards
- **Sandbox Environments** — Create, sync, and SSH into cloud dev sandboxes on CircleCI
- **Environment Detection** — Auto-detect tech stack, generate Dockerfiles, and set up sandboxes with the right dependencies
- **Hook Automation** — Wire tests and lint into your AI coding agent's lifecycle (Claude Code, Cursor, VS Code Copilot)
- **Skills** — Install bundled AI agent skills into your editor
- **Self-Updating** — Built-in upgrade command

## Requirements

- **macOS** (arm64 or x86_64) or **Linux** (arm64 or x86_64)
- `gh` CLI installed and authenticated (`gh auth login`) for context generation

## Installation

```bash
brew install CircleCI-Public/circleci/chunk
```

## Quick Start

### Context Generation

Generate a review context prompt from your org's GitHub PR comments:

```bash
# Authenticate
chunk auth login

# From inside a git repo — org and repos are auto-detected
chunk build-prompt

# Or specify explicitly
chunk build-prompt --org myorg --repos api,backend --top 10

# Output lands in .chunk/context/review-prompt.md
# AI agents (Claude Code, Cursor) pick it up automatically
```

### Sandbox Environments

Create and work in cloud sandbox environments:

```bash
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

## Commands

```
chunk auth login|status|logout       Authentication
chunk build-prompt                   Generate review context from PR comments
chunk init                           Initialize project configuration
chunk sandbox list|create|exec|ssh   Manage cloud sandbox environments
chunk sandbox sync|env|build         Sync files, detect env, build images
chunk sandbox env setup              Create sandbox and run setup steps
chunk sandbox template create        Create sandbox templates
chunk skill install|list             Manage AI agent skills
chunk task config|run                Configure and trigger CI tasks
chunk validate [name]                Run quality checks
chunk completion install|uninstall   Shell completions
chunk upgrade                        Update CLI
```

See [docs/CLI.md](docs/CLI.md) for the full command and flag reference.

## Configuration

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Anthropic API key (for `build-prompt` and `validate`) |
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
