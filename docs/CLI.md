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
│   ├── status                      # Check authentication status (CircleCI, Anthropic, GitHub)
│   └── remove <provider>           # Remove stored credential (circleci | anthropic | anthropic-oauth | github)
│
├── org                             # Manage CircleCI organizations
│   └── create <name>               # Create a new standalone CircleCI organization
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
│   --skip-test-suites              # Skip .circleci/test-suites.yml scaffolding (default: true; pass =false to generate)
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
│       --image <id>                # Snapshot image ID (default: validation.sidecarImage)
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
│   │   --image <image>             # Snapshot ID, or a system image name such as cimg-go:1.27-claude
│   │   --size <size>               # small, medium, large (default), xlarge or 2xlarge; system images only
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
│       │   --skip-config           # Don't record it as validation.sidecarImage
│       ├── get <snapshot-id>       # Get a snapshot by ID
│       │   --json                  # Output as JSON
│       └── list                    # List snapshots
│           --org-id <id>           # Organization ID
│           --json                  # Output as JSON
│
├── factory                         # Build and manage factory runs
│   ├── build [prompt|file|-]       # Implement a prompt on a sidecar, then review and validate it until it passes
│   │   --max-attempts <n>          # Maximum rounds of implement, review and check (default: 3)
│   │   --continue <run id|branch>  # Pick up an earlier run; its ID is the branch's final segment
│   │   --reviewers <n>             # Reviewer sidecars (0: one per review prompt)
│   │   --reviews <dir>             # Directory of review prompts (default: .chunk/reviews)
│   │   --no-validate               # Skip the project's validation commands
│   │   --keep-sidecars             # Leave the sidecars running when the run ends
│   │   --org-id <id>               # Organization ID
│   │   --image <id>                # Snapshot image ID (default: validation.sidecarImage)
│   │   --model <name>              # Claude model (default: Claude Code's default)
│   │   --implement-timeout <duration> # Max time for each implementer turn
│   │   --review-timeout <duration> # Max time for each review
│   │   --log                       # Keep a log of the run's full context in ~/.local/share/chunk/repo-<id>/factory/<start time>.log
│   │   --log-file <path>           # Keep the log in this file instead (implies --log)
│   │   --verbose                   # Log review prompts, passing output and reviewer checks, and show the log here (implies --log)
│   │   --open-pr                   # Refuse to start the run unless its pull request could be opened
│   │   --no-open-pr                # Do not push the branch or open a pull request when every check passes
│   │   --draft                     # Open the pull request as a draft
│   │   --remote                    # Provision a sidecar of its own for the run; bring the work back with 'chunk factory pull'
│   │   --json                      # Output as JSON
│   ├── pull [run id]               # Bring a --remote run's work back (default: the most recently started remote run)
│   │   --json                      # Output as JSON
│   ├── start [prompt|file|-]       # Hand the factory a diff and a prompt to refine
│   │   --diff <file|->             # Start from a patch of work done elsewhere
│   │   --json                      # Output as JSON
│   ├── plan [prompt]               # Plan a change with Claude, then send the plan to the factory
│   │   --model <model>             # Model Claude plans with (default: your Claude Code default)
│   ├── bootstrap                   # Set up this project for chunk factory: config, sidecar image, review prompts
│   │   --output <dir>              # Directory to write them to (default: .chunk/reviews)
│   │   --skip-sidecar              # Leave out setting up and recording the sidecar image
│   │   --snapshot-name <name>      # Name the sidecar image's snapshot (default: <repo>-<YYYY-MM-DD>-<HHMMSS>)
│   │   --org-id <id>               # Organization ID
│   │   --image <id>                # Image with Claude Code, for both sidecars (default: cimg-base:2026.09-claude)
│   │   --model <name>              # Claude model (default: Claude Code's default)
│   │   --timeout <duration>        # Max time for writing the prompts (default: 30m)
│   │   --no-history                # Leave out the team's review comments from GitHub
│   │   --since <YYYY-MM-DD>        # Use review comments from this date on (default: 90 days ago)
│   │   --top <n>                   # Use review comments from this many reviewers (default: 5)
│   │   --json                      # Output as JSON
│   └── eval [prompts-dir]          # (hidden) Score review prompts by reviewing past changes and grading what they find
│       --commits <n>               # Review this many recent commits (this or --suite is required)
│       --from <rev>                # Take --commits from this revision's history (default: HEAD)
│       --suite <dir>               # Review the cases of the suite.json in this directory instead
│       --runs <n>                  # Times to review each case (default: 1)
│       --parallelism <n>           # Maximum number of sidecars (0: one per prompt)
│       --output <path>             # Also write the full report as JSON to this file
│       --org-id <id>               # Organization ID
│       --image <id>                # Snapshot image ID (default: validation.sidecarImage)
│       --model <name>              # Claude model for reviews (default: Claude Code's default)
│       --timeout <duration>        # Max time for each review
│       --no-grade                  # Score on the suite's labels alone (not with --commits)
│       --grader-model <name>       # Claude model for the grader (default: Claude Code's default)
│       --grader-timeout <duration> # Max time for grading each case (default: 20m)
│       --json                      # Output as JSON
│
├── watch [dir...]                  # Follow and manage your factory runs live
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
└── upgrade                         # Update to latest version
```

## Behavior Decisions

- `auth login` and `auth signup` both use OAuth and store the resulting token in the system keychain (or `~/.config/chunk/config.json` with `--insecure-storage`). They differ only in which page the browser opens: login for existing accounts, signup for new ones. Use `--no-browser` to print the URL instead of opening it automatically.
- `auth signup` fails with a user-friendly error if a CircleCI token is already stored; run `chunk auth remove circleci` first to clear it. Existing accounts should use `chunk auth login`.
- After a successful `auth signup`, chunk checks whether the new account belongs to any organization. If it belongs to none, chunk prompts for a name and creates a standalone org, so the first sidecar command does not fail on having none. Without a terminal it prints a warning pointing at `chunk org create <name>` instead; the signup itself still succeeds.
- Commands that resolve an org (sidecar, validate, init, mutate, prune) offer the same org creation prompt when the account has no organizations. Without a terminal, or with `CI` set, sidecar, validate, mutate and prune fail with a suggestion to run `chunk org create <name>` or pass `--org-id`; `init` prints a warning and finishes without writing an org ID.
- `org create` is hidden from the default help output. It requires CircleCI authentication and creates a standalone org (not tied to a VCS provider), printing the org name, ID, and slug on success.
- `task run` defaults to pipeline-as-tool mode; use `--no-pipeline-as-tool`
  to disable.
- `config set` user keys: `model`, `telemetry`, `notifications`. Project keys (`.chunk/config.json`):
  `orgID`, `validation.sidecarImage`. Credentials use `chunk auth set`, not `config set`.
- Validation commands run remotely by default. Set `local: true` on a command to
  run it in the local working tree. `remote: true` remains an explicit,
  backwards-compatible annotation; setting both fields is invalid.
- `validate --mark-remote` sets `remote: true` and clears `local: true` on commands
  in `.chunk/config.json`, then exits. With a `[name]` it marks that one command;
  without it every configured command **except `role: autofix`** ones, which it
  names as skipped — a formatter that runs on the sidecar rewrites files there and
  the edits never reach the local working tree. Naming an autofix command marks it
  anyway, for the caller who means it. Commands already marked are reported as no
  change. `chunk sidecar setup` marks install and gate commands automatically, so
  `--mark-remote` is for the rest: a sidecar set up by hand, or a command whose
  role does not qualify. Unmarking is still a hand edit of the config.
- Command placement is independent of `validation.sidecarImage`: explicit local
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
- Telemetry is opt-out. Events are anonymous until you authenticate with
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
- **A lone prompt argument with no spaces that names an existing file is read as the
  prompt, wherever the file is.** `chunk factory build ../notes/spec.md` (and
  `chunk factory start`, the same way) reads that file on this machine and
  runs on the repository where the command was triggered, never the file's
  own repository, which is untouched. The run's record keeps the file's path
  as where the prompt came from (`spec_origin` in its JSON, and `chunk watch`
  shows it above the prompt). A word with no spaces and a `.md`, `.markdown`
  or `.txt` extension that does not exist is an error, `file not found`,
  rather than literal text, since that is a far likelier mistyped path than a
  prompt; a directory, a binary file and a file over 1 MiB (chunk's cap on how
  much of a file it reads as a prompt) are refused the same way, each before
  any sidecar is created. Anything else — an argument with spaces, or a
  single word that is not an existing file — is literal prompt text, even one
  that names a file: `chunk factory build "fix README.md typos"`.
- **`factory build` (and `factory build --remote`) say what they are about to do
  before any of their plumbing.** On a terminal, before the run ID and
  "started on the chunk daemon" (or, with `--remote`, before the sidecar is
  provisioned), it prints a plain-language line: that chunk implements the
  change (or, with `--continue`, reviews the existing work), reviews it and
  runs the project's checks until they pass, for how many review rounds and
  the flag that changes that ("up to 3 review rounds (change with
  --max-attempts)"), and what the run ends with. A second line recommends
  `chunk watch` as the best way to follow it and every other run, and what it
  shows (findings, check output, the pull request); you can also leave it
  running. What it says the run ends with matches the flags: a pull request by
  default (hedged, since a project with no GitHub token or origin still runs
  and opens none), the pull request with `--open-pr`, a draft pull request
  with `--draft` and `--open-pr` together (hedged the same way as the default
  when `--draft` is given alone, since `--draft` by itself still resolves to
  the same auto mode as the default and can likewise end with none), none with
  `--no-open-pr` (the work stays on the local branch), and with `--remote`,
  that it keeps running on its own sidecar, `chunk factory pull` brings the
  work back, and it opens no pull request yet.
  These lines are left out with `--json`, in CI (`CI` set), and when stdout or
  stderr is not a terminal (a log file, a pipe), so none of those changes
  anything already being parsed.
- **`chunk watch` is the recommended way to interact with chunk.** The plain
  output of `factory build` is a log of a run; `chunk watch` shows every run
  in every project, each review finding with its severity and location, the
  output of each failed check, the implementer's live activity and the pull
  request, with keys to copy, expand and cancel. So on a terminal chunk points
  there when it helps: at the start of a run (above), at the end of a failed
  check's or a review with findings' line ("details in chunk watch"), at the
  end of every run with its run ID (before `--continue <run id>`, when the
  run can be carried on), after `chunk init`, and in bare `chunk` help. Each
  pointer follows the same rule as the start message: never with `--json`, in
  CI, or when stdout or stderr is not a terminal. Chunk never opens `watch` for
  you.
- **A factory run works in rounds, and `--max-attempts` caps them.** A round
  is one implementer pass, then the reviews and the project's checks. The run
  stops when every check passes or after `--max-attempts` rounds (default: 3).
  A run that reaches the cap with checks still failing says so, names
  `--max-attempts` to raise it and, when it committed work, the
  `chunk factory build --continue <run id>` command that carries it on. Raise
  the cap when the change is large or the reviewers are strict and runs keep
  ending with feedback left, so a run can finish without `--continue`. Lower
  it, down to 1, for quick or exploratory changes or to cap spend: each round
  costs an implementer turn plus every reviewer, so more rounds cost more and
  take longer.
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
  A project can have several runs going at once, each in its own worktree and
  on its own sidecars. A run that continues another works in the worktree of the
  first run of its chain, so it is refused while any run of that chain is going. Runs are kept in memory: a daemon
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
  command's output. While the implementer works, a terminal shows one line,
  redrawn in place, of how long the turn has run, what the implementer last did
  and said, and how many tools it has used, with `quiet <n>m` once it has done
  nothing for five minutes; without a terminal the same is printed every two
  minutes.
  Factory branches are named `chunk/factory/<description>/<run id>`. The
  description is a short slug made from the prompt's first line (for a plan
  from `factory plan`, its `# name` line), while the final segment remains the
  stable run ID. When the run ends it prints that
  branch, how to keep it, how long the run took and what the implementer cost,
  and exits non-zero unless every check passed. `--log` has the run keep a
  plain-text log in `~/.local/share/chunk/repo-<id>/factory/<start time>.log`, and
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
- **`factory build` opens a pull request once the checks pass.** When the loop
  ends with every check passed, the daemon pushes the run's branch to `origin`
  (with the developer's own git credentials, never prompting) and opens a pull
  request on GitHub, ready for review; `--draft` opens a draft instead, and
  `--no-open-pr` leaves the branch alone. The title is the prompt's first line,
  or for a plan from `factory plan` the text of its `## 1. Outcome`, cut
  between words to 72 characters; the commit's subject comes from the same
  place. The description holds the request, the
  diff stat and the checks that passed, with a note when the branch also carries
  the uncommitted work the run started from. It targets the branch the run was
  started from if `origin` has it, and the repository's default branch otherwise
  (a local-only branch, or a factory branch, cannot be a target). A run whose
  checks did not pass, was cancelled or made no changes pushes nothing and opens
  nothing. It needs a GitHub token with write access to pull requests
  (`GITHUB_TOKEN` or `chunk auth set github`, then restart the daemon, which reads
  it at start) and an `origin` on GitHub; a project with neither still runs — the
  work is worth doing either way — and the run records why it opened none.
  `--open-pr` insists on the pull request instead: what it needs is checked when
  the request is accepted, so a project that could not open one is refused before
  the run costs minutes. `chunk auth` only checks that a token works, not what it
  may do, so a read-only token is found when the pull request is refused. An
  organization with single sign-on also refuses a token until it is authorized there
  (GitHub's token page, Configure SSO), and the error says so. The branch includes
  the baseline commit when the run started from uncommitted work, so that commit is
  part of the pull request. A `--continue` run pushes the same branch again, which
  updates the pull request already open for it: GitHub refuses a second one, and the
  run records the open one rather than a failure. If the checks passed but the pull
  request could not be opened (a refused token, say), the work stays committed, and
  pushed if the push got that far; the command prints why, says where the work is,
  and exits non-zero. The session's `factory.pull_request` records
  `state` (`pending`, `pushing`, `opening`, `opened`, `failed` or `skipped`), `url`,
  `number`, `base` and a `message` for the last two, which `chunk watch` draws as
  the run pane's `Pull request` row and as `PR #<n>` beside an ended run in the run list.
  Once it is open, the daemon asks GitHub about it about once a minute and records
  `status`: `up` (nobody has reviewed it or been asked to), `in_review`, `feedback`
  (changes requested, or review threads still unresolved), `checks_failing`,
  `merged` or `closed`. Feedback outranks failing checks, and a reviewer's latest
  verdict is the one that counts, so one who asked for changes and then approved
  no longer blocks. `chunk watch` shows it as the run's chip in place of `passed`
  (`PR up`, `in review`, `feedback`, `checks failing`, `merged`, `closed`) and on
  the `Pull request` row, and the run list search finds it. It is followed while the run is
  going and for a day after it ends, which is as long as the run list keeps it, and
  not once merged or closed. With no GitHub token the status stays unset and the run
  shows `passed`.
  A `--remote` run does not go through the local daemon, so it opens none, and `--open-pr` or `--draft` with `--remote` is refused. Only GitHub is supported.
- **`factory build --continue` picks up a finished run's work.** A run that
  ends with committed work short of passing (exhausted or stuck, or failed or
  cancelled part way, such as on a reviewer sidecar that could not be made)
  prints `chunk factory build --continue <run id>`, and `r` in `chunk watch`
  starts the same run, with no prompt, from the run's pane. The continued run reopens the earlier
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
  and baseline) in the repository's data directory beside its worktree, shared
  by every checkout of the repository, which is what `--continue` reads, so a run can be continued after the daemon
  restarts; runs from before records were kept cannot be continued. The
  session's record names the run it continues as `continues_run_id`.
- **`factory build --remote` runs the build on a sidecar of its own, so you can
  close your laptop; `chunk factory pull` brings the work back.** It makes the
  run's worktree and record locally, as a plain build does, then provisions a
  one-sidecar pool for the host, installs chunk there (the release matching
  this build, unless `--chunk-binary` uploads a Linux build of your own -
  hidden, for testing), makes the baseline commit, and execs `chunk factory
  build` on the host as one foreground command, with the flags that mean
  something there forwarded (`--max-attempts`, `--image`, `--reviewers`,
  `--reviews`, `--no-validate`, `--keep-sidecars`, `--model`,
  `--implementer-instructions`, `--implement-timeout`, `--review-timeout`) and
  always `--no-open-pr`: a remote run does not go through the local daemon,
  so it opens no pull request, and `--open-pr`/`--draft` are refused with it.
  `--continue`, `--log`, `--log-file` and `--verbose` are refused too, since
  none have meaning without the local daemon. The host's stderr streams to the
  terminal as the run goes; Ctrl-C only detaches - it sends no cancel and
  deletes nothing - and prints `chunk factory pull <run id>`. The record is
  saved once the host's own exec is submitted, with `remote` set to
  `sidecar_id`, `command_id` and `host_repo` (the synced checkout, not where
  the work ends up).
  `chunk factory pull [run id]` brings that work back: with no run id, the
  most recently saved record with `remote` set, and with one, that run; a
  record with no remote run is refused. It waits out the host's run if it has
  not ended yet (following it exactly as `--remote` does), then reads the
  host's own run record over SSH - not anything the build printed, so it
  works however little output the server still has - for the worktree the
  host's `factory build` actually made (there is exactly one non-pool `*.json`
  under the host's data directory; `Remote.HostRepo` is only the synced
  checkout). It rsyncs that worktree into this run's local one and runs
  `git add -A -N` there, so files the run added show up in `git diff HEAD`
  rather than as untracked. Only once the work is local does it tear the host down:
  the sidecar is deleted, the pool's state file removed, and `remote` cleared
  from the record, leaving an ordinary local run pointing at the pulled
  worktree. A failure before the work is local changes nothing - neither the
  sidecar nor the record - so a pull is always safe to run again.
- **`factory start` hands the factory a diff and a prompt.** The factory's
  input is a diff, a first pass at the change, and the prompt it is for, not
  where the work was done (`factory.Handoff`). Not every change starts as a
  prompt fired at the cloud: some developers work out a first pass with an
  agent first. Before it does anything, on a terminal it prints a line saying
  it is only setting up a worktree and branch for the change (starting from
  the diff, when `--diff` is given), that nothing is reviewed or pushed yet,
  and that `chunk factory build --continue` is what reviews the work, runs the
  checks, and, by default, opens the pull request — the same rule as
  `factory build`'s own message about `--json` and a non-terminal stderr
  applies here too. `chunk factory start "<prompt>"` makes a run's worktree and
  branch, `chunk/factory/<description>/<run id>`, from the developer's files as
  they are (uncommitted changes in the baseline, as for a build), and keeps the
  run's record. The diff is made one of two ways: in the worktree, with Claude
  Code, another agent or none, or elsewhere and passed with `--diff <file>` (or
  `-` for stdin, with the prompt as an argument), which applies it to the
  developer's files in the worktree. Other ways, such as a TUI that iterates
  with the developer, hand over the same diff and prompt. Nothing runs on a
  sidecar. `chunk factory build --continue <run id>` hands the diff, whatever
  changed in the worktree since the baseline, committed or not, to the factory
  to refine against the prompt. Its implementer is told the diff is the
  developer's first pass and to finish it into something ready to merge. The
  continued run says how big the diff is and refuses to start if there is
  none. The record carries what the refining run needs to know as data,
  `brief` (how its implementer is told about the work) and `needs_work`,
  rather than where the work came from, so the factory does not change when a
  new way to make a first pass does. Only the worktree's path goes to stdout,
  so `cd "$(chunk factory start ...)"` lands in it; `--json` prints the record
  instead.
- **`factory plan` makes a plan with Claude, then sends it to the factory.**
  It opens a two-pane screen: a chat with Claude Code on this machine on the
  left, and the plan Claude writes on the right, rendered as markdown. Each
  message is a turn of one `claude -p --resume` session that reads the code
  and keeps the plan, in factory-bot's format, in a file in the project's data
  directory rather than the working tree. The plan file is the only thing it
  writes; its shell is an allowlist of read-only git and `gh` commands (`git
  fetch origin`, `log`, `diff`, `show`, `status`, `rev-parse`, `ls-files`,
  `ls-tree`, the listing forms of `branch`, and `gh pr view|diff|list|checks`),
  so it can look at a collaborator's branch or pull request while planning.
  Anything else is denied, including the flags that make those commands write a
  file or run a program, and the permission rules in the project's and your own
  Claude Code settings do not apply to it.
  `tab` moves to the plan, where `c` writes a comment right under the
  highlighted part (a heading, paragraph, list item or code block); `enter`
  saves it and `shift+enter`/`ctrl+j` adds a line. `shift+↑/↓` (or `K`/`J`)
  selects a run of parts for one comment, which hangs under the last of them; a
  selection that touches a comment takes in all of it, and its text, and `esc`
  clears a selection. A comment on a part Claude has since rewritten is listed
  after the plan, naming the part it was on, where it can still be edited or
  deleted. Comments go with the next message, each quoting the markdown it is
  on. `ctrl+s` sends the plan to the local daemon as `chunk factory build <plan
  file>` would; a plan not sent is kept, and its path printed. It checks what
  `factory build` checks before it opens. The same screen opens from `chunk
  watch` with `p`.
- **`factory bootstrap` sets a project up for `chunk factory`.** It plans
  three parts, prints the plan, and leaves out each part already done, so a
  rerun finishes a setup that stopped partway; with nothing left to do it fails
  as it does for existing prompts. Without `.chunk/config.json` it detects the
  VCS, the validation commands and the org and writes the config, as
  `chunk init` does, but without init's hooks, skills and completions, which
  are for `chunk validate`. Without `validation.sidecarImage` (and unless
  `--skip-sidecar`), it does what `sidecar setup` and then `sidecar snapshot
  create` do, on a sidecar of its own rather than the active one: it starts the
  sidecar from `cimg-base:2026.09-claude` (or `--image`) rather than the
  provisioner's default `cimg-base`, which has no Claude Code, since
  `chunk factory` runs its implementer and reviewers as Claude Code on sidecars
  made from the snapshot. It syncs the project to `/home/user/<repo>`, runs the
  environment's setup steps, checks that `claude` is on the sidecar's `PATH`
  (with `~/.local/bin`, as every Claude run does) and stops without a snapshot
  when it is not, then snapshots the sidecar as `<repo>-<YYYY-MM-DD>-<HHMMSS>` (`--snapshot-name` picks
  another; a name the org already has is explained rather than left as a bare
  409, and is looked for before the sidecar is made), records the snapshot as
  `validation.sidecarImage`, marks the install and gate commands remote, and
  deletes the sidecar.
  An environment with no setup steps gets no sidecar or snapshot: it warns and
  the default image stays. `--json` reports the snapshot as `sidecar_image`.
  The sidecar image is set up first, its setup steps' output shown on stderr
  as they run, and the review prompts are written after it. A sidecar image
  that fails does not stop the prompts: it is warned of, the prompts are
  written, and the command then fails with it, so a rerun has only the image
  left to do. An image without Claude Code, or an interrupted run, stops the
  prompts too, since they would fail the same way. With `--json`, what one part
  made is reported even when the other fails.
  Existing review prompts in `.chunk/reviews` are left out of the run; in an
  `--output` directory they are an error. A prompts directory that cannot be read is
  an error rather than taken for one with prompts. The plan is worked out from
  local files and flags alone, so it is shown before any login prompt: without
  a GitHub token it says up front that the team's review comments are left out.
  A Claude credential is required even when the prompts exist, since
  `chunk factory build` runs Claude on every change. The review prompts part runs
  Claude Code once, read-only, on a sidecar of its own (pool
  `factory-bootstrap`, destroyed when it ends). The sidecar runs the system image
  `cimg-base:2026.09-claude`, not the project's `validation.sidecarImage`:
  bootstrap needs Claude Code rather than the project's toolchain, and a project
  image may not have it. `--image` overrides it. Claude Code reads what the project
  says about itself: AGENTS.md, CLAUDE.md, docs, CI and linter configuration,
  and the code and tests. With a GitHub token and a GitHub `origin`, it also
  fetches the review comments the repository's most active reviewers (`--top`,
  default 5) left on its pull requests since `--since` (default 90 days ago),
  keeping each one's 200 newest. That happens before any sidecar starts, so a
  failed fetch costs nothing; it fails the command, with `--no-history` as the
  way out. No token is not an error: bootstrap warns and goes on from the code
  alone. Bootstrap needs a GitHub `origin` either way, since the sidecar's copy
  of the repository is named from it; without one it fails before any sidecar
  starts. The comments go to the sidecar as
  `.chunk/review-history.md` in its copy of the repository, over SSH rather than
  in the request, so their size is not bounded by the request's, and Claude is
  told to read them as data and make recurring pushback into rules. `--json`
  reports how many were used as `review_comments`. The
  project's validation commands are named in the request so the reviews leave
  to them what they already enforce. While it reads, each tool call it
  makes (the file it reads, the pattern it searches for) is shown as it happens,
  and the run's cost is shown when it ends. The answer comes back against a JSON
  schema; prompts with a name that is not lowercase words joined by hyphens, a
  repeated name, an empty or oversized body, or past the sixth are left out and
  counted. It never overwrites: a target directory that already has `.md` or
  `.txt` prompts is refused before any sidecar starts, and each file is created
  exclusively. `--output` writes elsewhere. `factory` with nothing to check
  points at it. `--template <name>` writes a standard set embedded in the binary
  instead (`generic`, `go-service`, `node-service`, `python`, `react`, `rust`),
  for a new project with little for Claude to read: no sidecar starts, no
  credential is needed, and the same overwrite refusal applies. An unknown name
  is refused with the list of sets. It cannot be combined with `--org-id`,
  `--image`, `--model`, `--timeout` or `--eval-commits`.
- **`factory bootstrap --eval-commits <n>` scores the prompts it writes, and is
  hidden** while `factory eval` is. Once the prompts are written, it runs
  `factory eval --commits <n>` on them: the `n` most recent usable commits on
  `HEAD` are made into cases before any sidecar starts, so a history with none
  fails then, and the cases are reviewed and graded on `factory-eval` sidecars
  from the project's `validation.sidecarImage`, not bootstrap's Claude image,
  so they review as factory would. Scoring runs after both of bootstrap's
  parts, so it uses an image the run has just recorded, and bootstrap's own
  sidecar is destroyed first. It is only for the prompts the run writes: when
  the prompts already exist, or `--skip-sidecar` leaves no image to score on,
  the plan says scoring is skipped, and `factory eval --commits <n>` scores
  existing prompts. A run whose sidecar image or prompts part fails is not
  scored.
  Reviews and the grader use Claude Code's default model and timeouts. The
  prompts are kept whatever the score: an eval that fails or scores no case is
  a warning, and the command still succeeds. The summary follows the list of
  prompts; `--json` reports it as `eval`, in `factory eval`'s report format.
  With history, the prompts come from comments on the same recent changes the
  commits are, so the scores flatter them.
- **`factory eval` scores review prompts, and is hidden.** It is for tuning
  prompts and what writes them, such as bootstrap, not for everyday use. It
  reviews a set of cases, each a change to the repository at a pinned revision
  and the request it answers. `--commits <n>` makes cases of the `n` most recent
  commits on the first-parent history of `--from`: each commit's diff against its
  parent, with its message as the request. Merges, bots' commits (an author
  ending in `[bot]`), commits that change only documentation or nothing, and
  diffs over 48 KB are skipped, and at most ten times `n` commits are looked at.
  `--suite <dir>` reads the cases from a `suite.json` instead: each a patch at a
  `revision` and its `request`, optionally labeled with the problems a review
  should find (`expect`: a `file`, optional `lines` as `[first, last]`, and
  `about`) or as `clean`, but not both. Every case's revision and patch are
  checked against the repository before any sidecar starts. Each case is put on
  every sidecar of its own pool (`factory-eval`, destroyed when it ends) as
  factory leaves a reviewer: the revision's tree committed as the baseline, the
  patch uncommitted on top. The prompts are sent as factory's first round sends
  them, with no implementer. Only high and medium findings count.
- **A grader judges every case, so cases need no labels.** After a case's
  reviews, a Claude run of its own on the first sidecar, where the case is still
  the uncommitted work, is given the request, factory's blocking rubric and the
  blocking findings, sorted by place and without the prompts that reported them.
  It may read the code and run commands, such as a test, to check them. It gives
  each finding a verdict, `confirmed` (a real problem that clears the bar),
  `not_blocking` (a real point that does not) or `wrong`, marks findings about a
  problem an earlier one reported as duplicates, and lists high and medium
  problems no finding is about as missed. Literal requested behavior overrides
  the reviewers: asking for broader or contrary behavior is `wrong`. Graded
  precision is confirmed problems over problems with a verdict, a duplicate
  counting once at its first finding's verdict; coverage is confirmed problems
  over those plus the missed ones, so it is relative to what the grader finds. A
  grading that fails leaves its case ungraded and the eval goes on; a missing
  Claude Code or a rejected credential stops it. `--no-grade` skips the grader,
  which leaves `--commits` with nothing to score, so the two cannot be combined.
- **Labeled cases also score the reviews, and check the grader.** Findings are
  grouped into the problems they are about, so a problem several prompts report
  counts once. A finding is about an expected problem when it names the same
  file and, if the problem has lines, a line within three of them; each expected
  problem takes a finding of its own first. Other findings are about the same
  problem as an earlier one in the same file within three lines, or about the
  file as a whole. Where a run is graded, its problems are regrouped by the
  grader's judgment, which reads the findings where nearby lines only place
  them: an expected problem keeps the finding paired with it and gains those the
  grader judged about the same problem, and the grader's other groups are
  problems the case does not list. Which expected problems were caught does not
  change. Recall is the share of expected problems caught. Problems a
  labeled case does not list are unexpected: on a clean case they would send
  sound work back, and on another they may be real, so the share of reported
  problems the labels list is a floor. Unlabeled cases count towards overlap but
  not towards these. Where a labeled case is graded, the summary's
  `graded.calibration` says how the grader agreed: findings on expected problems
  it confirmed, findings on clean cases it confirmed, misses it listed on clean
  cases, and expected problems the reviews missed that it found. Each prompt's
  row counts every problem it reported, how many of those another prompt
  reported too, and its findings by verdict. A case whose setup or any review
  fails is reported and left out of the totals rather than scored on the reviews
  that ran. The command exits non-zero when no case could be scored; a run in
  which only some cases errored still succeeds, and `summary.errored` counts
  them.
- **`watch` is only about runs.** The left pane lists factory runs, live ones
  first, then up to twenty that ended in the last day, and `↑/↓` moves through
  them. The first poll selects the newest run. Sidecars have no rows: the
  dashboard is for watching the work, not the infrastructure it borrows, so a
  sidecar's sync, validate and resource activity is not shown at all. With no run
  to list, the pane says where runs come from rather than sitting empty. `/`
  searches the run list: type to narrow it
  to the runs whose title, project, branch or status contain every word typed
  (`Enter` keeps the filter, `Esc` drops it, and `Esc` again quits). `n` starts
  a change without leaving the dashboard: it opens a prompt in the footer,
  naming the project it is for, and `Enter` has Claude Code draft it on this
  machine, in a factory run's worktree, as `chunk factory start` does with a
  draft made in it (`claude -p --model sonnet` with the developer's own
  login, edits accepted, anything else left to their own Claude Code permissions, chunk's
  hooks off). The right pane logs what Claude does as it drafts, and `Esc`
  stops it, keeping what it did. The draft is then shown in the right pane at
  once, with no key to press: what Claude said it did, the per-file summary and
  the whole diff, coloured, scrolled with `↑/↓`; `Enter`
  sends the diff and the prompt to the factory, as `chunk factory build
  --continue <run> --model sonnet "Finish the draft…"` would: the guidance
  starts the implementer on the draft at once instead of after a round of
  checks on it, and Sonnet is quicker than Claude Code's default for the
  implementer and reviewers, with the factory's checks still deciding when it
  is done. The factory's sidecars are started while Claude drafts, from the
  files the draft started from, so the run starts on them at once rather than
  waiting about a minute for its own, and the project's checks are run once on
  the implementer's sidecar meanwhile, so the run's own checks start with warm
  caches; sidecars never used are deleted. The
  factory run is selected once the next poll lists it; `Esc` keeps the draft
  for later and says how to send it. A draft with no changes can only be
  closed. The draft is in the project the dashboard was opened in, or the
  selected run's when it was opened outside one. What build checks first (an
  `origin` remote, validation commands or review prompts, Claude Code being
  installed) is checked before Claude starts, and why there is no draft, or
  why it could not be sent, is shown in the footer. Closing the dashboard
  stops a draft still being made. `p` opens the `chunk factory plan` screen
  in the same project, to plan the change with Claude before the factory
  builds it. `esc` (or `ctrl+c` while Claude works) goes back to the runs with
  the plan kept, and Claude carrying on; `p` opens it again as it was. A sent
  plan's run is selected once a poll lists it, and a plan not sent when the
  dashboard closes is kept, and its path printed. `n` and `p` are not offered
  against a remote daemon, since a factory run works from files on this
  machine. A selected
  run's pane reads top to bottom: the prompt, a status line that says what the run is doing
  ("Round 2 of up to 3 · implementing", or "Round 1 of 1" with
  `--max-attempts 1`) or, once it
  ended, how it ended, what blocked it and what it cost, then each round, then
  where the work is (branch, whether it was committed), the committed change
  itself under that (read from the run's branch on this machine and coloured, as
  much as fits, then `git diff <baseline> <branch>` for the rest) and, for a run that
  stopped with committed work still failing, the `--continue` command that picks
  it up. The title, status and outcome stay in place while the rounds scroll.
  One round starts open: the latest, or on a run that ended failing, the one
  that failed. The others are a line each (how they came out, findings,
  duration). `Space` expands or collapses whatever has the cursor — the task, a
  round, details, or a row inside a round — and the footer names what it will
  do there, or names nothing on a row with nothing to fold; a collapsed
  block's header sums up what it holds, and `Enter` does the same on a row
  with nothing else to open. During a run's first round, a few lines under the status say
  what a round is, what the cap means and how to change it with
  `--max-attempts`; after that, `?` shows or hides them. An open round shows the implementer's turn (duration, cost, diff
  stat), then its validation commands
  and reviews under `validate`. Where the run put its work and what it said while
  setting up (worktree, setup log, setup progress) are behind a `details` row
  under the rounds, which `Space` or `Enter` opens; whether it is open is kept
  per run, like the task and the rounds. A factory review is a check: it passes unless it
  found something worth changing (high or medium severity), so its row shows ✓ with any lesser
  issues noted ("2 issues, none worth changing"), or ✗ with how many of its issues are
  worth changing ("3 issues, 1 worth changing"). A line under the rounds says what
  is happening and whether there is anything to do ("Now: the implementer is making the
  change. Nothing to do yet.", or, once the run ended, whether the checks passed). `→` moves into
  the pane and `↑/↓` picks a row. A row inside a round that has something behind
  it carries a `▸`, and `Space` folds that open under the row, up to 15 lines:
  the implementer's summary and diff stat, a failed check's kept output (its
  end), or a finished review's findings, fetched the first time it is opened.
  `Enter` opens the same in a full-screen view to read all of it, and is the
  way to follow a review that is still running (on a round, it opens or closes
  the round). Once the run has ended, its `Branch:` row takes the cursor,
  and so does its `Pull request:` row once the pull request is open (a pull
  request that is still being pushed, failed or was skipped has no link to act
  on, so it cannot be selected). `c` copies what has the cursor, the branch
  name or the full URL, and says `copied` under the row for a few seconds. `b`
  and `u` copy the branch name and the pull request's URL from any row of the
  run, with no need to move the cursor first. On
  the pull request row `Enter` or `o` opens it in the default browser
  (`o` works from any row of the run), and so does `Cmd+Enter` in terminals
  that report it (kitty keyboard protocol: kitty, Ghostty, WezTerm, iTerm2 when
  enabled), which the footer advertises only then. The row says `opened in your
  browser`, or `could not open a browser, URL copied` when there is none (over
  SSH, say) and the URL is copied instead. The URL is an OSC 8 hyperlink, so
  Cmd+click works in terminals that support it. The summary and a
  check's output are word-wrapped to the screen; a log keeps one line per line
  of output. The footer only lists keys that do something for what is
  selected, so `x` is gone once a run has ended. **`q` only detaches**: the run
  belongs to the daemon and carries on. The one key that stops it is `x`, and it
  needs a second `x` on the same run to confirm. `r` continues a finished run
  that has committed work short of passing, as `chunk factory build --continue
  <run id>` with no prompt does: the work is checked first and the implementer
  is sent what failed. It is one press, since the new run can be cancelled
  with `x`.
- `watch` requires a TTY — it exits with an error if stdout is not a terminal. In an editor's built-in terminal (VS Code and the editors built on it, such as Cursor, and JetBrains IDEs) it leaves the bottom two rows blank: those terminals can report a couple of rows more than their panel shows, which would hide the footer. It polls the daemon every 5 seconds. Use `j`/`k` or `↑`/`↓` to select a run, `q` or `Esc` to quit. By default it watches every project it knows about; pass `--focus` to watch only the current directory. Running `watch` in a project also registers that project so future runs find it. `--all` is deprecated — it is now the default.
- **Run results are read from disk, not sent to the daemon.** Every `validate` run
  writes its events to the project's event log and registers the project (a
  `project-root` breadcrumb in the same data directory) whether or not a daemon
  is running. The daemon discovers projects from those breadcrumbs and replays
  each log from the start. `watch` no longer draws those events — it lists runs
  — but the daemon still reads them, and `chunk sidecar logs` still reaches the
  output. (Remote command *output* is registered with
  the daemon live and buffered in memory, keyed by the same project root, so it
  is only listed for a project that has been registered.)
- **`watch` can show a command's output.** `Enter` on a review or check row in
  the run pane opens a scrollback view of the output the daemon holds for it. A command that is still running tails live — the pane polls every 200 ms
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
  - Buffers are in memory only, so restarting the daemon loses them. `Enter` on a
    row whose output the daemon no longer knows about says so.
  - Output streaming needs a CircleCI token. Without one the dashboard still works
    and shows a one-line hint in the footer; the daemon never prompts.
- **`chunk sidecar logs <command-id>`** is the non-TUI door onto the same output,
  for scripts and for agents with no terminal. Command IDs are the ones the chunk
  daemon records for the commands it is buffering, and the ones a factory run
  records for its reviews. It reads the chunk daemon's buffer when the daemon has the command
  and falls back to streaming from the API when it does not, so it works either
  way. `-f`/`--follow` keeps printing until the command exits.
  - Output goes to stdout and nothing else does, so it can be piped. A non-zero
    remote status is reported on **stderr** as `exit status N`.
  - `logs` itself exits non-zero only when *reading* failed. A failing command is
    not a failing read — conflating the two would make the exit status useless to
    a caller.
- `chunk init` uses Claude to auto-detect the test command for the project.
  It generates `.claude/settings.json` with pre-commit hooks. It never touches
  CircleCI — tokens are prompted inline only when a command actually needs them.
- `sidecar sync` mirrors the working tree to the sidecar with rsync, leaving
  out whatever git ignores and keeping ignored files already on the sidecar
  (such as a venv it built). The branch does not need to be pushed to GitHub. Pooled
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
- `chunk hook disable` creates a `hooks-disabled` sentinel file in the project data directory (`~/.local/share/chunk/<hash>/`) inspected by the `chunk validate` Stop hook; `hook enable` removes it, along with any `.chunk/hooks-disabled` left by older versions. Stop-hook validation is also disabled when `CHUNK_HOOKS_DISABLED` is set in the environment.
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
- The notice suggests `brew upgrade chunk` when the running binary resolves into
  a Homebrew prefix, and `chunk upgrade` otherwise. `chunk upgrade` refuses to
  replace a Homebrew-managed binary in place.
- The notice names both versions (`v0.7.180 → v0.7.186`) and links the new
  release's GitHub page, so the reason to upgrade is one click away. The `watch`
  footer keeps its one-line form: it has no room for a URL.

## Config keys

| Key | Scope | Description |
|-----|-------|-------------|
| `model` | user config (`~/.config/chunk/config.json`) | Claude model override |
| `telemetry` | user config (`~/.config/chunk/config.json`) | Usage telemetry, tied to your CircleCI user ID once authenticated (`true`/`false`, default: `true`) |
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
