# chunk

CLI for remote validation of changes — run code in a cloud environment before pushing — and generating agent context from code review patterns.

## Features

- **Context Generation** — Mines PR review comments from GitHub, analyzes them with Claude, and outputs a markdown prompt file tuned to your team's standards
- **Hook Automation** — Wire tests and lint into your AI coding agent's lifecycle (Claude Code, Cursor, VS Code Copilot)
- **Environment Detection** — Auto-detect tech stack, generate Dockerfiles, and set up sidecars with the right dependencies
- **Sidecar Environments** — Validate changes in a clean cloud environment on CircleCI

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

# Install review skills for Claude Code, Codex, and Cursor
chunk skill install
```

### Sidecar Environments (private preview, email ai-feedback@circleci.com)

Create and work in cloud sidecar environments:

```bash
# Authenticate
chunk auth set circleci

# Create a sidecar
chunk sidecar create --name my-sidecar --image ubuntu:22.04

# Sync local files and SSH in
chunk sidecar sync --sidecar-id <id>
chunk sidecar ssh --sidecar-id <id>

# Or run a command directly
chunk sidecar ssh --sidecar-id <id> -- make test
```

#### Environment Detection and Setup

Auto-detect your tech stack and set up a sidecar with the right dependencies:

```bash
# Detect environment — saves to .chunk/config.json
chunk sidecar env --dir .

# Set up a sidecar from the detected environment
chunk sidecar env setup --name my-sidecar

# Or build a local Docker test image
chunk sidecar env | chunk sidecar build --dir .
```

#### Templates

Create reusable sidecar templates from container images:

```bash
chunk sidecar template create --image ubuntu:22.04 --tag my-template
chunk sidecar create --name my-sidecar --template-id <template-id>
```

## Commands

```
chunk auth set|status|remove         Authentication
chunk sidecar list|create|exec|ssh   Manage cloud sidecar environments
chunk sidecar sync|env|build         Sync files, detect env, build images
chunk sidecar env setup              Create sidecar and run setup steps
chunk sidecar template create        Create sidecar templates
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
| `CIRCLE_TOKEN` | CircleCI personal API token (for `sidecar` and `task`) |

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
### Building

```bash
task build              # Build binary -> dist/chunk
task test               # Run tests
task lint               # Run linters
task acceptance-test    # Run acceptance tests
```

To build and install from source into `~/.local/bin` (make sure it's on your `PATH`):

```bash
task dev-install
```

Acceptance tests that clone repositories are skipped by default. Set `CHUNK_ENV_BUILDER_ACCEPTANCE=1` to enable them. To avoid re-cloning on repeated runs, set `CHUNK_SIDECAR_CACHE_DIR` to a persistent directory.

---

See [AGENTS.md](AGENTS.md) for AI agent instructions.
