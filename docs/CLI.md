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
│   --skip-hooks                    # Skip git pre-commit hook installation
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
├── factory                         # Plan changes and build them in factory
│   ├── plan [prompt]               # Plan a change with Claude, then approve it and build it on the daemon
│   │   --model <model>             # Model Claude plans with (default: your Claude Code default)
│   └── bootstrap                   # Set up this project for chunk factory: config and sidecar image
│       --skip-sidecar              # Leave out setting up and recording the sidecar image
│       --snapshot-name <name>      # Name the sidecar image's snapshot (default: <repo>-<YYYY-MM-DD>-<HHMMSS>)
│       --org-id <id>               # Organization ID
│       --image <id>                # Image the sidecar starts from (default: cimg-base:2026.09-claude)
│       --json                      # Output as JSON
│
├── watch                           # Plan and follow your factory flows
│
├── conflicts                       # Report whether this branch still merges cleanly into its merge target
│   --project <path>                # Override project directory
│   --hook                          # Emit Claude Code hook JSON so the notice reaches the agent
│   --json                          # Print the raw conflict report as JSON
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
- **Keys in `chunk watch` and `chunk factory plan` follow one set of rules**, so
  a key means the same thing on both screens. `↑↓←→` and `j k h l` move,
  `g`/`G` and `home`/`end` go to the top and bottom, `pgup`/`pgdn` page and
  `ctrl+u`/`ctrl+d` page by half. `enter` goes in or confirms and `esc` goes
  back one level, cancelling a comment or prompt first when there is one, and
  leaves from the top; where leaving would drop unsent
  comments or a request it asks first. `q` quits wherever no text is being
  typed and `ctrl+c` always quits. `ctrl+o` opens a chat from any
  screen. A lowercase key is the common action and its
  capital the bigger one (`n` and `N` go to the next and previous comment). Plain letters are never actions while a text
  box has focus. Footer hints read `key action`, in lowercase.
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
  immediately. It is deliberately incapable of failing: run from an agent's
  commit hook, a non-zero exit would block the commit, and a merge conflict
  with the default branch is information rather than a reason to stop. With no
  daemon running, `--hook` prints nothing at all —
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
- **`watch` works with this machine's daemon only.** Flows and plans are
  planned and built from this machine, so with `CHUNK_DAEMON_REMOTE_ADDR` set
  `watch` refuses to start (`watch.remote_unsupported`), before it checks for a
  terminal, and says to unset the variable; the variable keeps its meaning for
  the other daemon clients. The header turns red with `unreachable` when the
  daemon stops answering, and the daemon is started again.
- **`watch` takes no directories.** Flows are the signed-in user's wherever it
  is run, and new plans are made in the project it was opened in, which it
  registers. An argument exits 2. `--focus` and `--all` still parse, deprecated,
  and do nothing.
- **`chunk watch` is the recommended way to follow factory work.** It shows
  your flows in factory and opens their plans. So on a terminal chunk points
  there when it helps: once an approved plan starts building, after
  `chunk init`, and in bare `chunk` help. These pointers are left out with
  `--json`, in CI (`CI` set), and when stdout or stderr is not a terminal (a
  log file, a pipe), so none of those changes anything already being parsed.
  Chunk never opens `watch` for you.
- **`factory plan` makes a plan with a factory planning session, then
  approves it in factory and builds it.** It opens a flow screen. A two-row
  strip under the header shows the plan's step (`Planning › Building › Integrating › Done`), what is
  happening now (the factory task and its unit) and what comes next
  (`Next: you approve the plan · ctrl+s`, `Next: chunk: build-unit`,
  `Waiting on <blocker>`). The left pane is the planning session's activity.
  The right has two tabs:
  **Plan**, the plan rendered as markdown, and **Flow**, which shows progress
  (each unit ✓ done, ▸ being built, ○ to do, or for a plan with sub-plans its
  open sub-plans), the pull request and its deviations, and related plans
  (parent, sub-plans, blockers). With the right pane focused, `←` and `→`
  switch tabs. The screen opens on Plan while the plan is being planned and on
  Flow once it is building. The left pane is always titled Activity: for a
  new plan it holds the request input until the plan is created, then it is
  read-only activity. Once the plan is being built, the activity pane also
  shows the build session's progress from this machine's chunk daemon, under
  a Building divider: each tool call, reply and note, including a warning
  when another session holds the plan's claim. The Flow tab's first line
  says whether `R` can resume the plan. When it can't, the line says why.
  For example, it names the session that holds the claim and the
  `factory daemon release` command that frees it, or a blocker that isn't
  Done. In `chunk watch`, `R`
  resumes a planner or starts a fresh build session in the chunk daemon, in
  the project `chunk watch` was opened in, and the flow list offers the same
  `R` for the selected flow. The dashboard's header says what `R` does there
  or why it can't, where it otherwise names the daemon; a daemon it can't
  reach is shown instead. On a screen opened by `chunk factory plan`, `R`
  resumes only a planner; resume a build from `chunk watch`. chunk never
  resumes a build on its own. The request you type for a new plan (or the one passed as
  `chunk factory plan "<request>"`) creates it
  in factory, named by Claude for what the request asks for, as a 2-3 word
  slug (`dry-run-flag`), or for a slug of its first line (`add-a-dry-run-flag`)
  when Claude cannot name it,
  and has the chunk daemon start one long-lived Claude Code session with
  factory's f plugin, which claims the plan and plans it through the factory
  daemon's tasks. The session runs in the chunk daemon, not in the screen:
  closing the screen leaves it planning, and any chunk window that opens the
  plan shows the same session's activity. A screen opened while it is working
  shows it working. The screen refreshes the plan's text, state and comments every 5s;
  the plan's state is in the status strip. Nothing typed after the request
  reaches the session; it works through factory's tasks. The activity pane
  names the task each turn is on, as the plan's flow in factory names it,
  in sentence case (`Early plan` for `early-plan`), and the reply is marked
  with it (`chunk · Early plan`). It needs factory, the f plugin, and the
  factory daemon running with `FACTORY_PLAN_STORE=circleci` as the CircleCI
  user chunk is signed in as; it checks the daemon before it opens, and says in
  the activity pane if the session could not claim the plan. While the plan
  is in Planning (or Unprioritized, where factory starts a new plan) and
  factory has a task for a session no one is running, the screen resumes the
  session on its own and tells it to claim the plan again, with none of the
  developer's words; it tries once per flow state, and never for a plan past
  Planning. A new plan's request that could not start the session is kept,
  and sent in place of that claim. Closing the screen stops the session,
  which frees its claim. A plan opened again in a later screen picks up the
  conversation that planned it, rather than starting a blank one. One chunk
  at a time plans a plan: another that opens it can read it and comment, but
  does not start a second session.

  The session works in a worktree of the plan's own rather than the
  developer's checkout, since that is where the plan is built once it
  is approved. It starts from a fresh fetch of origin's default branch, so
  unpushed and uncommitted work in the checkout is not in it, but for an
  uncommitted `.chunk/config.json`, which is copied in. It is removed once
  the plan has ended, unless it has changes in it.

  `tab` moves to the plan, where `c` writes a comment right under the
  highlighted part (a heading, paragraph, list item or code block); `enter`
  queues it and `shift+enter`/`ctrl+j` adds a line. `shift+↑/↓` (or `K`/`J`)
  selects a run of parts for one comment, which hangs under the last of them;
  a selection that touches a comment takes in all of it, and its text, and
  `esc` clears a selection. Queued comments wait in the activity pane, marked
  `queued · press enter or s to send`. Sending (`enter` in the activity pane,
  or `s` in the plan) saves the queued comments
  to factory and hands them to the session to address. Comments are sent
  only when factory is ready to take them:
  while factory works on the plan they stay queued, and `s` is not offered. Once comments are handed off, the
  action bar turns purple and says the screen is waiting for factory, until a
  refresh finds the plan has moved; comments queued meanwhile are held, and
  after 15 seconds the bar says how long it has waited. While comments
  are saved they cannot be changed. A saved comment is edited at once with
  `c`, resolved with `r` and deleted with `d` (after a `y`); resolved ones are
  listed last. A comment on a part since rewritten is listed after the plan,
  naming the part it was on.

  Dragging the mouse over the text of the Activity pane, the plan or the Flow tab
  selects it and puts it on the clipboard when the button is let go, which
  the status bar says. The screen takes the mouse from the terminal, so the
  terminal's own selection does not work on it; this one does the same, a
  line at a time, and keeps to the text: the plan's cursor and comment bars
  and the frame of a message are not copied. Dragging past the top or bottom
  of the pane scrolls it, so more than is in view can be selected, and the
  wheel scrolls while the selection stays on its text. A key press or a click
  clears it, as does a change to the text it is on. It copies with the
  terminal's clipboard escape sequence, which many terminals block, and also
  with the machine's clipboard tool (`pbcopy`, `wl-copy`, `xclip`, `xsel` or
  `clip.exe`) when it has one and the session is not over ssh; over ssh it
  needs a terminal that allows the sequence (in tmux,
  `set -g set-clipboard on`).

  `e` edits the highlighted part, or the run of parts `shift+↑/↓` selected,
  in `$VISUAL` or `$EDITOR` (`vi` when neither is set), for a change that is
  quicker to make than to ask for. It works only while factory waits on the
  developer, the same time comments are sent; at any other time the status
  bar says so and no editor opens. If the plan changed while the editor was
  open, nothing is saved and the edit is kept in the temporary file the status
  names. A saved edit queues a comment on the edited part asking the planning
  session to re-read it, unless one is queued there already; it is sent with
  the other comments, and can be deleted before then. Comments on an edited part follow
  it, or are listed after the plan as for any part rewritten.

  `ctrl+s` approves the plan, after a `y`, once factory's review of it has
  passed and factory waits on you, when the status strip under the
  header reads `Next: you approve the plan · ctrl+s`; until then it reads
  `Now: factory is reviewing the plan`. A plan waits there after its design
  is written too: while factory waits for the design to be approved rather
  than the plan, the strip reads `Next: you approve the design · ctrl+s`, and
  `ctrl+s` approves the design, so the session writes the units, once its
  comments are sent and resolved; the action bar then waits for factory, as
  it does after a hand-off. While factory waits for the plan's 3. Open
  Questions to be answered, the strip reads
  `Next: you answer 3. Open Questions in comments`, and `ctrl+s` offers no
  approval until they are answered in comments and sent. Approving reads the plan as factory has it, not the screen's copy.

  A plan in Integrating - Human whose PR still conflicts with its base, after
  factory's own rebase of that head, reads PR conflicts in the header.
  `ctrl+s` then hands it back to factory's agent to rebase, after a `y`, by
  sending factory's `resolve-conflicts` event, and the action bar waits for
  factory to move it. While comments are queued on it, `ctrl+s` does not
  hand it back: the comments must be saved first, and saving them hands the
  plan to the agent too, which rebases it.

  A plan not yet started (Unprioritized) reads
  `Next: you start work · ctrl+s` in the strip, and `ctrl+s` starts it, after
  a `y`, by sending factory's `prioritize` event. The strip's Next follows
  factory's order: `resolve-conflicts` comes first when offered, and the
  `abandon` events factory offers in every live state are never Next. A plan
  with nothing to do next waits on its first blocker that is not Done.

  `x` abandons the plan, while factory offers `abandon` (in every live
  state): the strip asks `Abandon plan as: c canceled · w won't do ·
  d duplicate · esc keep it`, and the key picked sends factory's `abandon`
  event with that reason. Factory ends the plan in Canceled, Won't Do or
  Duplicate at its next poll. Any other key keeps the plan.

  `X` deletes the plan, after a `y`, while no factory session claims it:
  its text, comments and flow go, and the screen closes, back to where it
  was opened from. Factory refuses to delete a plan with sub-plans, and
  the strip says so.

  Approving a plan with units is one request to the chunk daemon, which
  approves it in factory, stops its planning session and starts its build, in
  a Claude Code session in the plan's worktree, through to a pull request.
  Closing chunk once the daemon has the request does not stop it. In `chunk watch`,
  the screen stays open after approval, on the Flow tab, to follow the build;
  the activity pane shows what the build session is doing.
  Its comments can no longer be sent, because the planning session has
  stopped. `enter` on a
  related plan in the Flow tab opens it. The standalone `chunk factory plan`
  closes on approval. The daemon frees the plan's claim from a session that
  is only planning it, a parent plan's included; while a build holds the claim,
  approving is refused and names its session. A build whose plan another
  session holds says so in `chunk watch`, and picks the plan up once the claim
  is released. Builds outlive the screen and the terminal, and any number go at
  once; the plan's Flow tab follows each, from factory. A build ends with its
  plan, so abandoning the plan (`x`) stops it at its next poll. A
  stopped one leaves the plan approved in factory, and `/f:resume <ID>` in
  Claude Code carries it on. A plan not approved stays in factory, and its ID
  is printed. Before it opens, the screen checks that the project has a git
  remote named `origin` and something to check a build with: validation
  commands, review prompts in `.chunk/reviews/`, or both.
- **`factory bootstrap` sets a project up for `chunk factory`.** It plans
  two parts, prints the plan, and leaves out each part already done, so a
  rerun finishes a setup that stopped partway; with nothing left to do it fails
  as already set up and points at `chunk factory plan`. Without
  `.chunk/config.json` it detects the VCS, the validation commands and the org
  and writes the config, as `chunk init` does, but without init's hooks, skills
  and completions, which are for `chunk validate`. Without
  `validation.sidecarImage` (and unless `--skip-sidecar`), it does what
  `sidecar setup` and then `sidecar snapshot create` do, on a sidecar of its
  own rather than the active one: it starts the sidecar from
  `cimg-base:2026.09-claude` rather than the provisioner's default
  `cimg-base`, since factory's agents run as Claude Code on sidecars made
  from the snapshot. `--image` may name any
  image instead; bootstrap does not check it for Claude Code. It syncs the
  project to `/home/user/<repo>`, runs the environment's setup steps, then
  snapshots the sidecar as `<repo>-<YYYY-MM-DD>-<HHMMSS>` (`--snapshot-name`
  picks another; a name the org already has is explained rather than left as a
  bare 409, and is looked for before the sidecar is made), records the
  snapshot as `validation.sidecarImage`, marks the install and gate commands
  remote, and deletes the sidecar. An environment with no setup steps gets no
  sidecar or snapshot: it warns and the default image stays. Its credential is
  a CircleCI token; it needs no Claude credential, GitHub token or GitHub
  `origin`. `--json` reports `config_written` and the snapshot as
  `sidecar_image` (empty when no image was recorded).
- **`watch` is Flows and the plans opened from it.** The dashboard opens on
  Flows: your open flows in factory, grouped by who they wait on, then by
  phase, with the selected flow's plan text and open comments previewed beside
  it. A sub-plan stays in its own group and is marked `↳` with its parent's
  title; one listed under the same heading as its parent (same group, same
  phase) is indented under it. Sub-plans that have ended are not shown. When
  chunk cannot read the plans' parents the list is shown flat, with `sub-plans
  unavailable` under it. `Enter` opens the `chunk factory plan` screen on the
  selected flow's plan, and `n` on a new one, to plan the change with Claude
  before it is built. `q` or `esc` quits. Opening a plan only reads it: no session claims it
  until you send a new plan's request, or the screen resumes a planner factory
  is waiting on, and then in the project the dashboard was opened
  in, since factory does not record a plan's repository; a plan another
  session holds is refused rather than taken over. `esc` or `ctrl+c` goes back
  to the plans with the plan and its session kept; `ctrl+p` does too, to
  switch to another plan. Each opened plan keeps its own activity and
  session. An approved plan's screen stays open on its Flow tab, which
  follows the build from factory. Quitting with a request or comments unsent
  in any plan asks for a second `ctrl+c`; plans and saved comments stay in
  factory, and each plan's session keeps planning in the chunk daemon, with
  its conversation, for the next time the plan is opened, but the activity
  shown and an unsent request are not kept. Closing the dashboard stops no
  planning session or build: both run in the chunk daemon.
- **`ctrl+o` opens a chat over any screen** of `watch`: a Claude Code session
  that answers questions about your plans and flows, from the plans, the code,
  Sourcegraph, git history and the factory daemon. It is told where it was
  opened: on a plan's screen, the plan and its status strip; on Flows, the open
  flows. Asked to, it acts on a flow with factory's own commands, without
  asking first: `factory daemon repair` and `release`, and factory's
  `task_request`, `plan_block` and `plan_resolve`. It runs nothing else that
  changes anything: it does not plan, claim, edit plan text or add comments;
  for those it says what to comment or which key to press. `esc` or `ctrl+o`
  goes back to the screen as it was. The chunk daemon runs one chat session
  per repository, so every `chunk watch` there shares its conversation, and
  quitting `chunk watch` does not end it. The daemon stops it after 30 idle
  minutes and resumes it on the next message. Outside a repository it says it
  is unavailable, and takes no input.
- `watch` requires a TTY — it exits with an error if stdout is not a terminal. In an editor's built-in terminal (VS Code and the editors built on it, such as Cursor, and JetBrains IDEs) it leaves the bottom two rows blank: those terminals can report a couple of rows more than their panel shows, which would hide the footer. It checks the daemon answers every 5 seconds. Running `watch` in a project also registers that project so the daemon finds it.
- **Run results are read from disk, not sent to the daemon.** Every `validate` run
  writes its events to the project's event log and registers the project (a
  `project-root` breadcrumb in the same data directory) whether or not a daemon
  is running. The daemon discovers projects from those breadcrumbs and replays
  each log from the start, and `chunk sidecar logs` reaches the output. (Remote
  command *output* is registered with the daemon live and buffered in memory,
  keyed by the same project root, so it is only listed for a project that has
  been registered.)
- **The daemon keeps a command's output.** Output is capped at 256 KiB per
  command, keeping the **tail** — the end of a failed run is the part worth
  reading. The daemon holds at most 20 commands per project, evicting the
  oldest *finished* one first; a running command is never evicted. Buffers are
  in memory only, so restarting the daemon loses them. Output streaming needs
  a CircleCI token; the daemon never prompts.
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
  It installs a git pre-commit hook (`.git/hooks/pre-commit`) that runs
  `chunk validate`, and writes no agent hook files. It never touches
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
  keyed by session and branch, where the session is `CLAUDE_CODE_SESSION_ID`
  (overridable with `CHUNK_SESSION_ID`). Every session always creates its own
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
- **`chunk validate` always runs its commands.** Whatever is on stdin, on a
  clean tree or a repeat run with nothing changed, it runs the configured
  commands and exits with their status. Nothing is cached between runs.
- **`validate --async` and `validate results` are two halves of one flow.**
  `--async` hands the run to the chunk daemon and returns immediately; `results`
  reads what finished and prints it to stdout. A background run is something
  you ask for explicitly, so reading its answer back is too. A result is reported once and
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
  renders the notice in the TUI header instead.
- The notice suggests `brew upgrade chunk` when the running binary resolves into
  a Homebrew prefix, and `chunk upgrade` otherwise. `chunk upgrade` refuses to
  replace a Homebrew-managed binary in place.
- The notice names both versions (`v0.7.180 → v0.7.186`) and links the new
  release's GitHub page, so the reason to upgrade is one click away. The `watch`
  header keeps a one-line form: it has no room for a URL.

## Config keys

| Key | Scope | Description |
|-----|-------|-------------|
| `model` | user config (`~/.config/chunk/config.json`) | Claude model override |
| `telemetry` | user config (`~/.config/chunk/config.json`) | Usage telemetry, tied to your CircleCI user ID once authenticated (`true`/`false`, default: `true`) |
| `notifications` | user config (`~/.config/chunk/config.json`) | OS desktop notification after validate completes (`true`/`false`, default: `false`) |
| `orgID` | `.chunk/config.json` | CircleCI organization ID for sidecar subcommands |
| `validation.sidecarImage` | `.chunk/config.json` | Snapshot or image ID for sidecar bootstrap and validate (unset: a matching org snapshot is selected automatically) |
| `asyncValidate` | `.chunk/config.json` | Whether the chunk daemon may run a validate request in the background: `auto` (default), `always`, `never` |
| `asyncValidateMaxLines` | `.chunk/config.json` | Largest change, in lines, still validated in the background under `auto` (default: 500) |
| `asyncValidateWorktree` | `.chunk/config.json` | Run background checks in a checked-out snapshot so edits cannot make the result stale (`true`/`false`, default: `false` — a snapshot holds nothing gitignored) |
| `asyncValidateInert` | `.chunk/config.json` | Extensions or file names this project also counts as prose (see below) |
| `asyncValidateBlocking` | `.chunk/config.json` | Extensions or file names that always block (see below) |

`asyncValidateInert` adds extensions (`.sql`) or file names (`NOTICE`) to the
built-in prose list (`.md`, `.markdown`, `.txt`, `.rst`, `.adoc`, `LICENSE`,
`NOTICE`, `AUTHORS`, `CHANGELOG`), which is validated in the background at any
size; it never replaces that list. `asyncValidateBlocking` names extensions or
file names that always block, over an inert default, the line threshold, and
`asyncValidate: always`; a repo that lints its markdown lists `.md` there.
Entries in either list are refused when the config loads if they are paths,
globs, or extensions without their dot. An extension matches whatever its case;
a file name must match exactly. Set these keys with `chunk config set`, which
validates the value before writing it; each `set` replaces that list.

`chunk config show` displays resolved user credentials and, when run from a
project directory, the resolved `orgID` (env var takes precedence over project
config).

## Flag Conventions

- Required flags use cobra's `MarkFlagRequired()`
- Comma-separated lists are split with `strings.Split(s, ",")`
- Dates use `YYYY-MM-DD` format, parsed with `time.Parse("2006-01-02", s)`
- Boolean toggles default to `false`
- Model flags fall back to config file values, then built-in defaults
