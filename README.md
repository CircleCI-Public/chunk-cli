# chunk

CLI for [Chunk](https://chunk.ai/) — inner loop validation and AI-ready code review context for software teams building with AI agents. The chunk CLI currently has two main capabilities:

- **Chunk sidecars** — lightweight microVM environments that run alongside your AI agent and validate changes in the inner loop before they reach CI
- **build-prompt** — mines PR review comments from your GitHub org and uses Claude to generate a context file tuned to your team's code review standards

## Why chunk?

As AI churns out more and more code, CI pipelines get flooded with commits that haven't been well validated. Failures surface late, forcing expensive re-prompting cycles and slowing delivery.

Chunk sidecars fix this by running lightweight microbuilds to validate inside the inner loop — while the agent is still working — ensuring basic checks pass before anything hits CI. Save the CI for the integration and release work that gets code to production.

The build-prompt command complements this by capturing your team's real review patterns and turning them into agent context, so the code agents write reflects your standards from the start.

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

### Agent Onboarding for Sidecars (Preferred method)

Chunk init will install skills for working with Chunk sidecars. After the init, start a claude session and run `/chunk-sidecars` and your agent will create a sidecar and configure it for use running tests and creating snapshots of good Chunk sidecars.

### Manual setup

#### Sidecar Environments

Create and work in cloud sidecar environments. Sidecars are available to all CircleCI customers, including the free plans. Share feedback in the [CircleCI Discord](https://discord.gg/circleci).

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

##### Active sidecar

Most sidecar commands operate on the *active* sidecar so you don't have to pass `--sidecar-id` every time:

```bash
chunk sidecar use <id>      # set active sidecar
chunk sidecar current       # show which sidecar is active
chunk sidecar forget        # clear the active sidecar
```

##### Environment Detection and Setup

Auto-detect your tech stack, install dependencies, and snapshot the result so future sidecars boot fast:

```bash
# Detect environment, run install steps, and create a snapshot
chunk sidecar setup --name my-sidecar

# Or build a local Docker test image from the detected environment
chunk sidecar env | chunk sidecar build --dir .
```

##### Snapshots

Capture a configured environment so future sidecars start from a known-good state:

```bash
chunk sidecar snapshot list
chunk sidecar snapshot create --name checkpoint
# Later:
chunk sidecar create --name new-sidecar --image <snapshot-id>
```

**Note:** `snapshot create` consumes the source sidecar — it is deleted once the snapshot is captured and cannot be reused. To resume work, launch a new sidecar from the snapshot with `chunk sidecar create --image <snapshot-id>`. Use `chunk sidecar snapshot list` to look up snapshot IDs for your org.

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
chunk sidecar setup                        Detect env and install deps
chunk sidecar snapshot create|get|list     Manage sidecar snapshots
chunk init                                 Initialize project configuration
chunk validate [name]                      Run quality checks
chunk hook disable|enable|status           Manage hook execution
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
