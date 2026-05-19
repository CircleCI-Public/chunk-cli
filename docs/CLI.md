# CLI Command Tree

Complete command reference for the `chunk` CLI.

## Command Tree

```
chunk
├── auth
│   ├── set <provider>               # Store credential (circleci | anthropic | github)
│   │   -f / --force                 # Overwrite existing credentials without confirmation
│   ├── status                      # Check authentication status (CircleCI, Anthropic, GitHub)
│   └── remove <provider>           # Remove stored credential (circleci | anthropic | github)
│       -f / --force                 # Skip confirmation prompt
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
│
├── config
│   ├── show                        # Display resolved configuration
│   │   --json                      # Output as JSON
│   └── set <key> <value>           # Set a config value (keys: model, orgID, validation.sidecarImage)
│
├── commands                        # List all available commands
│
├── init                            # Initialize project configuration
│   --force                         # Overwrite existing config
│   --skip-hooks                    # Skip hook file generation
│   --skip-validate                 # Skip validate command detection
│   --skip-completions               # Skip shell completion installation
│   --skip-skills                   # Skip agent skill installation
│   --skip-test-suites              # Skip CircleCI test-suites.yml generation (default: true)
│   --project-dir <path>            # Project directory (defaults to cwd)
│
├── task
│   ├── config                      # Set up .chunk/run.json for this repository
│   │   -f / --force                # Overwrite existing configuration without confirmation
│   └── run                         # Trigger a task run
│       --definition <name|uuid>    # Definition name or UUID (required)
│       --prompt <text>             # Prompt text (required)
│       --branch <branch>           # Branch override
│       --new-branch                # Create a new branch
│       --no-pipeline-as-tool       # Disable pipeline-as-tool mode
│       --json                      # Output as JSON
│
├── skill
│   ├── install                     # Install all skills
│   │   --json                      # Output as JSON
│   └── list                        # List skills and install status
│       --json                      # Output as JSON
│
├── validate                        # Run validation commands
│   [name]                          # Optional: run a specific named command
│   --dry-run                       # Print commands without executing
│   --list                          # List all configured commands
│   --json                          # Output --list results or errors as JSON
│   --cmd <command>                 # Run an inline command
│   --save                          # Save --cmd to config
│   --remote                        # Run on active sidecar, or create one if none is set
│   --sidecar-id <id>               # Remote execution in specific sidecar
│   --org-id <id>                   # Organization ID when creating a new sidecar
│   --identity-file <path>          # SSH identity file for sidecar
│   --workdir <path>                # Working directory on sidecar
│   --project <path>                # Override project directory
│
├── sidecar
│   ├── list --org-id <id>          # List sidecars
│   │   --all                       # List all sidecars in the org (requires org admin)
│   │   --json                      # Output as JSON
│   ├── create                      # Create a sidecar and set it active
│   │   --org-id <id>               # Organization ID (falls back to CIRCLECI_ORG_ID or picker)
│   │   --name <name>               # Sidecar name (auto-generated when omitted)
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
│   ├── ssh                         # SSH into sidecar
│   │   --sidecar-id <id>           # Sidecar ID (defaults to active sidecar)
│   │   --identity-file <path>      # SSH identity file
│   │   -e / --env KEY=VALUE        # Set env var in remote session (repeatable)
│   │   --env-file <path>           # Load env file (defaults to .env.local when flag is present)
│   ├── sync                        # Sync files to sidecar
│   │   --sidecar-id <id>           # Sidecar ID (defaults to active sidecar)
│   │   --identity-file <path>      # SSH identity file
│   │   --workdir <path>            # Destination path on sidecar (auto-detected when omitted)
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
│   └── snapshot
│       ├── create                  # Snapshot a sidecar, then delete the source sidecar
│       │   --sidecar-id <id>       # Sidecar ID (defaults to active sidecar)
│       │   --name <name>           # Snapshot name (required)
│       └── get <snapshot-id>       # Get a snapshot by ID
│           --json                  # Output as JSON
│
├── hook
│   --project <path>                # Override project directory
│   ├── disable                     # Disable chunk validate hooks
│   ├── enable                      # Re-enable chunk validate hooks
│   └── status                      # Show whether hooks are enabled or disabled
│
├── completion
│   ├── install                     # Install shell completion (bash or zsh)
│   └── uninstall                   # Remove shell completion
│
└── upgrade                         # Update to latest version
```

## Behavior Decisions

- `build-prompt` auto-detects org and repos from the git remote when flags
  are omitted. If `--org` is provided explicitly, `--repos` is required.
- `build-prompt --output` creates parent directories automatically.
- `build-prompt --since` defaults to 3 months before the current date.
- `task run` defaults to pipeline-as-tool mode; use `--no-pipeline-as-tool`
  to disable.
- `config set` accepts `model` in user config and `orgID` /
  `validation.sidecarImage` in project config.
- `chunk init` uses Claude to auto-detect the test command for the project.
  It generates `.claude/settings.json` with pre-commit hooks, installs the
  `chunk-sidecar` skill when an agent config directory is present, and skips
  `.circleci/test-suites.yml` generation by default. Pass
  `--skip-test-suites=false` to write built-in Go/pytest templates.
- Commands that require a CircleCI token (`task run`, `task config`, `sidecar *`,
  `validate --sidecar-id`) prompt for it inline at the point of need rather than
  failing with an error.
- `chunk auth set github` stores a GitHub token in the config file; previously
  only the `GITHUB_TOKEN` environment variable was supported.
- `sidecar list` lists the current user's sidecars by default. Use `--all` to
  list all sidecars in the org (requires org admin privileges).
- `sidecar setup` prepares the sidecar but does not create a snapshot. After
  verifying the environment, snapshot it explicitly with
  `chunk sidecar snapshot create --name <name>`.
- `hook disable` creates `.chunk/hooks-disabled`; Stop-hook invocations of
  `chunk validate` print a skip message and exit 1 while hooks are disabled.
- `validate:<name>` is rewritten to `validate <name>` before Cobra parses args.

## Flag Conventions

- Required flags use cobra's `MarkFlagRequired()`
- Comma-separated lists are split with `strings.Split(s, ",")`
- Dates use `YYYY-MM-DD` format, parsed with `time.Parse("2006-01-02", s)`
- Boolean toggles default to `false`
- Model flags fall back to config file values, then built-in defaults
