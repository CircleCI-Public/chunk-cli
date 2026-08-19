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
│   ├── set <provider>               # Store credential (circleci | anthropic | github)
│   ├── status                      # Check authentication status (CircleCI, Anthropic, GitHub)
│   └── remove <provider>           # Remove stored credential (circleci | anthropic | github)
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
├── validate                        # Run validation commands
│   [name]                          # Optional: run a specific named command
│   --dry-run                       # Print commands without executing
│   --list                          # List all configured commands
│   --json                          # Output as JSON (only applies with --list)
│   --cmd <command>                 # Run an inline command
│   --save                          # Save --cmd to config
│   --remote                        # Run on the active sidecar
│   --mark-remote                   # Mark [name] (or all commands) remote in config, then exit
│   --sidecar-id <id>               # Remote execution in specific sidecar
│   --org-id <id>                   # Organization ID (used when creating a new sidecar)
│   --identity-file <path>          # SSH identity file for sidecar
│   --workdir <path>                # Working directory on sidecar
│   --project <path>                # Override project directory
│   -e / --env KEY=VALUE            # Set env var in remote sidecar session (repeatable)
│   --env-file <path>               # Env file to load (default: .env.local; pass a path to override)
│   │
│   └── variants <variants-file>    # Run code variants on parallel throwaway sidecars
│       --name <command>            # Validate command to run (default: all remote commands)
│       --parallel <n>              # Max concurrent sidecars (default 5)
│       --timeout <seconds>         # Per-command timeout when the command sets none (0 for no limit)
│       --org-id <id>               # Organization ID
│       --image <id>                # Snapshot image ID (default: validation.sidecarImage)
│       --identity-file <path>      # SSH identity file
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
│   ├── add-ssh-key                 # Add SSH key to sidecar
│   │   --sidecar-id <id>           # Sidecar ID (defaults to active sidecar)
│   │   --public-key <key>          # SSH public key string
│   │   --public-key-file <path>    # Path to public key file
│   ├── ssh                         # SSH into sidecar (stdin forwarded when piped)
│   │   --sidecar-id <id>           # Sidecar ID (defaults to active sidecar)
│   │   --identity-file <path>      # SSH identity file
│   │   -e / --env KEY=VALUE        # Set env var in remote session (repeatable)
│   │   --env-file <path>           # Env file to load (default: .env.local; pass a path to override)
│   ├── sync                        # Sync files to sidecar
│   │   --sidecar-id <id>           # Sidecar ID (defaults to active sidecar)
│   │   --identity-file <path>      # SSH identity file
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
│   │   --identity-file <path>      # SSH identity file
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
├── watch [dir...]                  # Live TUI dashboard for active sidecars and recent activity
│   --all                           # Watch all known projects, not just the current directory
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
- `org create` is hidden from the default help output. It requires CircleCI authentication and creates a standalone org (not tied to a VCS provider), printing the org name, ID, and slug on success.
- `build-prompt` auto-detects org and repos from the git remote when flags
  are omitted. If `--org` is provided explicitly, `--repos` is required.
- `build-prompt --output` creates parent directories automatically.
- `build-prompt --since` defaults to 3 months before the current date.
- `build-prompt` does not write intermediate files by default. Pass `--debug` to write the raw details JSON, analysis markdown, and PR rankings CSV alongside the prompt — useful when diagnosing unexpected prompt output.
- `task run` defaults to pipeline-as-tool mode; use `--no-pipeline-as-tool`
  to disable.
- `config set` user keys: `model`, `telemetry`. Project keys (`.chunk/config.json`):
  `orgID`, `validation.sidecarImage`. Credentials use `chunk auth set`, not `config set`.
- `validate --mark-remote` sets `remote: true` on commands in `.chunk/config.json`
  and exits without running anything. With a `[name]` it marks that one command;
  without it every configured command **except `role: autofix`** ones, which it
  names as skipped — a formatter that runs on the sidecar rewrites files there and
  the edits never reach the local working tree. Naming an autofix command marks it
  anyway, for the caller who means it. Commands already marked are reported as no
  change. `chunk sidecar setup` marks install and gate commands automatically, so
  `--mark-remote` is for the rest: a sidecar set up by hand, or a command whose
  role does not qualify. Unmarking is still a hand edit of the config.
- Per-command `remote` routing only decides anything while
  `validation.sidecarImage` is unset. Once it is set, `validate` sends **every**
  command to the sidecar (`allRemote`), marked or not, exactly as `--remote` does.
  Since `sidecar snapshot create` is normally followed by recording that key, a
  project on a snapshot runs everything remotely and `remote: true` becomes a
  no-op.
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
- Telemetry is anonymous and opt-out. It's disabled by the
  `CHUNK_NO_TELEMETRY` / `NO_ANALYTICS` / `DO_NOT_TRACK` / `CI` environment
  variables (first match wins, in that order), or `chunk config set telemetry false`.
- **Org ID resolution** for `sidecar create`, `sidecar list`, and other sidecar
  subcommands that need an org (in order): `--org-id` flag → `CIRCLECI_ORG_ID`
  env var → `orgID` in `.chunk/config.json` → interactive org picker (TTY only).
  Non-interactive sessions (agents, CI) should set `orgID` in project config or
  pass `--org-id` / `CIRCLECI_ORG_ID`.
- `watch` requires a TTY — it exits with an error if stdout is not a terminal. It polls sidecar state every 5 seconds and keeps an in-memory window of the 300 most recent event log entries. Use `j`/`k` or `↑`/`↓` to select a sidecar, `q` or `Esc` to quit. Running `watch` in a project also registers that project so `--all` finds it in future runs.
- `chunk init` uses Claude to auto-detect the test command for the project.
  It generates `.claude/settings.json` with pre-commit hooks. It never touches
  CircleCI — tokens are prompted inline only when a command actually needs them.
- `sidecar sync` sends a full git bundle on first use, then incremental bundles
  (`<lastRef>..HEAD`) on subsequent syncs. The branch does not need to be pushed
  to GitHub. Pass `--checkout` to fall back to the git checkout/patch approach
  (requires the branch to be pushed).
- `sidecar ssh -- <cmd>` forwards stdin when the process stdin is a pipe, enabling
  patterns like `cat bundle | chunk sidecar ssh -- git fetch ...`.
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
- **`validate variants` sidecars are outside that scheme.** Each variant gets its
  own sidecar, and none of them are written to the active-sidecar file — parallel
  workers would race on it and leave the user's own session pointing at a sidecar
  about to be deleted. That also makes them invisible to the reaper above, so the
  command cleans up after itself instead: it deletes each sidecar as its variant
  finishes, catches SIGINT/SIGTERM so an interrupt still unwinds through those
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
- `chunk validate` caches successful runs in hook mode only, keyed by
  `.chunk/config.json`, the execution target, the HEAD SHA, and the contents of
  all changed files; a repeat hook invocation with nothing changed prints
  `skipped` instead of re-running. Manual runs, `--cmd` inline commands, and
  repos whose state cannot be hashed never cache. Entries expire after 7 days.
  See [HOOKS.md](HOOKS.md#result-caching).

## Config keys

| Key | Scope | Description |
|-----|-------|-------------|
| `model` | user config (`~/.config/chunk/config.json`) | Claude model override |
| `telemetry` | user config (`~/.config/chunk/config.json`) | Anonymous usage telemetry (`true`/`false`, default: `true`) |
| `orgID` | `.chunk/config.json` | CircleCI organization ID for sidecar subcommands |
| `validation.sidecarImage` | `.chunk/config.json` | Snapshot or image ID for sidecar bootstrap and validate (unset: a matching org snapshot is selected automatically) |

`chunk config show` displays resolved user credentials and, when run from a
project directory, the resolved `orgID` (env var takes precedence over project
config).

## Flag Conventions

- Required flags use cobra's `MarkFlagRequired()`
- Comma-separated lists are split with `strings.Split(s, ",")`
- Dates use `YYYY-MM-DD` format, parsed with `time.Parse("2006-01-02", s)`
- Boolean toggles default to `false`
- Model flags fall back to config file values, then built-in defaults
