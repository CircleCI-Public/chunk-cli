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

### Sidecar Environments

Create and work in cloud sidecar environments. Sidecars are available to CircleCI customers on a paid plan. Share feedback in the [CircleCI Discord](https://discord.gg/circleci).

```bash
# Authenticate
chunk auth set circleci

# Create a sidecar (sets it as active automatically)
chunk sidecar create --name my-sidecar

# Sync local files and validate remotely
chunk sidecar sync
chunk validate --remote

# SSH in interactively or run a one-off command
chunk sidecar ssh
chunk sidecar ssh -- make test
```

#### Active sidecar

Most sidecar commands operate on the *active* sidecar so you don't have to pass `--sidecar-id` every time:

```bash
chunk sidecar use <id>      # set active sidecar
chunk sidecar current       # show which sidecar is active
chunk sidecar forget        # clear the active sidecar
```

#### Environment Detection and Setup

Auto-detect your tech stack, install dependencies, and snapshot the result so future sidecars boot fast:

```bash
# Detect environment, run install steps, and create a snapshot
chunk sidecar setup --name my-sidecar

# Or build a local Docker test image from the detected environment
chunk sidecar env | chunk sidecar build --dir .
```

#### Snapshots

Capture a configured environment so future sidecars start from a known-good state:

```bash
chunk sidecar snapshot create --name checkpoint
# Later:
chunk sidecar create --name new-sidecar --image <snapshot-id>
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

## Commands

```
chunk auth set|status|remove               Authentication
chunk sidecar list|create|exec|ssh         Manage cloud sidecar environments
chunk sidecar sync|env|build               Sync files, detect env, build images
chunk sidecar use|current|forget           Manage active sidecar
chunk sidecar setup                        Detect env, install deps, snapshot
chunk sidecar snapshot create|get          Manage sidecar snapshots
chunk init                                 Initialize project configuration
chunk validate [name]                      Run quality checks
chunk skill install|list                   Manage AI agent skills
chunk task config|run                      Configure and trigger CI tasks
chunk build-prompt                         Generate review context from PR comments
chunk completion install|uninstall         Shell completions
chunk upgrade                              Update CLI
```

See [docs/CLI.md](docs/CLI.md) for the full command and flag reference.

## Documentation

| Doc | Purpose |
|-----|---------|
| [docs/GETTING_STARTED.md](docs/GETTING_STARTED.md) | Step-by-step guide: auth, init, build-prompt, skills, sidecar workflow |
| [docs/SKILLS.md](docs/SKILLS.md) | Skills reference: installing, triggering, and troubleshooting agent skills |
| [docs/CLI.md](docs/CLI.md) | Full command and flag reference |
| [docs/HOOKS.md](docs/HOOKS.md) | Pre-commit and Stop hook configuration |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Internal module layout and dependency rules |

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
