# chunk

CLI for [Chunk](https://chunk.ai/) — inner loop validation for software teams building with AI agents.

**Chunk sidecars** are lightweight microVM environments that run alongside your AI agent and validate changes in the inner loop, before they reach CI.

On top of sidecars, chunk provides:

- **`chunk validate`** to run your test, lint and format checks locally, on a sidecar, or in the background.
- **`chunk factory`** to have an implementer agent make a change on a sidecar while reviewers check it against the prompts in `.chunk/reviews/`, until the checks pass.
- **`chunk watch`** to follow and manage your factory runs live.
- **`chunk mutate`** to find test coverage gaps with mutation testing.

## Why chunk?

As AI churns out more and more code, CI pipelines get flooded with commits that haven't been well validated. Failures surface late, forcing expensive re-prompting cycles and slowing delivery.

Chunk sidecars fix this by running lightweight microbuilds to validate inside the inner loop — while the agent is still working — ensuring basic checks pass before anything hits CI. Save the CI for the integration and release work that gets code to production.

## Requirements

- **macOS** (arm64 or x86_64), **Linux** (arm64 or x86_64), or **Windows** (arm64 or x86_64)

## Installation

### macOS / Linux

```bash
brew install CircleCI-Public/circleci/chunk
```

### Windows

Download the latest `chunk-cli_Windows_x86_64.zip` or `chunk-cli_Windows_arm64.zip` from [GitHub Releases](https://github.com/CircleCI-Public/chunk-cli/releases), extract it, and put `chunk.exe` on your `PATH`.

## Quick Start

### Start here

```bash
chunk auth set anthropic-oauth   # 1. connect Claude with your subscription (recommended)
chunk auth set anthropic         #    or an Anthropic API key (or export ANTHROPIC_API_KEY)
chunk factory bootstrap          # 2. set up this project for factory runs
chunk watch                      # 3. follow your runs — keep it open; press n for a run, p for a plan
```

### Project Setup

To run quality checks automatically as your agent works, outside of factory runs, set up hooks:

```bash
# Detect test commands, configure hooks, set up .claude/settings.json
chunk init

# Run configured validations
chunk validate              # all commands
chunk validate test         # specific command
chunk validate --list       # list configured commands
```

### Agent Onboarding for Sidecars (Preferred method)

Chunk init will install skills for working with Chunk sidecars. After the init, start a claude session and run `/chunk-sidecar` and your agent will create a sidecar and configure it for use running tests and creating snapshots of good Chunk sidecars.

```bash
# Install or update the agent skills (Claude Code, Codex, opencode)
chunk skill install
```

### Manual setup

#### Sidecar Environments

Create and work in cloud sidecar environments. Sidecars are available to all CircleCI customers, including the free plans. Share feedback in the [CircleCI Discord](https://discord.gg/circleci).

```bash
# Authenticate (browser OAuth — recommended)
chunk auth signup           # create a new account
chunk auth login            # or log in to an existing account

# Or set a token directly
chunk auth set circleci

# Create a sidecar (sets it as active automatically)
chunk sidecar create --name my-sidecar

# Sync local files and validate remotely
chunk sidecar sync
chunk validate --remote

# SSH in interactively or run a one-off command
chunk sidecar ssh
chunk sidecar ssh -- make test

# Read the output of a command that ran on a sidecar
chunk sidecar logs <command-id> --follow

# Delete your sidecars created more than 24h ago (without --before, all of them;
# --skip-in-use keeps those this machine still refers to)
chunk prune --before 24h
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
chunk sidecar snapshot create --name my-env
# Later:
chunk sidecar create --name new-sidecar --image <snapshot-id>
```

`snapshot create` records the snapshot as `validation.sidecarImage` in `.chunk/config.json`, so `chunk validate` and `chunk factory` start their sidecars from it. If the project already records a different image, it asks before replacing it. Pass `--skip-config` for a throwaway checkpoint.

**Note:** `snapshot create` consumes the source sidecar — it is deleted once the snapshot is captured and cannot be reused. To resume work, launch a new sidecar from the snapshot with `chunk sidecar create --image <snapshot-id>`. Use `chunk sidecar snapshot list` to look up snapshot IDs for your org.

**Larger sidecars.** `--size small|medium|large|xlarge|2xlarge` picks the sidecar's size (default `large`). It works with a system image such as `cimg-go:1.27-claude`, once the image has been built at that size. A snapshot keeps the size of the sidecar it was taken from, so make the sidecar at the size you want, set it up, then snapshot it: `chunk sidecar create --image cimg-go:1.27-claude --size 2xlarge`, then `chunk sidecar snapshot create --name <name>`. `--size` is refused for a snapshot, and with it chunk does not fall back to the project's recorded or auto-picked snapshot.

## Factory

`chunk factory` has an implementer agent change your code on a sidecar, then reviews the result with each prompt in `.chunk/reviews/` and runs your validation commands, feeding failures back until every check passes or attempts run out. When they all pass, it pushes the run's branch and opens a pull request on GitHub, ready for review (`--no-open-pr` leaves the branch alone). It works in a git worktree of its own, so your checkout is never touched, and you can follow a run in `chunk watch`.

```bash
# Write a first set of review prompts from your code and past PR review comments
chunk factory bootstrap

# Implement a prompt, then review and validate it until it passes
chunk factory build "add a --verbose flag"

# Or make a first pass yourself in the run's worktree, then have the factory refine it
cd "$(chunk factory start "add a --verbose flag")"
# ...work on it...
chunk factory build --continue <run-id>
```

`factory bootstrap` only writes prompts when `.chunk/reviews/` has none, and the prompts are a starting point: read and edit them before relying on them.

## Other Tools

```bash
chunk watch                  # follow and manage your factory runs live (--focus: this directory only)
chunk mutate ./internal --parallel 4   # find coverage gaps; see docs/MUTATE.md
chunk conflicts              # does this branch still merge cleanly? (advisory, always exits 0)
chunk validate --async       # run in the background, then `chunk validate results`
chunk validate variants <file>   # run checks against code variants on a temporary sidecar pool
```

## Commands

```
chunk auth login|signup|set|status|remove  Authentication
chunk org create|list                      Manage CircleCI organizations
chunk sidecar list|create|delete|exec|ssh  Manage cloud sidecar environments
chunk sidecar sync|env|build|add-ssh-key   Sync files, detect env, build images, add SSH keys
chunk sidecar logs                         Print output of a command that ran on a sidecar
chunk sidecar use|current|forget           Manage active sidecar
chunk sidecar setup                        Detect env and install deps
chunk sidecar snapshot create|get|list     Manage sidecar snapshots
chunk prune                                Delete your sidecars
chunk init                                 Initialize project configuration
chunk config set|show                      Manage configuration
chunk validate [name]                      Run quality checks
chunk validate results|variants            Background run results; validate code variants
chunk factory bootstrap|build|start        Implement and review changes on sidecars
chunk watch                                Follow and manage your factory runs live
chunk mutate [path]                        Find test coverage gaps via mutation testing
chunk conflicts                            Report whether this branch merges cleanly
chunk hook disable|enable|status           Manage hook execution
chunk skill install|list                   Manage AI agent skills
chunk task config|run                      Configure and trigger CI tasks
chunk commands                             List all available commands
chunk completion install|uninstall         Shell completions
chunk upgrade                              Update CLI
```

See [docs/CLI.md](docs/CLI.md) for the full command and flag reference.

## Documentation

| Doc | Purpose |
|-----|---------|
| [docs/GETTING_STARTED.md](docs/GETTING_STARTED.md) | Step-by-step guide: auth, init, sidecar setup, dev loop, skills |
| [docs/SKILLS.md](docs/SKILLS.md) | Skills reference: installing, triggering, and troubleshooting agent skills |
| [docs/CLI.md](docs/CLI.md) | Full command and flag reference |
| [docs/HOOKS.md](docs/HOOKS.md) | Pre-commit and Stop hook configuration |
| [docs/MUTATE.md](docs/MUTATE.md) | Mutation testing with `chunk mutate` |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Internal module layout and dependency rules |

## Configuration

### Environment Variables

| Variable | Description |
|----------|-------------|
| `ANTHROPIC_API_KEY` | Anthropic API key (optional for `init`; for `factory`, or use `CLAUDE_CODE_OAUTH_TOKEN`) |
| `CLAUDE_CODE_OAUTH_TOKEN` | Claude subscription token from `claude setup-token` (for `factory`, when no API key is set) |
| `GITHUB_TOKEN` | GitHub PAT with `repo` scope (optional, for `factory bootstrap` to learn from past review comments) |
| `CIRCLECI_TOKEN` | CircleCI personal API token (for `sidecar` and `task`); `CIRCLE_TOKEN` also works |
| `CIRCLECI_ORG_ID` | CircleCI organization ID, used instead of `--org-id` |
| `CODE_REVIEW_CLI_MODEL` | Claude model override |

See [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) for the complete environment variable reference.

## Platform Support

| Platform | Status |
|----------|--------|
| macOS (Apple Silicon) | Supported |
| macOS (Intel) | Supported |
| Linux (arm64) | Supported |
| Linux (x86_64) | Supported |
| Windows (arm64) | Supported |
| Windows (x86_64) | Supported |

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
