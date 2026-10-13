# smelt

A self-hosted **software factory** for AI-built code. Give it a markdown spec;
it builds the change with Claude Code on an isolated branch, reviews it with a
panel of reviewer agents, runs your tests, lint and type checks, feeds every
failure back until it all passes, then pushes a branch and opens a PR for a
human to merge.

Everything runs **on your machine, in Docker**. No cloud sandboxes and no
third-party CI account are needed.

```
spec.md ──► implement ──► review ×N + validate ──► feedback ──► … ──► commit ──► push / PR
             (Claude, in a      (parallel, each in      (high/medium findings
              container)         its own container)      and failed checks)
```

> This is a fork of [CircleCI-Public/chunk-cli](https://github.com/CircleCI-Public/chunk-cli)
> that adds a local Docker backend, per-repo sandbox images, push/PR publishing and
> commit attribution. It is released and updated from this repository, and by
> default never contacts CircleCI. The original CircleCI cloud sidecars still
> work as an opt-in backend; see
> [CircleCI cloud sidecars (optional)](#circleci-cloud-sidecars-optional).
>
> smelt has its own name, folders (`.smelt/`, `~/.config/smelt`), environment
> variables (`SMELT_*`), keychain entries and daemon. It never reads or changes
> the state of CircleCI's own CLI, so the two can be installed side by side. To
> move a repository over, run `smelt init` in it and log in again; nothing is
> migrated.

## How it works

- **One run is one task.** `smelt factory build` runs in the local smelt daemon,
  so it survives closing the terminal. Watch it live with `smelt watch`.
- **Each agent gets its own container.** One implementer and one reviewer per
  review prompt in `.smelt/reviews/`, all started from your repo's sandbox image.
  Your working tree is copied in; the containers never talk to each other.
- **Each round:**
  0. Once, before round 1: the implementer writes a plan (files, approach, how
     each acceptance item is met, what it can't verify in the sandbox) and the
     requirements review checks it against your request.
  1. The implementer makes the change, following the plan, and ends its turn
     with the output of your gates. smelt checks it ran them after its last edit.
  2. Your validation commands (tests, lint, typecheck…) run first. If one fails,
     the reviews are skipped and the failure goes straight back to the
     implementer. If they pass, the reviewers check the change.
  3. High and medium findings, and failed checks, go back to the implementer.
  4. This repeats until everything passes or the attempts run out. In short,
     each round reads plan → implement → gates → reviews.
- **Your checkout is never touched.** The work happens in a git worktree on its
  own branch, `smelt/<description>/<run id>`. It starts from your files
  as they are, uncommitted changes included.
- **It stops at the PR.** smelt never pushes to your default branch, never
  force-pushes, and never merges.

## Requirements

- **macOS or Linux** (the local backend uses `rsync` and bind mounts)
- **Docker**: Docker Desktop or Docker Engine, running
- **Go 1.26+** and **[Task](https://taskfile.dev/)**, to build smelt
- **git** and **rsync**
- **Claude Code** (`claude` on your `PATH`) and a Claude subscription
- **GitHub CLI** (`gh`), logged in, if you want smelt to open PRs; without it,
  smelt pushes the branch and prints a compare URL

## Install

One command installs the latest release to `~/.local/bin` (no sudo):

```bash
curl -fsSL https://raw.githubusercontent.com/smeltdev/smelt/main/install.sh | bash
```

The script downloads the archive for your OS and architecture from this
repository's [Releases](https://github.com/smeltdev/smelt/releases),
checks it against the release's `checksums.txt`, and only then replaces
`smelt`. It prints the line to add to your shell's startup file if
`~/.local/bin` isn't on your `PATH`.

**Pin a version** (with or without the `v`), or choose another directory:

```bash
curl -fsSL https://raw.githubusercontent.com/smeltdev/smelt/main/install.sh | VERSION=v1.0.0 bash
curl -fsSL https://raw.githubusercontent.com/smeltdev/smelt/main/install.sh | DESTDIR=/usr/local/bin bash   # uses sudo if needed
```

**Verify provenance.** Release archives carry build provenance attestations:

```bash
gh attestation verify smelt_Darwin_arm64.tar.gz --repo smeltdev/smelt
```

Check the install:

```bash
smelt --version
```

**Build from source** (for contributors):

```bash
git clone git@github.com:smeltdev/smelt.git
cd smelt
task dev-install        # builds dist/smelt and links it to ~/.local/bin/smelt
```

`task dev-install` links `~/.local/bin/smelt` to your working tree's build, so
`install.sh` refuses to replace it unless you set `SMELT_FORCE=1`.

**Updating:**
- Release install: `smelt upgrade`. It downloads the latest release from this
  repository and checks it against `checksums.txt` before replacing the binary.
  `smelt` also tells you when a newer release exists; set
  `SMELT_NO_UPDATE_CHECK=1` to turn that off.
- Source build: `git pull && task build`. `smelt upgrade` refuses on a source
  build.
- Either way, stop the daemon afterwards so the next run starts it from the new
  binary. A running daemon keeps the code it started with:

```bash
pkill -f 'smelt _daemon'
```

## Set up a repository

Run `smelt init` in the repo you want the factory to work on. It walks you
through everything:

```bash
cd ~/code/my-app
smelt init
```

| Step | What it does |
|---|---|
| **Detect** | Finds your stack (Go, Node/TypeScript, Python, Rust, Java, Ruby and more) and your validation commands: test, lint, format, typecheck. It reads `.circleci/config.yml` when there is one. |
| **Claude login** | Mints a long-lived token from your Claude subscription (`claude setup-token`) and stores it in the OS keychain, never in a file. |
| **Factory backend** | Choose **docker (local containers)**. |
| **Sandbox image** | Writes `Dockerfile.smelt`, then offers to build and verify it (details below). |
| **Commit author** | Factory commits are attributed to your git identity, or to someone else you name. |

**What `Dockerfile.smelt` contains:**
- **Base image:** your CI image when there is one (e.g. `cimg/node:24.21.0`),
  otherwise one matched to the detected stack.
- **Dependencies:** your lockfiles' dependencies, baked into the package
  manager's store.
- **Agent kit:** git, the Claude Code CLI, the Codex CLI and a few coreutils,
  in a section marked `# --- smelt agent kit (vN) ---`.

**How the image is checked:** after the build, smelt verifies the image by
running the agent kit, your install step and your cheapest check inside it. If
the build or that check fails, it offers to let Claude repair
`Dockerfile.smelt`, up to 3 attempts.

**Commit what init wrote:**

```bash
git add .smelt/config.json Dockerfile.smelt
git commit -m "Set up smelt factory"
```

`Dockerfile.smelt` is yours to edit. The image tag is a hash of the Dockerfile
and the lockfiles it copies, so when either changes, the next
`smelt factory build` rebuilds the image automatically.

**When smelt updates its agent kit:** `Dockerfile.smelt` is written once and
never changed behind your back, so a Dockerfile from an older smelt keeps its
older kit. `smelt factory build` and `smelt factory image` compare the kit
version in the marker with the current one and, when it is older, warn (at most
once a day for `factory build`) and carry on. To update it:

```bash
smelt factory image --update-kit    # replace only the agent-kit section, show the diff, ask
```

It keeps everything above the marker as you wrote it, then builds and verifies
the image. Pass `--yes` to skip the question. `--regenerate` instead re-runs
detection and rewrites the whole file. A Dockerfile without the marker cannot be
updated this way.

### Add review prompts (recommended)

Each markdown file in `.smelt/reviews/` is one reviewer, and each reviewer gets
its own container. Write them like instructions to a senior reviewer:

```
.smelt/reviews/
├── correctness.md      # does it do what the spec asks, edge cases, error paths
├── testing.md          # are the new behaviours tested
└── maintainability.md  # duplication, abstractions, project conventions
```

Reviewers report findings as high, medium or low. High and medium findings go
back to the implementer; low findings are only shown. With no review prompts,
runs are gated on your validation commands alone. For examples, see this
repository's own [`.smelt/reviews/`](.smelt/reviews/).

## Build something

Write a spec, a markdown file describing the change, and hand it to the
factory:

```bash
smelt factory build --plan spec.md
```

You'll see each round as it happens: the implementer's summary, every check and
every finding. When it ends, the work is committed on its branch. In a
terminal, smelt then offers to push the branch and open a PR into the branch
you started from.

```bash
smelt factory build --plan spec.md --pr            # push and open the PR without asking
smelt factory build --plan spec.md --no-publish    # keep it local
smelt factory build "fix the flaky retry test"     # a short prompt instead of a file
smelt factory build --continue <run-id> "also handle the empty case"   # keep going on an earlier run
```

Useful flags:
- `--max-attempts N`: the most rounds to run.
- `--base BRANCH`, `--draft`, `--pr-title`, `--pr-body`: shape the PR.
- `--log`, `--verbose`: keep a full log of the run.
- `--harness claude|codex`: choose the coding agent (see [Coding agents](#coding-agents)).
- `--model`: choose the model (default: `factory.models.<harness>`, else the agent's own).

See [docs/CLI.md](docs/CLI.md) for every flag.

### Managing runs

By default one run is active per project. Set `factory.maxConcurrent` in `.smelt/config.json` to
build several tickets at once; each run is one implementer plus one reviewer container per review
prompt, so pass `--reviewers N` to cap the reviewers when running several.

```bash
smelt watch                          # live dashboard of runs, rounds and containers
smelt factory list                   # this repo's runs: state, result, age
smelt factory status [run-id]        # a run's state, rounds, result and cost (--json for tools)
smelt factory stop <run-id>          # stop a running run; its work so far is committed
smelt factory publish <run-id> --pr  # push a finished run's branch and open or update its PR
smelt factory update <run-id>        # merge the run's base branch into its branch; conflicts are committed with markers for --continue
smelt factory clean [run-id]         # remove finished runs' worktrees, branches and records
smelt factory image [--rebuild]      # build or rebuild and verify the sandbox image
smelt factory image --regenerate     # re-detect and rewrite Dockerfile.smelt (asks first)
smelt factory image --update-kit     # replace only the agent-kit section of Dockerfile.smelt (asks first)
smelt factory author ["Name <email>"]  # show or set who factory commits are attributed to
smelt refresh                        # log in to Claude again (and restart the daemon)
smelt refresh codex                  # log in to Codex again
smelt harness status [--json]        # which agents are installed and logged in, and their usage limits
```

## Coding agents

smelt drives two coding agents, each as the implementer and as the reviewers of
a run: **Claude Code** and **OpenAI Codex**. Choose one per run:

```bash
smelt factory build --plan spec.md --harness codex
smelt factory build --plan spec.md                 # uses factory.harness, or asks
```

Precedence, highest first: `--harness`, then `factory.harness` in
`.smelt/config.json`, then a question in a terminal (it offers to save your
answer). With none of these and no terminal, the command fails and lists the
options. There is no silent default. `smelt init` has a "Coding agent" step that
sets `factory.harness` and offers the login (`--harness` answers it).

`factory.models.claude` and `factory.models.codex` set the model each agent uses
when `--model` is not given, so switching agents never carries a model name the
other one rejects.

**Logging in to Codex:**

```bash
smelt auth login codex               # ChatGPT subscription: for your own machine
smelt auth login codex --device-auth # no browser; needs device-code sign-in enabled in ChatGPT → Settings → Security
smelt auth set codex                 # OpenAI API key: for a shared or unattended runner
smelt auth status                    # kind of login, plan, and whether it still works
```

`auth login codex` runs `codex login` in a private directory, so `~/.codex` is
untouched, and keeps the login in the OS keychain. A container never gets your
refresh token: its copy of `auth.json` has `refresh_token` blanked, and it lives
in a private directory of the container only. The access token lasts about ten
days. Under 24 hours before it expires, `factory build` first tries to renew it
on your machine; if that does not work it stops and asks you to run
`smelt refresh codex` (a one-minute browser login). `SMELT_CODEX_AUTH_JSON`
(the base64 of `auth.json`) or `CODEX_API_KEY` override the keychain.

**What a run reports:** `smelt factory status --json` shows, per implementer
turn, the `harness`, the `model`, `input_tokens`, `cached_tokens`,
`output_tokens` and the last `rate_limit` signal the agent emitted. Codex
reports tokens, not dollars, so `cost_usd` stays 0 for it.

**`smelt harness status`** shows both agents: installed on this machine and in
the sandbox image, whether the login works, the plan and the usage windows:
real figures for Codex (from its own `app-server`), and for Claude the
undocumented `/api/oauth/usage` endpoint, read with Claude Code's own login from
the macOS keychain (read-only). Anthropic has said third-party tools should not
call that endpoint and it can change without notice. Turn the probe off with
`{"harness": {"claude": {"usage_probe": false}}}` in `~/.config/smelt/config.json`;
without it, or when it fails, the last rate-limit signal a run saw is shown.
An image built from an older Dockerfile (agent kit v1) has no
`smelt.harnesses` label; smelt then runs the image once to see which agents it
has, and remembers the answer per image. `image_status` says `labeled`,
`unlabeled` or `no_image`.
smelt reports; choosing between agents is up to whatever drives it.

## Configuration

Factory settings live in the `factory` section of `.smelt/config.json`, which
is committed and shared with your team:

```json
{
  "factory": {
    "backend": "docker",
    "dockerfile": "Dockerfile.smelt",
    "maxAttempts": 3,
    "log": true
  }
}
```

| Key | Meaning |
|---|---|
| `backend` | `docker` for local containers (the default), or `circleci` for cloud sidecars. `circleci` is the one setting that lets smelt use CircleCI. |
| `dockerfile` | The managed sandbox Dockerfile. smelt builds the image and keeps it up to date. |
| `image` | An image you manage yourself. It takes precedence over `dockerfile` and is never built by smelt. |
| `harness` | `claude` or `codex`: the coding agent runs use. Unset means ask (and fail without a terminal). |
| `models` | Per-agent default model, e.g. `{"claude": "opus", "codex": "gpt-5-codex"}`, used when `--model` is not given |
| `maxAttempts` | Default number of rounds (built-in: 3) |
| `plan` | `true` (the default) has the implementer write a plan, reviewed before it implements; `false` skips it, like `--no-plan` |
| `gatesFirst` | `true` (the default) runs the validation commands before the reviews and skips the reviews of a round whose commands failed; `false` runs them side by side |
| `log` | Keep a run log by default |
| `author` | `"Name <email>"` for factory commits. Leave it unset so everyone commits as their own git identity. |

A flag always beats the config, and the config beats the built-in default.

**Credentials:**
- **Claude:** in the OS keychain, set by `smelt init` or `smelt refresh`.
  `CLAUDE_CODE_OAUTH_TOKEN` overrides it.
- **Codex:** in the OS keychain, set by `smelt auth login codex` (ChatGPT) or
  `smelt auth set codex` (API key). A run needs only the credential of the agent
  it uses. See [Coding agents](#coding-agents).
- **GitHub:** whatever `gh` is logged in as.
- **CircleCI:** not needed. Even if a CircleCI token is on your machine, smelt
  doesn't use it unless the project opts into the `circleci` backend.

## Troubleshooting

| Symptom | Fix |
|---|---|
| A run behaves like an older version after you upgraded or rebuilt smelt | The daemon is still running the old binary: `pkill -f 'smelt _daemon'` |
| `Building … failed` or the image fails its checks | `smelt factory image`. Read the error, edit `Dockerfile.smelt` or accept the repair offer. The full build log is linked in the error. |
| `No Docker image is set for factory runs` | Run `smelt init` and choose docker, or set `factory.dockerfile` or `factory.image` |
| The first Docker call after a while is slow and prints "Waiting for Docker to finish starting" | Docker Desktop's Resource Saver stopped the engine. smelt waits up to 3 minutes for it; turn Resource Saver off in Docker Desktop to avoid the wait. |
| `Image smelt-… is missing` | smelt's own images are never pulled. Run `smelt factory image` (or the `docker build` command in the message). |
| A branch can't be deleted because it's checked out in a worktree | `smelt factory clean <run-id>` |
| `smelt validate --remote` says it needs the circleci backend | Remote validation is a CircleCI feature. Opt in with `"factory": {"backend": "circleci"}` in `.smelt/config.json` (see below); otherwise `smelt validate` runs your checks locally. |

## CircleCI cloud sidecars (optional)

The original smelt runs the same factory, and `smelt validate`, on CircleCI
microVM sidecars instead of local Docker. Everything below needs a CircleCI
account (free plans included).

smelt only contacts CircleCI when you choose it: with
`"factory": {"backend": "circleci"}` in `.smelt/config.json` (`smelt init
--factory-backend circleci` or `smelt sidecar setup` writes it), with
`--backend circleci`, or by running a CircleCI command (`smelt auth`,
`smelt sidecar`, `smelt task`, `smelt org`, `smelt prune`). Without that,
`smelt validate`, the git pre-commit hook and the agent hooks run your checks
locally and ignore `remote: true`.

```bash
smelt auth login                              # or: smelt auth signup / smelt auth set circleci
smelt factory build --backend circleci --plan spec.md

smelt sidecar create --name my-sidecar        # create a sidecar; it becomes the active one
smelt sidecar sync && smelt validate --remote # sync your tree and validate remotely
smelt sidecar ssh [-- make test]              # shell in, or run one command
smelt sidecar use|current|forget              # manage the active sidecar
smelt sidecar setup --name my-sidecar         # detect env, install deps, snapshot
smelt sidecar snapshot create|get|list        # reusable environments
```

`smelt skill install` adds agent skills (`/smelt-sidecar`) that let Claude Code,
Codex or Cursor drive sidecars themselves. See
[docs/GETTING_STARTED.md](docs/GETTING_STARTED.md) for the full cloud walkthrough.

## Other tools

- **`smelt validate [name]`** runs your configured checks. `smelt init` also
  installs Claude Code hooks and a git pre-commit hook that run them
  ([docs/HOOKS.md](docs/HOOKS.md)).
- **`smelt build-prompt`** mines your team's PR review comments and generates a
  review-context prompt in `.smelt/context/`. It needs `ANTHROPIC_API_KEY` and
  `GITHUB_TOKEN`.
- **`smelt task config|run`** triggers AI coding tasks on CircleCI (needs a CircleCI account).
- **`smelt mutate`** finds test coverage gaps with mutation testing.
- **`smelt conflicts`** reports whether your branch still merges cleanly.

## All commands

```
Local factory
  smelt init                                 Set up a repo: detect, log in, sandbox image, author
  smelt factory build [--plan FILE]          Implement a spec, review and validate it, publish a PR
  smelt factory list|clean                   List and remove runs
  smelt factory image                        Build and verify the sandbox image
  smelt factory author                       Show or set the commit author
  smelt watch                                Live dashboard
  smelt refresh [claude|codex]               Log in to an agent again (restarts the daemon for Claude)
  smelt harness status                       Installed agents, logins and usage limits

Checks and hooks
  smelt validate [name]                      Run quality checks
  smelt hook disable|enable|status           Manage hook execution
  smelt config show|set                      Show or change configuration

CircleCI cloud (optional)
  smelt auth login|signup|set|status|remove  Authentication (login/set/remove also take codex)
  smelt org create                           Manage CircleCI organizations
  smelt sidecar …                            Cloud sidecar environments and snapshots
  smelt task config|run                      Trigger CI tasks
  smelt prune                                Delete your sidecars

Other
  smelt build-prompt                         Review context from PR comments
  smelt mutate                               Mutation testing
  smelt conflicts                            Merge-conflict check
  smelt skill install|list                   Agent skills
  smelt completion install|uninstall         Shell completions
```

See [docs/CLI.md](docs/CLI.md) for the full command and flag reference.

## Documentation

| Doc | Purpose |
|-----|---------|
| [docs/CLI.md](docs/CLI.md) | Full command and flag reference |
| [docs/GETTING_STARTED.md](docs/GETTING_STARTED.md) | Guide covering auth, init, sidecars, the dev loop and skills |
| [docs/HOOKS.md](docs/HOOKS.md) | Pre-commit and Stop hook configuration |
| [docs/SKILLS.md](docs/SKILLS.md) | Agent skills reference |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Module layout, dependency rules, environment variables |

## Development

```bash
task build              # build dist/smelt
task test               # run tests (with -race)
task lint               # golangci-lint
task acceptance-test    # acceptance tests
task dev-install        # build and link into ~/.local/bin
```

**Releasing** (maintainers): `task release VERSION=1.0.0` tags `v1.0.0` and
pushes the tag; the Release workflow (`.github/workflows/release.yml`) runs
goreleaser, publishes the archives, `checksums.txt` and `install.sh`, and
attests their provenance. This fork's versions start at `v1.0.0`; upstream's
`v0.x` tags are ignored by `task tag`. Setting the optional
`SMELT_TELEMETRY_WRITE_KEY` Actions secret bakes a Segment write key into
release builds; without it, telemetry never sends.

Contributions follow the code standards in [AGENTS.md](AGENTS.md). smelt's own
factory runs review against them with the `maintainability` reviewer.

Acceptance tests that clone repositories are skipped by default. Set
`SMELT_ENV_BUILDER_ACCEPTANCE=1` to enable them, and `SMELT_SIDECAR_CACHE_DIR`
to reuse clones between runs.
