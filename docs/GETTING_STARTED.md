# Getting Started with chunk

`chunk` runs your quality checks (tests, lint, format) on a **sidecar** — a clean cloud environment on CircleCI — while your AI agent is still working. Failures surface in the inner loop instead of in CI, so you catch what only reproduces outside your machine before you ever push.

This guide sets that up: authenticate, initialize your project, create a sidecar, and run the dev loop against it.

It also covers **team review context** ([below](#team-review-context)) — an additional tool that mines your PR review history into a prompt file your agents read automatically. Useful, but independent of the sidecar workflow; skip it until the loop above is working.

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

### Sidecars

A **sidecar** is an ephemeral Linux environment running on CircleCI. Instead of running tests locally, you sync your working tree to a sidecar and run checks there. This catches failures caused by local environment differences (different OS, missing dependencies, port conflicts) before they reach CI.

Sidecars are available to all CircleCI customers, including the free plans.

### Skills

**Skills** are instructions for AI coding agents (Claude Code, Cursor, Codex). Running `chunk skill install` copies skill files into your agent's configuration directory, teaching it how to run the sidecar dev loop and commands like `/chunk-review`.

### .chunk/ directory

The `.chunk/` directory lives at the root of your project and holds configuration that should be version-controlled. After `chunk init` it holds your validation commands; `chunk build-prompt` adds the review context file if you generate one:

```
.chunk/
├── config.json              # Configured validation commands
└── context/
    └── review-prompt.md     # Generated team review standards (optional)
```

---

## Step 1: Authenticate

Store credentials for the services you plan to use:

```bash
# CircleCI — required for sidecars. Browser OAuth is recommended:
chunk auth login           # existing CircleCI account
chunk auth signup          # new CircleCI account

# Or set a personal API token directly:
chunk auth set circleci

chunk auth set anthropic   # required for init (command detection) and build-prompt
chunk auth set github      # only needed for build-prompt
```

Check status at any time:

```bash
chunk auth status
```

Credentials are stored in your system keychain by default. Pass `--insecure-storage` to `chunk auth set` to store them in `~/.config/chunk/config.json` instead (respects `XDG_CONFIG_HOME`). You can also set them as environment variables, which take precedence over both:

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

Run this after authenticating — init uses your CircleCI credentials to detect your org ID and save it to the project config. If you belong to one org it's selected automatically; if you belong to several, init shows a picker. Org ID is needed for sidecar commands, so capturing it here means you won't be prompted later.

What it creates:

- **`.chunk/config.json`** — list of validation commands (test, lint, format) and your CircleCI org ID; tracked in git
- **`.claude/settings.json`** — hooks that run validation before commits and after each agent session; tracked in git
- **`.codex/hooks.json`** — the same hooks, for Codex sessions (only written if Codex is installed); tracked in git
- **`.git/hooks/pre-commit`** — runs `chunk validate` locally before every commit; not tracked in git

`chunk init` prints a one-line explanation after each of these hidden files so it's clear what got added and why.

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

## Step 3: Create your first sidecar

Sidecar commands need a CircleCI org ID. The recommended path is to authenticate and run `chunk init` before using sidecars — init captures the org ID automatically and saves it to `.chunk/config.json`. After that, `chunk sidecar create` just works.

If you skipped that or need to set the org ID manually:

```bash
# List your orgs — single-org users can skip this, init auto-selected it
chunk org list

# Persist the ID to the project config
chunk config set orgID <your-org-id>
```

`chunk config show` displays the resolved `orgID` when set. One-off overrides:
`chunk sidecar create --org-id <id>` or `CIRCLECI_ORG_ID=<id> chunk sidecar create`.
See [docs/CLI.md](CLI.md) for the full resolution order (`--org-id` → env →
project config → interactive picker).

---

## Step 4: Run the dev loop

This is the core workflow — sync your working tree to the sidecar, then run your validation commands there:

```bash
# Create a sidecar (--name is optional; a random name is generated if omitted)
chunk sidecar create
chunk sidecar create --name my-sidecar

# Set it as active
chunk sidecar use <id>

# Mark which commands belong on the sidecar (once per project)
chunk validate --list               # tags each command [local|remote, role]
chunk validate --mark-remote        # all but autofix commands (formatters stay local)
chunk validate --mark-remote test   # or just one, autofix included if you name it

# Dev loop: sync then validate
chunk sidecar sync           # push local changes to sidecar
chunk validate               # marked commands run on the sidecar, the rest locally
chunk validate --remote      # or force every command onto the sidecar

# Inspect or clear the active sidecar
chunk sidecar current        # show which sidecar is active
chunk sidecar forget         # unset the active sidecar (does not delete it)
```

Per-command routing only applies while `validation.sidecarImage` is unset. Once you record a snapshot ID there, `chunk validate` sends every command to the sidecar regardless of its `remote` flag — run formatters directly if you need them rewriting local files.

With no `validation.sidecarImage` recorded, a sidecar that has to be created is started from whichever of your org's snapshots best fits the repo — one named after the repo first, then one built for the detected stack. The chosen snapshot and the reason are printed. If no snapshot fits, the default image is used, which has none of your dependencies on it; record a snapshot ID to pin the environment instead of relying on the match.

The active sidecar and snapshot state are stored in `$XDG_DATA_HOME/chunk/<project>/` (default: `~/.local/share/chunk/<project>/`) — never inside the repo. The project key is derived from the git root path.

Or hand this off to the `chunk-sidecar` skill:

```
validate on the sidecar
run the tests on the sidecar
```

The skill handles the full loop: auth checks → find active sidecar → sync → validate → interpret failures → fix locally → repeat.

---

## Step 5: Install skills

Skills let your AI coding agent drive the loop above itself, instead of you running each command by hand.

```bash
chunk skill install     # install or update all skills
chunk skill list        # check installation status
```

After installing, your agent gains these skills:

| Skill | Trigger | What it does |
|---|---|---|
| `chunk-sidecar` | "validate on the sidecar" / "sidecar dev loop" | Syncs and validates changes on a sidecar |
| `chunk-sidecar-setup` | "set up chunk sidecar" / "walk me through sidecar setup" | Interactive first-time onboarding: auth, orgID, create, install deps, snapshot |
| `chunk-testing-gaps` | "find testing gaps" / "mutation test" | Runs mutation testing on parallel sidecars to find undertested code |
| `debug-ci-failures` | "debug CI" / "why is CI failing" | Analyzes CircleCI build failures and flaky tests |
| `chunk-review` | "review my changes" / "chunk review" | Applies your team's review standards to the current diff |

See [docs/SKILLS.md](SKILLS.md) for full details on each skill.

**Shortcut:** rather than doing Steps 3–4 by hand, you can hand them to your agent. Say "set up chunk sidecar" and the `chunk-sidecar-setup` wizard walks through auth, orgID, creation, dependency installation, and snapshotting — then hands off to `chunk-sidecar` for the dev loop.

---

## Sidecar reference

The sections below cover the sidecar workflow in more detail. Skip them until you need something specific.

### Syncing

`chunk sidecar sync` uses git bundle by default — the first sync sends a full bundle of HEAD, and subsequent syncs send only the new commits since the last sync (`<lastRef>..HEAD`). Uncommitted working-tree changes are applied on top as a patch. The branch does not need to be pushed to GitHub.

```bash
chunk sidecar sync
```

To fall back to the git checkout/patch approach (requires the branch to be pushed to GitHub):

```bash
chunk sidecar sync --checkout
```

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

### Monitoring sidecars with chunk watch

`chunk watch` opens a live TUI dashboard that shows all your sidecars and their activity in real time. Run it in any project directory while a sync or validation is in progress:

```bash
chunk watch
```

The dashboard refreshes every 5 seconds. The left pane lists your sidecars grouped by project, showing sync state and last activity time. The right pane shows the activity log — sync, validate, exec, and setup events — for the selected sidecar.

```
chunk watch  1 sidecar  main@a3f9e12                      15:04:32
──────────────────────────────────────────────────────────────────
 sidecars              │ activity  chunk-cli/my-sidecar
                       │
── chunk-cli           │ 14:58:01  sync      ✓  done
▶ my-sidecar           │ 14:55:12  validate  ✓  done
  ✓ in sync            │ 14:52:44  sync      ✓  done
  6m ago               │
──────────────────────────────────────────────────────────────────
  ↑/↓ j/k  select  ·  q  quit
```

`watch` shows every project you've watched before, not just the current one:

```bash
chunk watch                   # all projects you've watched before
chunk watch --focus           # current directory only
chunk watch /path/to/other    # add another project
```

`watch` requires a TTY — it will not run in a non-interactive shell (CI, pipes).

### Snapshots

Capture a configured environment so future sidecars boot fast:

```bash
chunk sidecar snapshot list
chunk sidecar snapshot create --name checkpoint
# Later:
chunk sidecar create --image <snapshot-id>           # name auto-generated
```

`snapshot list` prints each snapshot's name and ID for your org (from `--org-id`, project config, or the org picker). `snapshot create` deletes the source sidecar once the snapshot is captured to avoid leaking the build instance. If it was the active sidecar, local active-sidecar state is cleared too — launch a new one from the snapshot to resume work.

### Lock file regeneration

Lock files (`package-lock.json`, `yarn.lock`, `pnpm-lock.yaml`) record platform-specific package variants. When you run `npm install` on macOS, the lock file gains macOS-specific entries (e.g. `@emnapi/*` native bindings). CI then fails with `npm ci` on Linux because those entries don't match the Linux resolver.

**Fix: regenerate the lock file on the sidecar (Linux) and pull it back.**

```bash
# 1. Sync your local tree to the sidecar
chunk sidecar sync

# 2. Run the package manager install on the sidecar to regenerate the lock file
chunk validate --remote --cmd "npm install"   # or yarn install / pnpm install

# 3. Pull the updated lock file back to your local working tree
#    Replace <repo> with your repository name (e.g. my-app)
chunk sidecar ssh -- cat /home/user/<repo>/package-lock.json > package-lock.json

# 4. Commit the Linux-generated lock file
git add package-lock.json
git commit -m "Regenerate lock file on Linux"
```

> **Note:** `chunk sidecar sync` is one-way (local → sidecar). Step 3 manually pulls only the lock file back; it does not affect any other files. Run this workflow whenever you add or upgrade a dependency on macOS before pushing to CI.

---

## Smarter Testing on a sidecar

CircleCI Smarter Testing splits your test suite into independent **atoms** and distributes them across parallel shards so CI runs finish faster. The split is driven by `.circleci/test-suites.yml`, a file that declares how to **discover** atoms and how to **run** them. Sidecars are an ideal place to validate this file before pushing — they run Linux (matching CI), have the `circleci-testsuite` plugin and `circleci` CLI pre-installed, and automatically receive your `CIRCLE_TOKEN` over SSH.

### Scaffold `.circleci/test-suites.yml`

**Built-in templates (Go and pytest):**

By default, `chunk init` detects `go.mod` or `pyproject.toml` and writes a matching template. If the file already exists it is left as-is. You can skip this by passing `--skip-test-suites=false` to `chunk init`.

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

A successful Stop-hook run is cached, so if the hook fires again with nothing changed it prints `skipped (no changes since last successful run)` rather than re-running your commands. Failures are never cached. Running `chunk validate` yourself always executes the commands.

See [docs/HOOKS.md](HOOKS.md) for configuration details and [Result Caching](HOOKS.md#result-caching) for what invalidates the cache.

---

## Team review context

An additional tool, independent of the sidecar workflow above. `chunk build-prompt` mines your GitHub PR history and generates a prompt that captures how your team reviews code. Run it once and commit the output.

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

Requires `chunk auth set anthropic` and `chunk auth set github`.

Once that file exists and skills are installed, ask your agent to review:

```
chunk review
review my changes
review PR #123
```

The agent loads your team's prompt, diffs the changes, and returns filtered findings (Critical/High issues, capped at 10 comments).

---

## Typical day-to-day workflow

```
Make changes

Validate on a sidecar
    └─ chunk sidecar sync + chunk validate --remote   (or locally: chunk validate)
    └─ or hand it to the agent: "validate on the sidecar"
    └─ chunk watch   (optional: live dashboard to see sync/validate progress)

Before committing
    └─ Hook runs chunk validate automatically

Push

Optionally, if you generated review context
    └─ Agent picks up .chunk/context/review-prompt.md automatically
    └─ "chunk review" → agent applies team standards → filtered findings
```

---

## Command reference

See [docs/CLI.md](CLI.md) for the full command and flag reference.
