# chunk

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

## How it works

- **One run is one task.** `chunk factory build` runs in the local chunk daemon,
  so it survives closing the terminal. Watch it live with `chunk watch`.
- **Each agent gets its own container.** One implementer and one reviewer per
  review prompt in `.chunk/reviews/`, all started from your repo's sandbox image.
  Your working tree is copied in; the containers never talk to each other.
- **Each round:**
  1. The implementer makes the change.
  2. The reviewers and your validation commands (tests, lint, typecheck…) check
     it in parallel.
  3. High and medium findings, and failed checks, go back to the implementer.
  4. This repeats until everything passes or the attempts run out.
- **Your checkout is never touched.** The work happens in a git worktree on its
  own branch, `chunk/factory/<description>/<run id>`. It starts from your files
  as they are, uncommitted changes included.
- **It stops at the PR.** chunk never pushes to your default branch, never
  force-pushes, and never merges.

## Requirements

- **macOS or Linux** (the local backend uses `rsync` and bind mounts)
- **Docker**: Docker Desktop or Docker Engine, running
- **Go 1.26+** and **[Task](https://taskfile.dev/)**, to build chunk
- **git** and **rsync**
- **Claude Code** (`claude` on your `PATH`) and a Claude subscription
- **GitHub CLI** (`gh`), logged in, if you want chunk to open PRs; without it,
  chunk pushes the branch and prints a compare URL

## Install

One command installs the latest release to `~/.local/bin` (no sudo):

```bash
curl -fsSL https://raw.githubusercontent.com/web3technologies/chunk-cli/main/install.sh | bash
```

The script downloads the archive for your OS and architecture from this
repository's [Releases](https://github.com/web3technologies/chunk-cli/releases),
checks it against the release's `checksums.txt`, and only then replaces
`chunk`. It prints the line to add to your shell's startup file if
`~/.local/bin` isn't on your `PATH`.

**Pin a version** (with or without the `v`), or choose another directory:

```bash
curl -fsSL https://raw.githubusercontent.com/web3technologies/chunk-cli/main/install.sh | VERSION=v1.0.0 bash
curl -fsSL https://raw.githubusercontent.com/web3technologies/chunk-cli/main/install.sh | DESTDIR=/usr/local/bin bash   # uses sudo if needed
```

**Verify provenance.** Release archives carry build provenance attestations:

```bash
gh attestation verify chunk_Darwin_arm64.tar.gz --repo web3technologies/chunk-cli
```

Check the install:

```bash
chunk --version
```

**Build from source** (for contributors):

```bash
git clone git@github.com:web3technologies/chunk-cli.git
cd chunk-cli
task dev-install        # builds dist/chunk and links it to ~/.local/bin/chunk
```

`task dev-install` links `~/.local/bin/chunk` to your working tree's build, so
`install.sh` refuses to replace it unless you set `CHUNK_FORCE=1`.

**Updating:**
- Release install: `chunk upgrade`. It downloads the latest release from this
  repository and checks it against `checksums.txt` before replacing the binary.
  `chunk` also tells you when a newer release exists; set
  `CHUNK_NO_UPDATE_CHECK=1` to turn that off.
- Source build: `git pull && task build`. `chunk upgrade` refuses on a source
  build.
- Either way, stop the daemon afterwards so the next run starts it from the new
  binary. A running daemon keeps the code it started with:

```bash
pkill -f 'chunk _daemon'
```

## Set up a repository

Run `chunk init` in the repo you want the factory to work on. It walks you
through everything:

```bash
cd ~/code/my-app
chunk init
```

| Step | What it does |
|---|---|
| **Detect** | Finds your stack (Go, Node/TypeScript, Python, Rust, Java, Ruby and more) and your validation commands: test, lint, format, typecheck. It reads `.circleci/config.yml` when there is one. |
| **Claude login** | Mints a long-lived token from your Claude subscription (`claude setup-token`) and stores it in the OS keychain, never in a file. |
| **Factory backend** | Choose **docker (local containers)**. |
| **Sandbox image** | Writes `Dockerfile.chunk`, then offers to build and verify it (details below). |
| **Commit author** | Factory commits are attributed to your git identity, or to someone else you name. |

**What `Dockerfile.chunk` contains:**
- **Base image:** your CI image when there is one (e.g. `cimg/node:24.21.0`),
  otherwise one matched to the detected stack.
- **Dependencies:** your lockfiles' dependencies, baked into the package
  manager's store.
- **Agent kit:** git, the Claude Code CLI and a few coreutils.

**How the image is checked:** after the build, chunk verifies the image by
running the agent kit, your install step and your cheapest check inside it. If
the build or that check fails, it offers to let Claude repair
`Dockerfile.chunk`, up to 3 attempts.

**Commit what init wrote:**

```bash
git add .chunk/config.json Dockerfile.chunk
git commit -m "Set up chunk factory"
```

`Dockerfile.chunk` is yours to edit. The image tag is a hash of the Dockerfile
and the lockfiles it copies, so when either changes, the next
`chunk factory build` rebuilds the image automatically.

### Add review prompts (recommended)

Each markdown file in `.chunk/reviews/` is one reviewer, and each reviewer gets
its own container. Write them like instructions to a senior reviewer:

```
.chunk/reviews/
├── correctness.md      # does it do what the spec asks, edge cases, error paths
├── testing.md          # are the new behaviours tested
└── maintainability.md  # duplication, abstractions, project conventions
```

Reviewers report findings as high, medium or low. High and medium findings go
back to the implementer; low findings are only shown. With no review prompts,
runs are gated on your validation commands alone. For examples, see this
repository's own [`.chunk/reviews/`](.chunk/reviews/).

## Build something

Write a spec, a markdown file describing the change, and hand it to the
factory:

```bash
chunk factory build --plan spec.md
```

You'll see each round as it happens: the implementer's summary, every check and
every finding. When it ends, the work is committed on its branch. In a
terminal, chunk then offers to push the branch and open a PR into the branch
you started from.

```bash
chunk factory build --plan spec.md --pr            # push and open the PR without asking
chunk factory build --plan spec.md --no-publish    # keep it local
chunk factory build "fix the flaky retry test"     # a short prompt instead of a file
chunk factory build --continue <run-id> "also handle the empty case"   # keep going on an earlier run
```

Useful flags:
- `--max-attempts N`: the most rounds to run.
- `--base BRANCH`, `--draft`, `--pr-title`, `--pr-body`: shape the PR.
- `--log`, `--verbose`: keep a full log of the run.
- `--model`: choose the Claude model.

See [docs/CLI.md](docs/CLI.md) for every flag.

### Managing runs

```bash
chunk watch                          # live dashboard of runs, rounds and containers
chunk factory list                   # this repo's runs: state, result, age
chunk factory clean [run-id]         # remove finished runs' worktrees, branches and records
chunk factory image [--rebuild]      # build or rebuild and verify the sandbox image
chunk factory image --regenerate     # re-detect and rewrite Dockerfile.chunk (asks first)
chunk factory author ["Name <email>"]  # show or set who factory commits are attributed to
chunk refresh                        # log in to Claude again (and restart the daemon)
```

## Configuration

Factory settings live in the `factory` section of `.chunk/config.json`, which
is committed and shared with your team:

```json
{
  "factory": {
    "backend": "docker",
    "dockerfile": "Dockerfile.chunk",
    "maxAttempts": 3,
    "log": true
  }
}
```

| Key | Meaning |
|---|---|
| `backend` | `docker` for local containers (the default), or `circleci` for cloud sidecars. `circleci` is the one setting that lets chunk use CircleCI. |
| `dockerfile` | The managed sandbox Dockerfile. chunk builds the image and keeps it up to date. |
| `image` | An image you manage yourself. It takes precedence over `dockerfile` and is never built by chunk. |
| `maxAttempts` | Default number of rounds (built-in: 3) |
| `log` | Keep a run log by default |
| `author` | `"Name <email>"` for factory commits. Leave it unset so everyone commits as their own git identity. |

A flag always beats the config, and the config beats the built-in default.

**Credentials:**
- **Claude:** in the OS keychain, set by `chunk init` or `chunk refresh`.
  `CLAUDE_CODE_OAUTH_TOKEN` overrides it.
- **GitHub:** whatever `gh` is logged in as.
- **CircleCI:** not needed. Even if a CircleCI token is on your machine, chunk
  doesn't use it unless the project opts into the `circleci` backend.

## Troubleshooting

| Symptom | Fix |
|---|---|
| A run behaves like an older version after you upgraded or rebuilt chunk | The daemon is still running the old binary: `pkill -f 'chunk _daemon'` |
| `Building … failed` or the image fails its checks | `chunk factory image`. Read the error, edit `Dockerfile.chunk` or accept the repair offer. The full build log is linked in the error. |
| `No Docker image is set for factory runs` | Run `chunk init` and choose docker, or set `factory.dockerfile` or `factory.image` |
| The first Docker call after a while says "no such image" | Docker Desktop's Resource Saver was waking the engine. chunk retries; if it persists, open Docker Desktop. |
| A branch can't be deleted because it's checked out in a worktree | `chunk factory clean <run-id>` |
| `chunk validate --remote` says it needs the circleci backend | Remote validation is a CircleCI feature. Opt in with `"factory": {"backend": "circleci"}` in `.chunk/config.json` (see below); otherwise `chunk validate` runs your checks locally. |

## CircleCI cloud sidecars (optional)

The original chunk runs the same factory, and `chunk validate`, on CircleCI
microVM sidecars instead of local Docker. Everything below needs a CircleCI
account (free plans included).

chunk only contacts CircleCI when you choose it: with
`"factory": {"backend": "circleci"}` in `.chunk/config.json` (`chunk init
--factory-backend circleci` or `chunk sidecar setup` writes it), with
`--backend circleci`, or by running a CircleCI command (`chunk auth`,
`chunk sidecar`, `chunk task`, `chunk org`, `chunk prune`). Without that,
`chunk validate`, the git pre-commit hook and the agent hooks run your checks
locally and ignore `remote: true`.

```bash
chunk auth login                              # or: chunk auth signup / chunk auth set circleci
chunk factory build --backend circleci --plan spec.md

chunk sidecar create --name my-sidecar        # create a sidecar; it becomes the active one
chunk sidecar sync && chunk validate --remote # sync your tree and validate remotely
chunk sidecar ssh [-- make test]              # shell in, or run one command
chunk sidecar use|current|forget              # manage the active sidecar
chunk sidecar setup --name my-sidecar         # detect env, install deps, snapshot
chunk sidecar snapshot create|get|list        # reusable environments
```

`chunk skill install` adds agent skills (`/chunk-sidecar`) that let Claude Code,
Codex or Cursor drive sidecars themselves. See
[docs/GETTING_STARTED.md](docs/GETTING_STARTED.md) for the full cloud walkthrough.

## Other tools

- **`chunk validate [name]`** runs your configured checks. `chunk init` also
  installs Claude Code hooks and a git pre-commit hook that run them
  ([docs/HOOKS.md](docs/HOOKS.md)).
- **`chunk build-prompt`** mines your team's PR review comments and generates a
  review-context prompt in `.chunk/context/`. It needs `ANTHROPIC_API_KEY` and
  `GITHUB_TOKEN`.
- **`chunk task config|run`** triggers AI coding tasks on CircleCI (needs a CircleCI account).
- **`chunk mutate`** finds test coverage gaps with mutation testing.
- **`chunk conflicts`** reports whether your branch still merges cleanly.

## All commands

```
Local factory
  chunk init                                 Set up a repo: detect, log in, sandbox image, author
  chunk factory build [--plan FILE]          Implement a spec, review and validate it, publish a PR
  chunk factory list|clean                   List and remove runs
  chunk factory image                        Build and verify the sandbox image
  chunk factory author                       Show or set the commit author
  chunk watch                                Live dashboard
  chunk refresh [claude]                     Log in to Claude again and restart the daemon

Checks and hooks
  chunk validate [name]                      Run quality checks
  chunk hook disable|enable|status           Manage hook execution
  chunk config show|set                      Show or change configuration

CircleCI cloud (optional)
  chunk auth login|signup|set|status|remove  Authentication
  chunk org create                           Manage CircleCI organizations
  chunk sidecar …                            Cloud sidecar environments and snapshots
  chunk task config|run                      Trigger CI tasks
  chunk prune                                Delete your sidecars

Other
  chunk build-prompt                         Review context from PR comments
  chunk mutate                               Mutation testing
  chunk conflicts                            Merge-conflict check
  chunk skill install|list                   Agent skills
  chunk completion install|uninstall         Shell completions
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
task build              # build dist/chunk
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
`CHUNK_TELEMETRY_WRITE_KEY` Actions secret bakes a Segment write key into
release builds; without it, telemetry never sends.

Contributions follow the code standards in [AGENTS.md](AGENTS.md). chunk's own
factory runs review against them with the `maintainability` reviewer.

Acceptance tests that clone repositories are skipped by default. Set
`CHUNK_ENV_BUILDER_ACCEPTANCE=1` to enable them, and `CHUNK_SIDECAR_CACHE_DIR`
to reuse clones between runs.
