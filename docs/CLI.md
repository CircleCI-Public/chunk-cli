# CLI Command Tree

Complete command reference for the `chunk` CLI.

## Command Tree

```
chunk
├── auth
│   ├── login                       # Log in to CircleCI via browser (recommended)
│   │   --no-browser                # Print the login URL instead of opening a browser
│   ├── signup                      # Sign up for a new CircleCI account via browser
│   │   --no-browser                # Print the signup URL instead of opening a browser
│   ├── set <provider>               # Store credential (circleci | anthropic | anthropic-oauth | github)
│   ├── refresh [provider]          # Re-mint the Claude subscription token (claude | anthropic-oauth; default claude) and restart a running daemon
│   ├── status                      # Check authentication status (CircleCI, Anthropic, GitHub)
│   └── remove <provider>           # Remove stored credential (circleci | anthropic | anthropic-oauth | github)
│
├── refresh [provider]              # Same as `auth refresh`
│
├── org                             # Manage CircleCI organizations
│   └── create <name>               # Create a new standalone CircleCI organization
│
├── build-prompt                    # Mine PR comments → analyze → generate prompt
│   --org <org>                     # GitHub org (auto-detected from git remote)
│   --repos <items>                 # Comma-separated repo names
│   --top <n>                       # Top reviewers to include (default: 5)
│   --since <YYYY-MM-DD>            # Start date (default: 3 months ago)
│   --output <path>                 # Output path (default: .chunk/context/review-prompt.md)
│   --max-comments <n>              # Max comments per reviewer (0 = no limit)
│   --analyze-model <model>         # Model for analysis step
│   --prompt-model <model>          # Model for prompt generation step
│   --include-attribution           # Include reviewer attribution
│   --debug                  # Write intermediate files (details JSON, analysis, PR rankings CSV) for debugging
│
├── config
│   ├── show                        # Display resolved configuration
│   │   --json                      # Output as JSON
│   └── set <key> <value>           # Set a config value (see Config keys below)
│
├── init                            # Initialize project configuration
│   --force                         # Overwrite existing config
│   --skip-hooks                    # Skip hook file generation
│   --skip-validate                 # Skip validate command detection
│   --skip-completions              # Skip shell completion installation
│   --skip-skills                   # Skip agent skill installation
│   --skip-auth                     # Skip the Claude login check
│   --skip-factory                  # Skip the factory backend step
│   --factory-backend <name>        # docker (default) or circleci; skips the prompt. Asked first: the circleci path alone detects the org ID, installs the chunk-sidecar skills and offers test-suites.yml
│   --factory-image <image>         # Docker image for factory runs (user-managed, never auto-built); skips the prompt
│   --skip-image-build              # Write Dockerfile.chunk for docker runs but don't offer to build the image
│   --factory-author "Name <email>" # Who factory commits are attributed to (default: your git identity)
│   --skip-test-suites              # Skip .circleci/test-suites.yml scaffolding (circleci backend only; default: true; pass =false to generate)
│   --project-dir <path>            # Project directory (defaults to cwd)
│
├── task
│   ├── run                         # Trigger a task run
│   │   --definition <name|uuid>    # Definition name or UUID (required)
│   │   --prompt <text>             # Prompt text (required)
│   │   --branch <branch>           # Branch override
│   │   --new-branch                # Create a new branch
│   │   --no-pipeline-as-tool       # Disable pipeline-as-tool mode
│   │   --json                      # Output as JSON
│   └── config                      # Set up .chunk/run.json for this repository
│       --force                     # Overwrite existing configuration without confirmation
│
├── skill
│   ├── install                     # Install all skills
│   └── list                        # List skills and install status
│
├── mutate [path]                   # Find test coverage gaps via mutation testing
│   --parallel <n>                  # Sidecar pool capacity (0: enumerate only)
│   --max <n>                       # Limit the number of mutations (0: no limit)
│   --output <table|json>           # Enumeration output format
│   --test-cmd <command>            # Test command override
│   --test-timeout <duration>       # Per-mutation test timeout
│   --org-id <id>                   # CircleCI organization ID
│   --destroy-pool                  # Delete pool sidecars after the run
│
├── validate                        # Run validation commands
│   [name]                          # Optional: run a specific named command
│   --dry-run                       # Print commands without executing
│   --list                          # List all configured commands
│   --json                          # Output as JSON (only applies with --list)
│   --cmd <command>                 # Run an inline command
│   --save                          # Save --cmd to config
│   --remote                        # Run using the active sidecar pool
│   --parallel <n>                  # Max sidecars for remote commands (0: auto, up to 3)
│   --mark-remote                   # Mark [name] (or all commands) remote in config, then exit
│   --sidecar-id <id>               # Remote execution in specific sidecar
│   --org-id <id>                   # Organization ID (used when creating a new sidecar)
│   --workdir <path>                # Working directory on sidecar
│   --project <path>                # Override project directory
│   -e / --env KEY=VALUE            # Set env var in remote sidecar session (repeatable)
│   --env-file <path>               # Env file to load (default: .env.local; pass a path to override)
│   --async                         # Run in the background via the chunk daemon; results are read later
│   │
│   ├── results                     # Print results of finished background runs, then forget them
│   │   --project <path>            # Override project directory
│   │
│   └── variants <variants-file>    # Run code variants on a temporary sidecar pool
│       --name <command>            # Validate command to run (default: all remote commands)
│       --parallel <n>              # Maximum pool capacity (default 5)
│       --timeout <seconds>         # Per-command timeout when the command sets none (0 for no limit)
│       --org-id <id>               # Organization ID
│       --image <id>                # Snapshot ID, or Docker image with --backend docker (default: validation.sidecarImage, or factory.image for docker)
│       --workdir <path>            # Remote working directory
│
├── sidecar
│   ├── list                        # List sidecars
│   │   --org-id <id>               # Organization ID
│   │   --all                       # List all sidecars in the org (requires org admin)
│   │   --json                      # Output as JSON
│   ├── create                      # Create a sidecar
│   │   --org-id <id>               # Organization ID (see Org ID resolution)
│   │   --name <name>               # Sidecar name (auto-generated if omitted)
│   │   --image <image>             # E2B template ID or container image
│   ├── use <id>                    # Set the active sidecar for this project
│   ├── current                     # Show the active sidecar
│   │   --json                      # Output as JSON
│   ├── forget                      # Clear the active sidecar
│   ├── exec                        # Execute command in sidecar
│   │   --sidecar-id <id>           # Sidecar ID (defaults to active sidecar)
│   │   --command <cmd>             # Command to run (required)
│   │   --args <args>               # Command arguments
│   ├── logs <command-id>           # Print output of a command that ran on a sidecar
│   │   -f / --follow               # Keep printing until the command exits
│   ├── add-ssh-key                 # Add SSH key to sidecar
│   │   --sidecar-id <id>           # Sidecar ID (defaults to active sidecar)
│   │   --public-key <key>          # SSH public key string
│   │   --public-key-file <path>    # Path to public key file
│   ├── ssh                         # SSH into sidecar (stdin forwarded when piped)
│   │   --sidecar-id <id>           # Sidecar ID (defaults to active sidecar)
│   │   -e / --env KEY=VALUE        # Set env var in remote session (repeatable)
│   │   --env-file <path>           # Env file to load (default: .env.local; pass a path to override)
│   ├── sync                        # Sync files to sidecar
│   │   --sidecar-id <id>           # Sidecar ID (defaults to active sidecar)
│   │   --workdir <path>            # Destination path on sidecar (auto-detected when omitted)
│   │   --checkout                  # Sync via git checkout/patch instead of bundle (requires branch pushed to GitHub)
│   ├── env                         # Detect tech stack and print environment spec as JSON
│   │   --dir <path>                # Directory to analyse (default: .)
│   │   --no-save                   # Print only, do not save to .chunk/config.json
│   ├── build                       # Generate Dockerfile and build test image from env spec
│   │   --dir <path>                # Directory to write Dockerfile.test and build from
│   │   --tag <tag>                 # Image tag (e.g. myapp:latest)
│   ├── setup                       # Detect env, sync files, and run install steps
│   │   --dir <path>                # Directory to detect environment in (default: .)
│   │   --sidecar-id <id>           # Sidecar ID (defaults to active sidecar)
│   │   --org-id <id>               # Organization ID (used when creating a new sidecar)
│   │   --name <name>               # Sidecar name (used when creating a new sidecar)
│   │   --skip-sync                 # Skip syncing files to the sidecar
│   │   --force                     # Re-detect environment even if cached
│   │   -e / --env KEY=VALUE        # Set env var in remote sidecar session (repeatable)
│   │   --env-file <path>           # Env file to load (default: .env.local; pass a path to override)
│   └── snapshot
│       ├── create                  # Snapshot a sidecar, then delete the source sidecar
│       │   --sidecar-id <id>       # Sidecar ID (defaults to active sidecar)
│       │   --name <name>           # Snapshot name (required)
│       ├── get <snapshot-id>       # Get a snapshot by ID
│       │   --json                  # Output as JSON
│       └── list                    # List snapshots
│           --org-id <id>           # Organization ID
│           --json                  # Output as JSON
│
├── factory                         # Build and manage factory runs
│   ├── build [prompt|-]            # Implement a prompt on a sidecar, then review and validate it until it passes
│       --plan <file>               # Read the prompt from a file (usually a markdown spec)
│       --backend <name>            # docker or circleci (default: factory.backend, else docker)
│       --max-attempts <n>          # Maximum rounds of review and validation (default: factory.maxAttempts, else 3)
│       --continue <run id|branch>  # Pick up an earlier run; its ID is the branch's final segment
│       --reviewers <n>             # Reviewer sidecars (0: one per review prompt)
│       --reviews <dir>             # Directory of review prompts (default: .chunk/reviews)
│       --no-validate               # Skip the project's validation commands
│       --keep-sidecars             # Leave the sidecars running when the run ends
│       --push                     # Push the run's branch when it ends, without asking
│       --pr                       # Push and open a PR (or update the branch's PR), without asking
│       --no-publish               # Never offer to push or open a PR
│       --base <branch>            # Branch the PR targets (default: branch you started from, else the default)
│       --pr-title <t>             # PR title (default: the prompt's first line)
│       --pr-body <b>              # PR description (default: prompt and run outcome)
│       --draft                    # Open the PR as a draft
│       --org-id <id>               # Organization ID
│       --image <id>                # Snapshot ID, or Docker image with --backend docker (default: validation.sidecarImage, or factory.image for docker)
│       --model <name>              # Claude model (default: Claude Code's default)
│       --implement-timeout <duration> # Max time for each implementer turn
│       --review-timeout <duration> # Max time for each review
│       --log                       # Keep a log of the run's full context in ~/.chunk/factory/run-<start time>.log
│       --log-file <path>           # Keep the log in this file instead (implies --log)
│       --verbose                   # Log review prompts, passing output and reviewer checks, and show the log here (implies --log)
│       --json                      # Output as JSON
│   ├── list                        # List the project's factory runs and their worktrees
│   │   --json                      # Output as JSON
│   ├── clean [run...]              # Remove runs' worktrees, branches and records (default: every run not running)
│   │   --yes                       # Remove without asking
│   │   --force                     # Also remove worktrees with uncommitted changes, discarding them
│   │   --dry-run                   # List what would be removed and stop
│   ├── author ["Name <email>"]     # Show or set who factory commits are attributed to (factory.author)
│   │   --unset                     # Remove factory.author, so each person commits as themselves
│   └── image                       # Build the Docker image factory runs execute in (factory.dockerfile)
│       --rebuild                   # Build even when the image exists, without the layer cache
│       --regenerate                # Render Dockerfile.chunk again, show the diff, confirm before writing
│
├── watch [dir...]                  # Live TUI dashboard for active pools and recent activity
│   --focus                         # Watch only the current directory instead of all known projects
│
├── conflicts                       # Report whether this branch still merges cleanly into its merge target
│   --project <path>                # Override project directory
│   --hook                          # Emit Claude Code hook JSON so the notice reaches the agent
│   --json                          # Print the raw conflict report as JSON
│
├── hook                            # Manage chunk hook execution
│   --project <path>                # Override project directory
│   ├── disable                     # Disable chunk validate hooks
│   ├── enable                      # Re-enable chunk validate hooks
│   └── status                      # Show whether hooks are enabled or disabled
│
├── completion
│   ├── install                     # Install zsh completion
│   └── uninstall                   # Remove zsh completion
│
└── upgrade                         # Update a release install to the latest release (see Behavior Decisions)
```

## Behavior Decisions

- `auth login` and `auth signup` both use OAuth and store the resulting token in the system keychain (or `~/.config/chunk/config.json` with `--insecure-storage`). They differ only in which page the browser opens: login for existing accounts, signup for new ones. Use `--no-browser` to print the URL instead of opening it automatically.
- `auth signup` fails with a user-friendly error if a CircleCI token is already stored; run `chunk auth remove circleci` first to clear it. Existing accounts should use `chunk auth login`.
- After a successful `auth signup`, chunk checks whether the new account belongs to any organization. If it belongs to none, chunk prompts for a name and creates a standalone org, so the first sidecar command does not fail on having none. Without a terminal it prints a warning pointing at `chunk org create <name>` instead; the signup itself still succeeds.
- Commands that resolve an org (sidecar, validate, init, mutate, prune) offer the same org creation prompt when the account has no organizations. Without a terminal, or with `CI` set, sidecar, validate, mutate and prune fail with a suggestion to run `chunk org create <name>` or pass `--org-id`; `init` prints a warning and finishes without writing an org ID.
- `init` ends with a Claude credential step: it reports the credential in use, or (interactively) offers to run `claude setup-token` and store the token in the keychain. Without a terminal, with `CI` set, or with `--insecure-storage` it only prints a hint. It also runs when `.chunk/config.json` already exists. A failed or declined login is never fatal. The token is never written to `.chunk/config.json`.
- `auth refresh` / `refresh` always replace the stored token without a confirmation, warn when `CLAUDE_CODE_OAUTH_TOKEN` or an API key overrides it, and restart a running chunk daemon. `auth set anthropic-oauth` and `auth remove anthropic-oauth` restart it too.
- `org create` is hidden from the default help output. It requires CircleCI authentication and creates a standalone org (not tied to a VCS provider), printing the org name, ID, and slug on success.
- `build-prompt` auto-detects org and repos from the git remote when flags
  are omitted. If `--org` is provided explicitly, `--repos` is required.
- `build-prompt --output` creates parent directories automatically.
- `build-prompt --since` defaults to 3 months before the current date.
- `build-prompt` does not write intermediate files by default. Pass `--debug` to write the raw details JSON, analysis markdown, and PR rankings CSV alongside the prompt — useful when diagnosing unexpected prompt output.
- `task run` defaults to pipeline-as-tool mode; use `--no-pipeline-as-tool`
  to disable.
- `config set` user keys: `model`, `telemetry`, `notifications`. Project keys (`.chunk/config.json`):
  `orgID`, `validation.sidecarImage`. Credentials use `chunk auth set`, not `config set`.
- **CircleCI is opt-in, and a CircleCI token is not consent.** chunk contacts
  CircleCI only for a project with `"factory": {"backend": "circleci"}` in
  `.chunk/config.json` (or a request that names `--backend circleci`), and for
  the explicitly CircleCI commands: `auth`, `sidecar`, `task`, `org`, `prune`,
  `build-prompt`'s CircleCI parts, `review`, `mutate`. An empty backend means
  `docker`. Having `CIRCLE_TOKEN`, a keychain entry or an `orgID` in the config
  never causes traffic by itself.
- Without the circleci backend, every validation command runs in the local
  working tree, `remote: true` is ignored, and `chunk validate`, the git
  pre-commit hook, the agent hooks and the daemon's background validation never
  create, sync to or exec on a CircleCI sidecar. A dim note ("running lint
  locally; remote validation needs the circleci backend") is printed once per
  run for commands marked remote. `validate --remote`, `validate variants` and
  `--mark-remote` are the exceptions: `--remote` and `variants` fail with an
  error explaining the opt-in rather than silently falling back, and
  `--mark-remote` (like `chunk sidecar setup`) opts the project in by setting
  `factory.backend` to `circleci`.
- On the circleci backend, validation commands run remotely by default. Set
  `local: true` on a command to run it in the local working tree. `remote: true`
  remains an explicit, backwards-compatible annotation; setting both fields is
  invalid.
- `validate --mark-remote` sets `remote: true` and clears `local: true` on commands
  in `.chunk/config.json`, then exits. With a `[name]` it marks that one command;
  without it every configured command **except `role: autofix`** ones, which it
  names as skipped — a formatter that runs on the sidecar rewrites files there and
  the edits never reach the local working tree. Naming an autofix command marks it
  anyway, for the caller who means it. Commands already marked are reported as no
  change. `chunk sidecar setup` marks install and gate commands automatically, so
  `--mark-remote` is for the rest: a sidecar set up by hand, or a command whose
  role does not qualify. Unmarking is still a hand edit of the config.
- On the circleci backend, command placement is independent of `validation.sidecarImage`: explicit local
  commands stay local, while unspecified and explicitly remote commands use the
  managed sidecar pool. `--local` remains the whole-run local override.
- A run of several commands starts with the local `role: autofix` ones, then the
  remote batch, then the remaining local commands, keeping configured order
  within each. The remote batch syncs the working tree when it starts, so a
  formatter has to finish first for its rewrites to reach the gates.
- **Within the remote batch, independent commands run on separate sidecars, not
  one after another on a single one.** A batch with several remote commands
  (`test`, `lint`, `acceptance-test`, ...) uses up to `defaultValidateParallelism`
  (3) sidecars at once by default, each pulling the next unstarted command from
  the list, matching how `mutate --parallel` and `validate variants --parallel`
  already spread work across a pool. `--parallel <n>` overrides the cap in
  either direction; `PlanCommands` still clamps it to the number of remote
  commands actually selected, so a single-command run never provisions more
  than one. Sidecars a run creates to fill out that pool are kept for reuse by
  later runs rather than torn down when a smaller run only needs one of them.
  Recreating a sidecar costs a full initial sync, so shrinking on every small
  run would make the common case pay for the parallel one.
- **Snapshot selection.** When a sidecar has to be created and no
  `validation.sidecarImage` is recorded (project-level or per-command), `chunk`
  picks one of the org's snapshots instead of booting the bare default image.
  A snapshot matches on its name and tag, split into tokens: the repo name
  (from the git remote, then `vcs.repo`, then the directory name) outranks the
  detected stack, and the org's own snapshot outranks an equivalent system one.
  Matching is token-based, so `go` matches `go-base` but not `mongo-api`. When
  nothing matches, the default image is used and the reason is printed —
  guessing the wrong prepared environment produces failures that look like the
  repo's own. Selection never fails a run: an unreachable snapshot API is
  warned about and treated as no match.
- **`upgrade` follows this fork's releases.** It reads the latest release of
  `web3technologies/chunk-cli` (the one place the repository is named is
  `internal/upgrade`), downloads `chunk_<Os>_<arch>.tar.gz` and verifies it
  against that release's `checksums.txt` before replacing the binary; a missing
  or mismatched checksum aborts with the installed binary untouched. It refuses
  on a source build (`version` is `dev`, or the binary is a symlink as
  `task dev-install` makes) and says to `git pull && task build`, and on a
  Homebrew-managed binary (that is CircleCI's build) and points at the install
  script. Stop the daemon afterwards. The update notice and `chunk watch`'s
  footer use the same source and are silent for `dev` builds.
- Telemetry is opt-out, and off unless the binary was built with a Segment
  write key (`-X .../internal/telemetry.WriteKey=...`; release builds set it
  only if the `CHUNK_TELEMETRY_WRITE_KEY` secret exists). Without a key nothing
  is created or sent whatever the preference says, and `chunk config show` says
  so. When a key is present: events are anonymous until you authenticate with
  CircleCI; after that they include your CircleCI user ID, and earlier
  anonymous events from the same install are linked to it. They never include
  flag values, argument values, names or email addresses. It's disabled by the
  `CHUNK_NO_TELEMETRY` / `NO_ANALYTICS` / `DO_NOT_TRACK` / `CI` environment
  variables (first match wins, in that order), or `chunk config set telemetry false`.
- **Org ID resolution** for `sidecar create`, `sidecar list`, and other sidecar
  subcommands that need an org (in order): `--org-id` flag → `CIRCLECI_ORG_ID`
  env var → `orgID` in `.chunk/config.json` → interactive org picker (TTY only).
  Non-interactive sessions (agents, CI) should set `orgID` in project config or
  pass `--org-id` / `CIRCLECI_ORG_ID`.
- **`conflicts` is advisory and always exits 0.** It reports what the watch
  daemon already computed and never previews a merge itself, so it returns
  immediately. It is deliberately incapable of failing: it runs from the same
  commit hook group as `chunk validate`, whose non-zero exit blocks a commit,
  and a merge conflict with the default branch is information rather than a
  reason to stop. With no daemon running, `--hook` prints nothing at all —
  the daemon is optional, and announcing its absence on every commit would be
  noise — while a manual run says why there is no answer.
- **`conflicts --json` always emits one shape.** Output is a `ConflictReport`
  whether or not there is an answer, so `.known` is always present and a
  consumer never has to tell a report apart from an error object. `.conflict` is
  a pointer and is omitted in one case, so a script must guard it before
  dereferencing:

  | | `known` | `conflict` |
  |---|---|---|
  | No daemon, or a socket it cannot reach | `false` | reason in `conflict.unavailable` |
  | Daemon up, project not tracked | `false` | reason in `conflict.unavailable` |
  | Daemon up, tracked, no check has run yet | `true` | **absent** |
  | Tracked and checked, but not comparable | `true` | reason in `conflict.unavailable` |
  | Tracked and checked | `true` | `conflict.conflicted` is the answer |

  So `known: false` always carries its reason in `conflict.unavailable`, and the
  only report without a `conflict` object at all is the tracked project whose
  first check has not finished. `conflict.unavailable` is the one field for
  every "there is no answer, and here is why": no daemon, an untracked project,
  a detached HEAD, an unfetched target, or a branch that is its own merge
  target.
- **What `conflicts` compares.** The daemon previews merging each project's
  branch into the default branch on `origin`, falling back to `upstream`
  (`DefaultRemoteBranchIn`), re-checking every 60s and refreshing the target's
  remote-tracking ref every 3 minutes. The comparison covers **committed
  history only** — uncommitted work in the tree is invisible to it. A detached
  HEAD, a repo with no recorded default branch, and a branch that is itself the
  merge target all produce "no answer" rather than a clean bill of health.
- **`watch` can show a remote daemon.** With `CHUNK_DAEMON_REMOTE_ADDR` and
  `CHUNK_DAEMON_TCP_TOKEN` set, `watch` reads the daemon at that TCP address instead
  of the local socket, and never starts a local daemon. The header names the
  daemon (`local` or `remote host:port`) and turns red with `unreachable` when the
  last poll failed; a rejected token says so and names `CHUNK_DAEMON_TCP_TOKEN`.
  The local machine's working directory and project registry describe different
  repos than the daemon's, so they are not used: the default is every project the
  daemon tracks, and `watch <path>` / `--focus <path>` take paths **on the daemon's
  host**, sent as typed. `--focus` with no path is an error in this mode.
- **`factory build` takes defaults from the `factory` section of `.chunk/config.json`.**
  `{"factory": {"backend": "docker", "image": "chunk-sandbox:local", "maxAttempts": 5, "log": true}}`.
  Precedence, highest first: an explicitly set flag, then `factory.*`, then the built-in default
  (circleci, 3 attempts, no log). An explicit `--max-attempts 3` beats `factory.maxAttempts`. The docker
  backend uses `--image` or `factory.image` and never `validation.sidecarImage`, which is a CircleCI
  snapshot ID; with neither (and no `CHUNK_DOCKER_IMAGE`) the command fails before the run starts.
  `chunk init` asks where factory runs execute and writes `factory.backend` (and, for docker,
  `factory.dockerfile`).
- **The docker backend's image is resolved, highest first, from `--image`, `factory.image`,
  `factory.dockerfile`, then `CHUNK_DOCKER_IMAGE`.** `factory.image` is explicit and user-managed: it is
  never built, so a repo with its own `docker build` flow keeps it. `factory.dockerfile` (a
  repo-relative path, `Dockerfile.chunk` when `chunk init` wrote it) is managed: the image tag is
  `chunk-<repo>:<12 hex of sha256(Dockerfile + each COPY source's path and content)>`, so a change to
  the Dockerfile or a copied lockfile is a new tag, and `factory build` builds it first when it is
  missing. The build context is exactly the Dockerfile's `COPY` sources (absolute paths, `..` and
  wildcards are rejected), never the repo. A failed build is an error pointing at `chunk factory image`.
- **`chunk init` and `chunk factory image` verify what they build.** The image runs in a throwaway
  container over a copy of the repo: first the agent kit (`git`, `claude`, `sha256sum`, `pkill`), then the
  install step, then the cheapest gate (lint, format, typecheck, test). If the build or a check fails in a
  terminal with `claude` installed, chunk offers to let Claude edit `Dockerfile.chunk` (up to 3 attempts,
  only that file may change) and prints the diff on success. `factory build` never verifies or repairs.
- **The docker backend runs the install step once per sidecar** (the detected `install` setup step, else
  the `install` command) after the first sync and before the agent starts, streaming its output into the
  run log. Existing `pnpm install &&` prefixes in validation commands are left alone.
- **`Dockerfile.chunk` is generated from detection, never overwritten silently.** The base image is the
  one every CI gate job uses (`.circleci/config.yml`), else the detected stack's image. Dependencies
  are baked as the package manager's store (pnpm, npm, yarn, uv, pip, Go, Cargo), not as an installed
  tree, because the workspace is bind-mounted over the repo at run time. An unknown stack gets a
  minimal Debian image with a comment to add a toolchain.
- **The factory branch's commits are attributed to the developer.** They use `factory.author`
  (`"Name <email>"`) when set, else the repo's git `user.name`/`user.email`, else `chunk`. These are
  the commits a published PR carries. `.chunk/config.json` is committed, so `factory.author` applies to
  everyone in the repo: `chunk init` writes it only when you choose someone other than your git
  identity. `chunk factory author` shows the author and its source; with an argument it sets
  `factory.author`, and `--unset` removes it.
- **`factory build --plan FILE` reads the prompt from a file**, as if piped on stdin. It cannot be combined
  with a prompt argument or redirected stdin; with `--continue` it is optional guidance, like an argument.
- **`factory build` runs on the local daemon, in the background.** `chunk factory build "<prompt>"` (or
  `chunk factory build < prompt.md`; `chunk factory build - < prompt.md` is the explicit
  form; a prompt argument with a non-empty file on stdin is refused, so a
  stray word such as `--log run.log < prompt.md` cannot drop the file) checks
  the project's review prompts and validation commands, starts the daemon if
  needed, and starts a factory session, which the daemon starts tracking the
  project for if it has not seen it before; it follows the run
  until it ends. Ctrl-C stops the run and waits for it to commit the work done
  so far; the run belongs to the daemon, so if `chunk factory build` loses contact it
  keeps going and `chunk watch` still shows it. It uses the daemon's
  Claude and CircleCI credentials, resolved when the daemon started, and the
  project's configured org and image unless `--org-id` or `--image` say
  otherwise. It is refused when `CHUNK_DAEMON_REMOTE_ADDR` is set, since a run
  works from this machine's files. The project needs a git remote named
  `origin` (the sidecars clone from it); `factory build` fails up front without one.
  A project has one active run at a time. Runs are kept in memory: a daemon
  restart loses the record, but not the work committed on the run's branch. `--reviews` must name a directory inside the project. Every reviewer is
  given the original factory prompt as the requested-change specification, so
  the files in that directory can be reusable review lenses rather than copies
  of a particular request. Every reviewer is also given a severity rubric: only
  a problem it can demonstrate (it breaks the requested behavior or existing
  behavior, degrades run time, loses data or is a security problem) may be high
  or medium and so send the implementer back; style remarks, tests that could
  be more thorough and other suggestions are low and never block. From the
  second round, each reviewer is shown its own earlier blocking findings and the
  implementer's reply to them, so it checks whether they were fixed rather than
  raising them again or contradicting itself. As it follows the run it prints the sidecars being
  prepared and each round's findings, with the end of any failed validation
  command's output.
  Factory branches are named `chunk/factory/<description>/<run id>`. The
  description is a short slug made from the prompt's first line, while the
  final segment remains the stable run ID. When the run ends it prints that
  branch, how to keep it, how long the run took and what the implementer cost,
  and exits non-zero unless every check passed. `--log` has the run keep a
  plain-text log in `~/.chunk/factory/run-<start time>.log`, and
  `--log-file <path>` keeps it in a file of your choosing instead (it implies
  `--log`), with the full context the display leaves
  out — each prompt the implementer is sent, what it did and said, every
  review's findings and prose, and failed commands' output — for a person or an
  agent to judge the run and its prompts by. The daemon writes it, so a
  relative path is resolved against the caller's directory first. `--verbose`
  adds the review prompts, passing commands' output, and a check each round
  that every reviewer has the implementer's change, also shows the log in the
  terminal as the run writes it, and implies `--log`.
  `--implementer-instructions` appends run-specific guidance to the implementer's
  fixed safety prompt, which is useful for controlled harness experiments.
- **`factory build --continue` picks up a finished run's work.** A run whose checks
  still fail when it ends (exhausted or stuck) prints
  `chunk factory build --continue <run id>`. The continued run reopens the earlier
  run's worktree (adding it back from its branch if it was removed, and first
  committing any edits made in it by hand), adds a commit to the same branch,
  and measures the work from the same baseline, so reviewers see the whole
  change rather than only what the continuation adds: the sidecars are synced
  from the baseline's files and the work is then laid on the implementer's as
  uncommitted changes. The implementer is a new Claude session and is told the
  original request and where the work is. A prompt is optional and adds to the
  request, for the implementer and the reviewers alike. With one, the
  implementer starts on it; without one, the run checks the work as it is
  first and sends the implementer what failed, and that round counts toward
  `--max-attempts`. Given no prompt in a terminal, it asks what to do
  differently first; Enter skips that and checks first. Without a terminal it
  does not ask. Each run keeps a record (its request, worktree, branch
  and baseline) in the project's data directory beside its worktree, which is
  what `--continue` reads, so a run can be continued after the daemon
  restarts; runs from before records were kept cannot be continued. The
  session's record names the run it continues as `continues_run_id`.
- **`watch` is organized around runs.** Factory runs are listed at the top of
  the left pane, live ones first, then up to five that ended in the last day;
  sidecars are listed below them, and `↑/↓` moves through both as one list. The
  first poll selects the newest run. A selected run's pane shows where it stands
  and where its work is (prompt, worktree, branch, log), then each round: the
  implementer's turn (duration, cost, diff stat), each validation command, and
  each review with its findings, followed by the run's latest progress and its
  sidecars. A factory review is a check: it passes unless it found something
  worth changing (high or medium severity), so its row shows ✓ with any lesser
  findings noted, or ✗ with how many are worth changing. A factory run's own
  sidecars are shown there rather than as rows of their own. `→` moves into
  the pane, `↑/↓` picks a row and `Enter` opens it: a review's live log, a
  failed check's output, or the implementer's summary. The summary and a
  check's output are word-wrapped to the screen; a log keeps one line per line
  of output. The footer only lists keys that do something for what is
  selected, so `x` is gone once a run has ended. **`q` only detaches**: the run
  belongs to the daemon and carries on. The one key that stops it is `x`, and it
  needs a second `x` on the same run to confirm.
- `watch` requires a TTY — it exits with an error if stdout is not a terminal. It polls sidecar state every 5 seconds and keeps an in-memory window of the 300 most recent event log entries. Use `j`/`k` or `↑`/`↓` to select a sidecar, `q` or `Esc` to quit. By default it watches every project it knows about; pass `--focus` to watch only the current directory. Running `watch` in a project also registers that project so future runs find it. `--all` is deprecated — it is now the default.
- **Run results are read from disk, not sent to the daemon.** Every `validate` run
  writes its events to the project's event log and registers the project (a
  `project-root` breadcrumb in the same data directory) whether or not a daemon
  is running. The daemon discovers projects from those breadcrumbs and replays
  each log from the start, so a sidecar run made with no dashboard open is there
  in full the next time `watch` opens. A purely local run has no sidecar, so it
  gets no row. (Remote command *output* is the exception: it is registered with
  the daemon live and buffered in memory, keyed by the same project root, so it
  too is only listed for a project that has been registered.)
- **`watch` can show a command's output.** In the activity pane, an invocation
  marked `▤` has output the daemon still holds; `Enter` opens a scrollback view
  of it. A command that is still running tails live — the pane polls every 200 ms
  while it is open, and drops back to the 5 s snapshot tick when closed, so a tail
  never speeds up everything else. `↑`/`↓` and `PgUp`/`PgDn` scroll, `g` jumps to
  the top, `G` re-follows the end, and `Esc` closes the pane rather than quitting
  the dashboard. Scrolling up detaches from the bottom so arriving output does not
  yank the view away from what you are reading; scrolling back to the end
  re-follows automatically.
  - Output is capped at 256 KiB per command, keeping the **tail** — the end of a
    failed run is the part worth reading. When earlier output has been dropped the
    pane says `(earlier output dropped)` rather than presenting a partial run as if
    it were whole.
  - The daemon holds at most 20 commands per project, evicting the oldest
    *finished* one first. A running command is never evicted.
  - Buffers are in memory only, so restarting the daemon loses them. `Enter` on an
    invocation the daemon no longer knows about says so.
  - Output streaming needs a CircleCI token. Without one the dashboard still works
    and shows a one-line hint in the footer; the daemon never prompts.
- **`chunk sidecar logs <command-id>`** is the non-TUI door onto the same output,
  for scripts and for agents with no terminal. Command IDs appear in the `watch`
  dashboard. It reads the chunk daemon's buffer when the daemon has the command
  and falls back to streaming from the API when it does not, so it works either
  way. `-f`/`--follow` keeps printing until the command exits.
  - Output goes to stdout and nothing else does, so it can be piped. A non-zero
    remote status is reported on **stderr** as `exit status N`.
  - `logs` itself exits non-zero only when *reading* failed. A failing command is
    not a failing read — conflating the two would make the exit status useless to
    a caller.
- **`watch` shows live resource usage** for the selected sidecar — CPU, memory and
  disk, sampled every 2 seconds. Sampling only runs while a dashboard is attached,
  since each sampled sidecar is running a shell loop for it; close `watch` and it
  stops. A
  sample older than three intervals renders dimmed and marked `(stale)` rather
  than disappearing, so a stalled sampler looks stalled instead of looking like an
  idle sidecar. Memory is read from the sidecar's cgroup, not `/proc/meminfo`,
  because the latter reports the host's memory.
- **`watch` rows are per branch, except when they cannot be.** A branch's local runs
  are folded into its sidecar's row, so one row shows both kinds of run along with
  sync state. A branch with more than one sidecar — two agent sessions in one
  working tree, which is the normal shape (see **Sidecar state is per agent
  session**) — keeps a row each, with local runs as their own row, since folding
  them together hid a session's row and listed its activity under the other
  session's name. Rows that share a branch carry a label naming the session
  (`⧉ session d7d915b2`), because the branch name no longer tells them apart and
  generated sidecar names shorten to the repo name for all of them.
- `chunk init` uses Claude to auto-detect the test command for the project.
  It generates `.claude/settings.json` with pre-commit hooks. It never touches
  CircleCI — tokens are prompted inline only when a command actually needs them.
- `sidecar sync` mirrors the working tree to the sidecar with rsync, honouring
  `.gitignore`. The branch does not need to be pushed to GitHub. Pooled
  `validate` and `review` use the same mechanism.
- `sidecar current` prints the owning session (`session <8 chars>`) alongside the
  name and ID. A sidecar keeps the name it was created with, and adoption transfers
  it without renaming, so the name often carries the session that created it rather
  than the one using it now — the session line is what tells two parallel sessions
  apart. `--json` carries `session_id` and `owner_pid` in full.
- `sidecar ssh -- <cmd>` forwards stdin when the process stdin is a pipe, enabling
  patterns like `cat bundle | chunk sidecar ssh -- git fetch ...`.
- **`sidecar ssh` exits with the remote status**, for both a command and an
  interactive shell, and prints nothing of its own, the same as `ssh` and
  `sidecar exec`. A remote status can therefore coincide with one of chunk's own
  exit codes (2–5); chunk's own failures are the ones that print an error. A
  process killed by a signal additionally reports the signal on stderr.
- **Sidecar state is per agent session.** Two agent sessions open in the same
  working tree must not share a sidecar: they would sync into the same remote
  workspace and reset the checkout under each other's test run. State files are
  keyed by session and branch, where the session is the Stop hook payload's
  `session_id`, or `CLAUDE_CODE_SESSION_ID` (overridable with `CHUNK_SESSION_ID`)
  for the commands an agent runs itself. Every session always creates its own
  sidecar — adoption was removed to prevent two sessions from syncing into the
  same remote workspace and resetting each other's checkout. Parallel sessions
  cost one sidecar each; sequential ones reuse the same one (same session ID,
  same state file).
- **Abandoned sidecars are reaped automatically.** Local sidecar state is one file
  per session and branch, and `validate` sweeps them before resolving a sidecar:
  state naming a sidecar absent from the org listing is deleted, and a sidecar
  still running whose state has not been touched for 5 days is deleted through the
  API and its state dropped. The sidecar the current run is about to use is never
  deleted by age, and one that is alive with recently touched state is left alone
  so a concurrent session on another branch keeps its own. The sweep needs an org
  ID that resolves without prompting (see **Org ID resolution**), skips silently
  without one, and deletes nothing if the listing fails, since an empty listing is
  not proof of absence. A sidecar the API rejects as out of date (410) is deleted
  when a sync hits it, because no listing reveals that state.
- **The `validate variants` pool is outside that scheme.** Variants are queued
  across temporary pool members, which are not written to active-pool state —
  workers would race on it and leave the user's own session pointing at a sidecar
  about to be deleted. That also makes them invisible to the reaper above, so the
  command cleans up after itself instead: it deletes each sidecar when the pool
  shuts down, catches SIGINT/SIGTERM so an interrupt still unwinds through those
  deletes, and sweeps stranded `variant-*` sidecars from an earlier crashed run
  before starting a new one. Each name carries that sidecar's own creation time
  (`variant-<base36 seconds>--<id>`), which is what lets the sweep spare a
  concurrent run's in-flight sidecars — two runs at once, in two worktrees or two
  repos, is a normal shape for the mutation-testing skill — and only collect ones
  too old for any live run to own. Per-sidecar rather than per-run, because a run
  of a hundred variants takes hours and its newest sidecar must not inherit the
  age of the run that booted it. The delimiter is two dashes and variant IDs are
  sanitised to collapse dash runs, so the timestamp is recoverable by a plain
  split rather than by guessing which segment is a date. A name without one is
  reported and left alone. For the same reason `validate variants` syncs
  without persisting a workspace and therefore requires a resolvable workdir; it
  resolves one through `sidecar.ResolveWorkspace`, the same
  `--workdir` → active sidecar → `<sidecarHome>/<repo>` order as every other
  sidecar command, and fails before booting anything when none of the three
  applies.
- **`validate variants` never reports an environmental failure as a caught
  mutant.** A killed variant means the validate command ran and exited non-zero
  on its own terms. Exit codes 126 and 127 are the shell failing to run the
  command at all, and a command that outruns its timeout never returned a verdict;
  both are recorded as errors instead, and errors are neither kills nor survivors.
  Commands are template-expanded locally before being shipped, since a literal
  `{{CHANGED_PACKAGES}}` reaching the remote shell would otherwise fail every
  variant the same way. The command warns when every variant was killed, because
  that pattern is more often one broken command than a fully covered codebase.
- Commands that require a CircleCI token (`task run`, `task config`, `sidecar *`,
  `validate --sidecar-id`) prompt for it inline at the point of need rather than
  failing with an error.
- `chunk auth set github` stores a GitHub token in the config file; previously
  only the `GITHUB_TOKEN` environment variable was supported.
- `chunk hook disable` creates a `.chunk/hooks-disabled` sentinel file inspected by the `chunk validate` Stop hook; `hook enable` removes it. Stop-hook validation is also disabled when `CHUNK_HOOKS_DISABLED` is set in the environment.
- **Hook runs may be validated in the background.** When the chunk daemon is
  running, a Stop-hook `chunk validate` offers the daemon the choice of running
  the checks after the hook has exited. The daemon takes the offer for a change
  under 500 lines, or one confined to docs and text files, and holds the caller
  for anything larger, anything it cannot measure, and any project whose last
  run failed. Size is measured against the last state that passed its checks
  rather than against `HEAD`, so consecutive small changes do not accumulate
  into a large one and a commit neither hides a change nor resets the count. Results of a background run reach the agent on its next turn, via
  the `chunk validate results` hook. Only hook runs are ever released — a
  `chunk validate` typed at a terminal always waits. Set `asyncValidate` in
  `.chunk/config.json` to `never` or `always` to override the judgement, and
  `asyncValidateMaxLines` to move the threshold, `asyncValidateInert` and
  `asyncValidateBlocking` to say which paths it applies to. See
  [HOOKS.md](HOOKS.md#background-validation).
- `chunk validate` caches successful runs in hook mode only, keyed by
  `.chunk/config.json`, the execution target, the HEAD SHA, and the contents of
  all changed files; a repeat hook invocation with nothing changed prints
  `skipped` instead of re-running. Manual runs, `--cmd` inline commands, and
  repos whose state cannot be hashed never cache. Entries expire after 7 days.
  See [HOOKS.md](HOOKS.md#result-caching).
- **`validate --async` and `validate results` are two halves of one flow.**
  `--async` hands the run to the chunk daemon and returns immediately; `results`
  reads what finished and prints it to stdout. Nothing installs a hook to call
  `results` yet: a background run is something you ask for explicitly, so
  reading its answer back is too. Wiring it into a hook waits for the change
  that makes background runs happen on their own. A result is reported once and
  then forgotten. A live-tree run whose working tree changed while it was in
  flight has its verdict discarded: a pass describing code that is no longer on
  disk reads as a green light for work already changed. The discard itself is
  reported, without an exit code or output, because the commonest reason the
  tree moved is the run — output written, a golden regenerated, a lockfile
  touched — and staying silent about that is indistinguishable from no run
  having happened, which leaves such a project getting nothing with no way to
  tell why. `TaskState.Passed` consults `Stale`, so a stripped verdict cannot
  read as a clean pass. Stopping the tree moving under the run in the first
  place is a matter of where the run happens, not how its result is judged —
  which is what `asyncValidateWorktree` does: a run that validated a snapshot
  keeps its verdict and is reported with the state it describes named.
- **`validate results` is a subcommand, not a flag, because it validates
  nothing.** It reads daemon state. A flag on `validate` would read as a
  modifier of a run that never happens.
- Every command checks GitHub for a newer release in the background and prints a
  notice to stderr once it finishes. The result is cached in the app state dir
  (`update-check.json`) for 24 h to stay inside GitHub's unauthenticated rate
  limit of 60 requests/hour. The window is claimed before the request is made,
  so a command that exits mid-flight does not send the next invocation back to
  GitHub — the notice simply appears on a later run.
- The update check never delays a command: if the check has not finished by the
  time the command has, the notice is dropped rather than waited for. It is
  skipped entirely in CI (`CI` set), when the user sets
  `CHUNK_NO_UPDATE_CHECK`, under `go test`, and for `completion`,
  `receive-telemetry`, `upgrade`, and shell-completion requests, which either run
  on every keypress or report versions themselves. `watch` runs its own check and
  renders the notice in the TUI footer instead.
- The notice suggests the install script when the running binary resolves into
  a Homebrew prefix (that is CircleCI's build, not this project's), and
  `chunk upgrade` otherwise. `chunk upgrade` refuses to replace a
  Homebrew-managed binary in place. No notice is shown for `dev` builds.
- The notice names both versions (`v1.0.0 → v1.0.1`) and links the new
  release's GitHub page, so the reason to upgrade is one click away. The `watch`
  footer keeps its one-line form: it has no room for a URL.

## Config keys

| Key | Scope | Description |
|-----|-------|-------------|
| `model` | user config (`~/.config/chunk/config.json`) | Claude model override |
| `telemetry` | user config (`~/.config/chunk/config.json`) | Usage telemetry preference (`true`/`false`, default: `true`); only matters to a build that has a telemetry write key, which default builds do not |
| `notifications` | user config (`~/.config/chunk/config.json`) | OS desktop notification after validate completes (`true`/`false`, default: `false`) |
| `orgID` | `.chunk/config.json` | CircleCI organization ID for sidecar subcommands |
| `validation.sidecarImage` | `.chunk/config.json` | Snapshot or image ID for sidecar bootstrap and validate (unset: a matching org snapshot is selected automatically) |
| `asyncValidate` | `.chunk/config.json` | Whether hook runs may be validated in the background: `auto` (default), `always`, `never` |
| `asyncValidateMaxLines` | `.chunk/config.json` | Largest change, in lines, still validated in the background under `auto` (default: 500) |
| `asyncValidateWorktree` | `.chunk/config.json` | Run background checks in a checked-out snapshot so edits cannot make the result stale (`true`/`false`, default: `false` — a snapshot holds nothing gitignored) |
| `asyncValidateInert` | `.chunk/config.json` | Extensions (`.sql`) or file names (`NOTICE`) this project also counts as prose, validated in the background at any size; adds to the built-in list |
| `asyncValidateBlocking` | `.chunk/config.json` | Extensions or file names that always block, over an inert default, the line threshold, and `asyncValidate: always` |

`chunk config show` displays resolved user credentials and, when run from a
project directory, the resolved `orgID` (env var takes precedence over project
config).

## Flag Conventions

- Required flags use cobra's `MarkFlagRequired()`
- Comma-separated lists are split with `strings.Split(s, ",")`
- Dates use `YYYY-MM-DD` format, parsed with `time.Parse("2006-01-02", s)`
- Boolean toggles default to `false`
- Model flags fall back to config file values, then built-in defaults
