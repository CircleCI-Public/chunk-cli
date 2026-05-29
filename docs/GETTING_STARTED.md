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

Sidecars are in preview for CircleCI customers on Performance and Scale plans.

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

Credentials are stored in `~/.config/chunk/config.json` (respects `XDG_CONFIG_HOME`). You can also set them as environment variables:

| Variable | Used by |
|---|---|
| `ANTHROPIC_API_KEY` | `build-prompt`, `init` |
| `GITHUB_TOKEN` | `build-prompt` |
| `CIRCLE_TOKEN` | `sidecar`, `task` |
| `CIRCLECI_ORG_ID` | `sidecar` (optional; overrides `orgID` in `.chunk/config.json`) |

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

## Sidecar workflow (preview)

### First-time sidecar setup

Before creating a sidecar in a non-interactive session (AI agents, scripts), set
your CircleCI org ID once per project:

```bash
chunk auth set circleci
chunk config set orgID <your-org-id>
chunk sidecar create
```

`chunk config show` displays the resolved `orgID` when set. One-off overrides:
`chunk sidecar create --org-id <id>` or `CIRCLECI_ORG_ID=<id> chunk sidecar create`.
See [docs/CLI.md](CLI.md) for the full resolution order (`--org-id` → env →
project config → interactive picker).

### Dev loop

Sidecars let you run validations in a clean cloud environment. The typical loop:

```bash
# Create a sidecar (--name is optional; a random name is generated if omitted)
chunk sidecar create
chunk sidecar create --name my-sidecar

# Set it as active
chunk sidecar use <id>

# Dev loop: sync then validate
chunk sidecar sync           # push local changes to sidecar
chunk validate --remote      # run validate commands on sidecar
```

The active sidecar and snapshot state are stored in `$XDG_DATA_HOME/chunk/<project>/` (default: `~/.local/share/chunk/<project>/`) — never inside the repo. The project key is derived from the git root path.

Or hand this off to the `chunk-sidecar` skill:

```
validate on the sidecar
run the tests on the sidecar
```

The skill handles the full loop: auth checks → find active sidecar → sync → validate → interpret failures → fix locally → repeat.

### Environment setup

Auto-detect your tech stack and save it to config:

```bash
chunk sidecar env   # detect stack, save to config
```

### Environment variables

`chunk sidecar ssh`, `chunk sidecar setup`, and `chunk validate` (when running remotely) automatically load `.env.local` from your working directory and forward its variables to the remote sidecar session. This lets you pass secrets and configuration without embedding them in your shell or committing them to the repo.

```bash
# .env.local is loaded automatically — no flag needed
chunk sidecar ssh
chunk validate --remote

# Override with a different file
chunk sidecar ssh --env-file /path/to/other.env
chunk validate --remote --env-file /path/to/other.env

# Add individual variables (merged on top of the file)
chunk sidecar ssh --env MY_VAR=value
chunk validate --remote --env MY_VAR=value
```

Variables from `--env` flags take precedence over those in `--env-file`. `.env.local` is gitignored by convention, so it's a safe place to store project-specific secrets.

### Snapshots

Capture a configured environment so future sidecars boot fast:

```bash
chunk sidecar snapshot list
chunk sidecar snapshot create --name checkpoint
# Later:
chunk sidecar create --image <snapshot-id>           # name auto-generated
```

`snapshot list` prints each snapshot's name and ID for your org (from `--org-id`, project config, or the org picker). `snapshot create` deletes the source sidecar once the snapshot is captured to avoid leaking the build instance. If it was the active sidecar, local active-sidecar state is cleared too — launch a new one from the snapshot to resume work.

---

## Smarter Testing on a sidecar

CircleCI Smarter Testing splits your test suite into independent **atoms** and distributes them across parallel shards so CI runs finish faster. The split is driven by `.circleci/test-suites.yml`, a file that declares how to **discover** atoms and how to **run** them. Sidecars are an ideal place to validate this file before pushing — they run Linux (matching CI), have the `circleci-testsuite` plugin and `circleci` CLI pre-installed, and automatically receive your `CIRCLE_TOKEN` over SSH.

### Scaffold `.circleci/test-suites.yml`

**Built-in templates (Go and pytest):**

```bash
chunk init --skip-test-suites=false
```

> `--skip-test-suites` defaults to `true`, so you must pass `=false` explicitly. The `=` is required — `--skip-test-suites false` does not work with boolean cobra flags.

This detects `go.mod` or `pyproject.toml` and writes a matching template. If the file already exists it is left as-is.

**Other stacks (Jest, RSpec, etc.):** write the file manually or ask your AI agent to `"scaffold test-suites.yml"` (the `chunk-sidecar` skill covers per-language patterns). The file schema:

```yaml
---
name: <suite-name>
discover: <shell command that prints one test atom per line>
run: <shell command that runs atoms in << test.atoms >>, writing junit XML to << outputs.junit >>>
outputs:
  junit: <path/to/junit.xml>
```

CircleCI substitutes `<< test.atoms >>` and `<< outputs.junit >>` at run time. Example for Jest:

```yaml
---
name: ci tests
discover: npx jest --listTests
run: JEST_JUNIT_OUTPUT_FILE=<< outputs.junit >> npx jest --reporters=default --reporters=jest-junit << test.atoms >>
outputs:
  junit: test-reports/tests.xml
```

### Validate on the sidecar

After writing `.circleci/test-suites.yml`, use the sidecar to verify it works in a CI-like environment:

```bash
# 1. Push your local tree (including the new file) to the sidecar
chunk sidecar sync

# 2. Test discover — should print one atom per line and exit zero
chunk validate --remote --cmd "go list -f '{{ if or (len .TestGoFiles) (len .XTestGoFiles) }} {{ .ImportPath }} {{end}}' ./..."

# 3. Test run with a sample atom — should produce junit XML at the declared path
chunk validate --remote --cmd "go tool gotestsum --junitfile=test-reports/tests.xml -- -race ./internal/config/..."

# 4. Validate your CircleCI config references the suite correctly
chunk validate --remote --cmd "circleci config validate"
```

Replace the Go commands above with your stack's `discover` and `run` commands. For the run step, substitute `<< test.atoms >>` with one or two atoms from discover's output and `<< outputs.junit >>` with the `outputs.junit` path from your YAML.

### Why use a sidecar for this

- **Pre-installed tooling** — `circleci-testsuite` and `circleci` CLI are available on every sidecar without any install step.
- **Automatic auth** — `CIRCLE_TOKEN` is forwarded over SSH to the sidecar session, so authenticated `circleci` commands work out of the box. You do not need to set the token manually on the sidecar.
- **CI parity** — the sidecar runs Linux, catching path separator issues, case sensitivity, and missing system dependencies that pass on macOS but fail in CI.

### Wire up `.circleci/config.yml`

After validating the suite, reference it from your CircleCI config using the `circleci-testsuite` plugin in your test job:

```yaml
jobs:
  test:
    docker:
      - image: cimg/go:1.26
    steps:
      - checkout
      - run:
          name: Run tests
          command: |
            circleci-testsuite exec \
              --suite "ci tests" \
              --results-dir test-reports
      - store_test_results:
          path: test-reports
```

The `--suite` value must match the `name` field in `.circleci/test-suites.yml`. The plugin handles discovery, atom assignment, and result collection.

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
