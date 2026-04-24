# Getting Started with chunk

`chunk` is a CLI that does two related things:

1. **Generates team review context** — mines real PR review comments from GitHub, analyzes them with Claude, and produces a `.chunk/context/review-prompt.md` file that encodes how your team actually reviews code. AI coding agents pick this file up automatically and apply your team's standards.

2. **Validates changes remotely** — runs your quality checks (tests, lint, format) in a clean cloud environment on CircleCI before you push, so you catch failures that only reproduce outside your machine.

---

## Installation

```bash
brew install CircleCI-Public/circleci/chunk
```

Verify:

```bash
chunk --version
```

---

## Concepts

### .chunk/ directory

The `.chunk/` directory lives at the root of your project and holds configuration that should be version-controlled. After running `chunk init` and `chunk build-prompt`, it looks like:

```
.chunk/
├── config.json              # Configured validation commands
└── context/
    └── review-prompt.md     # Generated team review standards
```

### Sidecars

A **sidecar** is an ephemeral Linux environment running on CircleCI. Instead of running tests locally, you sync your working tree to a sidecar and run checks there. This catches failures caused by local environment differences (different OS, missing dependencies, port conflicts) before they reach CI.

Sidecars are in private preview — email ai-feedback@circleci.com to get access.

### Skills

**Skills** are instructions for AI coding agents (Claude Code, Cursor, Codex). Running `chunk skill install` copies skill files into your agent's configuration directory, teaching it commands like `/chunk-review` and how to run the sidecar dev loop.

---

## Step 1: Authenticate

Store credentials for the services you plan to use:

```bash
chunk auth set anthropic   # required for build-prompt and init
chunk auth set github      # required for build-prompt
chunk auth set circleci    # required for sidecars and task runs
```

Check status at any time:

```bash
chunk auth status
```

Credentials are stored in `~/.chunk/config.json`. You can also set them as environment variables:

| Variable | Used by |
|---|---|
| `ANTHROPIC_API_KEY` | `build-prompt`, `init` |
| `GITHUB_TOKEN` | `build-prompt` |
| `CIRCLE_TOKEN` | `sidecar`, `task` |

---

## Step 2: Initialize your project

Run this once per project. It auto-detects your test and lint commands (using Claude), creates `.chunk/config.json`, and wires up `.claude/settings.json` so hooks run automatically when your AI coding agent commits code.

```bash
chunk init
```

What it creates:

- **`.chunk/config.json`** — list of validation commands (test, lint, format)
- **`.claude/settings.json`** — hooks that run validation before commits and after each agent session

Review the generated config and adjust commands if needed:

```json
{
  "commands": [
    {"name": "format", "run": "task fmt",  "timeout": 30},
    {"name": "lint",   "run": "task lint", "timeout": 60},
    {"name": "test",   "run": "task test", "timeout": 300}
  ]
}
```

Run validations manually:

```bash
chunk validate           # run all commands
chunk validate test      # run a specific command
chunk validate --list    # list what's configured
chunk validate --dry-run # print commands without executing
```

---

## Step 3: Generate team review context

This step mines your GitHub PR history and generates a prompt that captures how your team reviews code. Run it once and commit the output.

```bash
# Auto-detects org and repos from your git remote
chunk build-prompt

# Or specify explicitly
chunk build-prompt --org myorg --repos api,backend --top 10 --since 2024-01-01
```

The pipeline runs three steps:

1. **Discover** — fetches PR review comments from GitHub, identifies top reviewers
2. **Analyze** — sends comments to Claude Sonnet to extract patterns
3. **Generate** — sends patterns to Claude Opus to produce a focused prompt

Output lands at `.chunk/context/review-prompt.md`. Commit this file — your team's AI agents will read it automatically.

---

## Step 4: Install skills

Skills install slash commands into your AI coding agents so they can run reviews and use sidecars.

```bash
chunk skill install     # install or update all skills
chunk skill list        # check installation status
```

After installing, your agent gains these skills:

| Skill | Trigger | What it does |
|---|---|---|
| `chunk-review` | "review my changes" / "chunk review" | Applies your team's review standards to the current diff |
| `chunk-sidecar` | "validate on the sidecar" / "sidecar dev loop" | Syncs and validates changes on a remote CircleCI environment |
| `chunk-testing-gaps` | "find testing gaps" / "mutation test" | Runs mutation testing to find undertested code |
| `debug-ci-failures` | "debug CI" / "why is CI failing" | Analyzes CircleCI build failures and flaky tests |

See [docs/SKILLS.md](SKILLS.md) for full details on each skill.

---

## Step 5: Review changes

Once `.chunk/context/review-prompt.md` exists and skills are installed, ask your agent to review:

```
chunk review
review my changes
review PR #123
```

The agent loads your team's prompt, diffs the changes, and returns filtered findings (Critical/High issues, capped at 10 comments).

---

## Sidecar workflow (private preview)

Sidecars let you run validations in a clean cloud environment. The typical loop:

```bash
# One-time: create a sidecar
chunk sidecar create --name my-sidecar

# Set it as active
chunk sidecar use <id>

# Dev loop: sync then validate
chunk sidecar sync           # push local changes to sidecar
chunk validate --remote      # run validate commands on sidecar
```

Or hand this off to the `chunk-sidecar` skill:

```
validate on the sidecar
run the tests on the sidecar
```

The skill handles the full loop: auth checks → find active sidecar → sync → validate → interpret failures → fix locally → repeat.

### Environment setup

Auto-detect your tech stack and build a sidecar image for it:

```bash
chunk sidecar env                                    # detect stack, save to config
chunk sidecar env | chunk sidecar build --tag myimg  # build Docker image
chunk sidecar create --name my-sidecar --image myimg
```

### Snapshots

Capture a configured environment so future sidecars boot fast:

```bash
chunk sidecar snapshot create --name checkpoint
# Later:
chunk sidecar create --name new-sidecar --image <snapshot-id>
```

---

## Hook behavior

After `chunk init`, two hooks run automatically in Claude Code and Cursor:

- **PreToolUse** — runs before every `git commit`. Blocks the commit if any validation command fails.
- **Stop** — runs when the agent finishes a session. Skips if the working tree is clean; runs all configured commands otherwise.

The Stop hook retries up to `stopHookMaxAttempts` times (default: 3) before giving up and letting the session end.

See [docs/HOOKS.md](HOOKS.md) for configuration details.

---

## Typical day-to-day workflow

```
Start coding session
    └─ Agent picks up .chunk/context/review-prompt.md automatically

Make changes
    └─ chunk sidecar sync + chunk validate --remote   (or locally: chunk validate)

Before committing
    └─ Hook runs chunk validate automatically

Ask for a review
    └─ "chunk review" → agent applies team standards → filtered findings

Push
```

---

## Command reference

See [docs/CLI.md](CLI.md) for the full command and flag reference.
