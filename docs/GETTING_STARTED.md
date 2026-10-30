# Getting Started with chunk

`chunk` runs your quality checks (tests, lint, format) on a **sidecar** — a clean cloud environment on CircleCI — while your AI agent is still working. Failures surface in the inner loop instead of in CI, so you catch what only reproduces outside your machine before you ever push.

This guide sets that up: authenticate, initialize your project, create a sidecar, and run the dev loop against it.

It also covers **review prompts** ([below](#review-prompts)) — prompts written from your code and your team's PR review history, which `chunk review`, `chunk factory` and the `chunk-review` skill review changes against. Useful, but independent of the sidecar workflow; skip it until the loop above is working.

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

**Skills** are instructions for AI coding agents (Claude Code, Codex, opencode). Running `chunk skill install` copies skill files into your agent's configuration directory, teaching it how to run the sidecar dev loop and commands like `/chunk-review`.

### .chunk/ directory

The `.chunk/` directory lives at the root of your project and holds configuration that should be version-controlled. After `chunk init` it holds your validation commands; add review prompts under `reviews/` yourself:

```
.chunk/
├── config.json              # Configured validation commands
└── reviews/
    └── <area>.md            # One review prompt per area, such as testing.md (optional)
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

chunk auth set anthropic   # required for init (command detection); review and factory accept it too
chunk auth set github      # optional: for the chunk daemon to follow your branches' pull requests
```

Check status at any time:

```bash
chunk auth status
```

Credentials are stored in your system keychain by default. Pass `--insecure-storage` to `chunk auth set` to store them in `~/.config/chunk/config.json` instead (respects `XDG_CONFIG_HOME`). You can also set them as environment variables, which take precedence over both:

| Variable | Used by |
|---|---|
| `ANTHROPIC_API_KEY` | `init`, `review`, `factory` |
| `CLAUDE_CODE_OAUTH_TOKEN` | `review`, `factory` (when no API key is set) |
| `GITHUB_TOKEN` | the chunk daemon (optional, to follow your branches' pull requests) |
| `CIRCLE_TOKEN` | `sidecar`, `task` |
| `CIRCLECI_ORG_ID` | `sidecar` (optional; overrides `orgID` in `.chunk/config.json`) |

---

## Step 2: Initialize your project

Run this once per project. It auto-detects your test and lint commands (using Claude), creates `.chunk/config.json`, and installs a git pre-commit hook that runs `chunk validate` before every commit.

```bash
chunk init
```

Run this after authenticating — init uses your CircleCI credentials to detect your org ID and save it to the project config. If you belong to one org it's selected automatically; if you belong to several, init shows a picker. Org ID is needed for sidecar commands, so capturing it here means you won't be prompted later.

What it creates:

- **`.chunk/config.json`** — list of validation commands (test, lint, format) and your CircleCI org ID; tracked in git
- **`.gitignore` entry for `.chunk/*-pool.json`** — pool state (sidecar IDs) written there by older versions; current versions keep it in the project data dir. Keeps `.chunk/config.json` tracked
- **`.git/hooks/pre-commit`** — runs `chunk validate` locally before every commit; not tracked in git

`chunk init` prints a one-line explanation after each of these hidden files so it's clear what got added and why.

Review the generated config and adjust commands if needed:

```json
{
  "commands": [
    {"name": "format", "run": "task fmt",  "timeout": 30, "local": true},
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

# Inspect command placement
chunk validate --list               # tags each command [local|remote, role]

# Dev loop: sync then validate
chunk sidecar sync           # push local changes to sidecar
chunk validate               # commands run remotely unless configured local
chunk validate --local       # force every command to run locally

# Inspect or clear the active sidecar
chunk sidecar current        # show which sidecar is active
chunk sidecar forget         # unset the active sidecar (does not delete it)
```

Commands run remotely by default. Set `"local": true` on commands that must run in the local working tree, such as formatters whose edits need to be retained. `"remote": true` remains supported as an explicit annotation, but is equivalent to leaving both fields unset. A command cannot set both `local` and `remote`.

With no `validation.sidecarImage` recorded, a sidecar that has to be created is started from whichever of your org's snapshots best fits the repo — one named after the repo first, then one built for the detected stack. The chosen snapshot and the reason are printed. If no snapshot fits, the default image is used, which has none of your dependencies on it; record a snapshot ID to pin the environment instead of relying on the match.

The active sidecar, sidecar pool and snapshot state are stored in `$XDG_DATA_HOME/chunk/<project>/` (default: `~/.local/share/chunk/<project>/`) — never inside the repo. The project key is derived from the git root path.

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
| `chunk-testing-gaps` | "find testing gaps" / "mutation test" | Runs mutation testing on a temporary sidecar pool to find undertested code |
| `debug-ci-failures` | "debug CI" / "why is CI failing" | Analyzes CircleCI build failures and flaky tests |
| `chunk-review` | "review my changes" / "chunk review" | Applies your team's review standards to the current diff |

See [docs/SKILLS.md](SKILLS.md) for full details on each skill.

**Shortcut:** rather than doing Steps 3–4 by hand, you can hand them to your agent. Say "set up chunk sidecar" and the `chunk-sidecar-setup` wizard walks through auth, orgID, creation, dependency installation, and snapshotting — then hands off to `chunk-sidecar` for the dev loop.

---

## Sidecar reference

The sections below cover the sidecar workflow in more detail. Skip them until you need something specific.

### Syncing

`chunk sidecar sync` mirrors your working tree to the sidecar with rsync over an SSH tunnel, skipping whatever git ignores. Ignored files already on the sidecar, such as a `.venv` it built, are kept between syncs. Uncommitted changes are included and later syncs transfer only what changed. The branch does not need to be pushed to GitHub.

```bash
chunk sidecar sync
```

To fall back to the git checkout/patch approach (requires the branch to be pushed to GitHub):

```bash
chunk sidecar sync --checkout
```

### SSH key management

chunk generates and manages its own SSH keypair at `~/.ssh/chunk_ai`. The public key is registered with each sidecar automatically when you first sync or SSH in — you do not need to create keys, run `ssh-add`, or pass an identity file.

If sync or SSH fails with `permission denied (publickey)`, re-register the key:

```bash
chunk sidecar add-ssh-key --public-key-file ~/.ssh/chunk_ai.pub
```

If the key is missing or corrupted, delete `~/.ssh/chunk_ai*` and chunk will regenerate the pair on next use.

### Validation pools

`chunk validate` currently uses a managed pool with capacity one. Remote commands are queued on that sidecar in configuration order, then commands explicitly configured with `local: true` run locally. Placement does not implicitly increase pool capacity.

`chunk validate variants` uses a temporary pool sized by `--parallel`. Variants are queued onto the next available pool member, and the pool is deleted when the run finishes.

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

`snapshot list` prints each snapshot's name and ID for your org (from `--org-id`, project config, or the org picker). `snapshot create` records the snapshot as `validation.sidecarImage` in `.chunk/config.json`, so `chunk validate` and `chunk factory` start their sidecars from it. If the project already records a different image, it asks before replacing it, and keeps it when there is no terminal to ask in. Pass `--skip-config` for a throwaway checkpoint. `snapshot create` also deletes the source sidecar once the snapshot is captured to avoid leaking the build instance. If it belonged to the active pool, that member is removed from local active-pool state; the state is cleared only when the pool becomes empty. Launch a new pool member from the snapshot to resume work.

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

## Planning and following flows with chunk watch

`chunk watch` is the recommended way to interact with chunk. Keep it open in
another terminal while you work. Planning needs the factory daemon running
with `FACTORY_PLAN_STORE=circleci`. Run it in the project you want to plan in:

```bash
chunk watch
```

`chunk watch` opens on **Flows**, your open flows in factory grouped by who
they wait on (Needs your review, With agent, Not started, Other; each heading
shows how many flows it holds) and then by phase (Planning, Building, ...), a
sub-plan marked `↳` with its parent (and indented under it when both are under
the same heading), a flow whose PR factory could not rebase marked ⚠, with a
read-only preview of the selected one's plan. Flows are yours wherever
`chunk watch` is run. `Enter` opens a flow's plan, `n` starts a new one, and
`q` or `esc` quits. Opening or
reading a plan never claims it; the request you type for a new plan does, and
the screen resumes a planner factory is waiting on, in the project
`chunk watch` was opened in. `ctrl+p` goes back to the list to
switch plans; each open plan keeps its activity while another is shown,
and its session keeps planning in the chunk daemon even once chunk is closed.
The planner's activity is on the left and the plan is on the right, under a strip that says
where the flow is and what comes next; with the plan focused, `←` shows the
Flow tab (progress, PR, related plans) and `→` the plan again. `tab` moves to
the plan, where `c` writes a comment right under the highlighted part, saved to factory
for chunk to address when you press `s`; `shift+↑/↓` selects several
parts to comment on together. There is no chat with the planner: comments
are how you change the plan, and questions go in comments too. Once factory's review of the design has
passed, `ctrl+s` approves the design, and chunk writes the units; once those
pass review too, `ctrl+s` approves the finished plan in factory, and the
chunk daemon builds it through to a pull request. The strip says which of
the two factory is waiting for: `Next: you approve the design · ctrl+s` or
`Next: you approve the plan · ctrl+s`. A plan whose PR
conflicts after factory's rebase reads PR conflicts, and `ctrl+s` hands it
back to factory to rebase again. The build
keeps going when you close chunk; plan as many as you like.
Once approved, the screen stays open on the Flow tab, which follows the
build: each unit done, being built or to do, and the pull request.
The left pane then shows the builder at work. If it warns that another
session holds the plan's claim, run the `factory daemon release` command it
names. A plan built on another machine, or before the daemon restarted,
shows no build activity here; the Flow tab still shows its progress. `x`
abandons a plan, and stops its build with it.
`esc` goes back to your plans with the plan kept. Quitting with a request or
comments still unsent in any plan asks for a second `ctrl+c`.
`chunk factory plan` opens the same screen on its own.

`ctrl+o` opens a chat you can ask about your plans and flows from any
screen; it answers straight away and changes nothing.

`watch` requires a TTY — it will not run in a non-interactive shell (CI,
pipes). It works with the chunk daemon on this machine, and refuses to start
with `CHUNK_DAEMON_REMOTE_ADDR` set. It takes no directories; `--focus` and
`--all` are deprecated and do nothing.

### Reading a command's output

The daemon keeps a copy of each command's output it was told about, even
after the process that ran it has exited. For scripts and for agents with no
terminal:

```bash
chunk sidecar logs <command-id>        # print a command's buffered output
chunk sidecar logs <command-id> -f     # keep printing until the command exits
```

Output is capped at 256 KiB per command and 20 commands per project, keeping the
most recent of each. Buffers live in memory, so restarting the daemon clears
them.

Output goes to stdout so it can be piped; a non-zero remote status is reported on
stderr as `exit status N`. `logs` itself fails only when *reading* failed.

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

## Review prompts

An additional tool, independent of the sidecar workflow above. Review prompts are one `.md` or `.txt` file per area in `.chunk/reviews/`, such as `testing.md`, written by your team. `chunk review` and `chunk factory plan` review changes with them. Commit them with the config so the whole team shares them.

`chunk factory bootstrap` sets a project up for `chunk factory` in one command. It writes `.chunk/config.json` if there is none, sets up a sidecar with your project's environment, and records a snapshot of it as `validation.sidecarImage` (`--skip-sidecar` leaves this out). It prints what it will do first and leaves out any part already done, so running it again finishes a setup that stopped partway.

```bash
chunk factory bootstrap
```

Requires a CircleCI token.

Once the prompts exist and skills are installed, ask your agent to review:

```
chunk review
review my changes
review PR #123
```

The agent runs one reviewer per prompt over the diff and returns the combined findings (High and Medium issues, capped at 10).

`chunk factory plan "<prompt>"` has factory make the change instead: you plan it with Claude, approve the plan, and it is built, checking the work with the same prompts and your validation commands, through to a pull request. `n` on Flows in `chunk watch` does the same ([above](#planning-and-following-flows-with-chunk-watch)).

---

## Typical day-to-day workflow

```
Make changes

Validate on a sidecar
    └─ chunk sidecar sync + chunk validate --remote   (or locally: chunk validate)
    └─ or hand it to the agent: "validate on the sidecar"
    └─ chunk watch   (recommended: keep it open to plan and follow your factory flows)

Before committing
    └─ The git pre-commit hook runs chunk validate

Push

Optionally, if you wrote review prompts
    └─ "chunk review" → one reviewer per prompt in .chunk/reviews/ → filtered findings
```

---

## Command reference

See [docs/CLI.md](CLI.md) for the full command and flag reference.
