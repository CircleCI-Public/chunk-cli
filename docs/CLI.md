# CLI Command Tree

Complete command reference for the `chunk` CLI.

## Command Tree

```
chunk
├── auth
│   ├── set <provider>               # Store credential (circleci | anthropic | github)
│   ├── status                      # Check authentication status (CircleCI, Anthropic, GitHub)
│   └── remove <provider>           # Remove stored credential (circleci | anthropic | github)
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
│   └── set <key> <value>           # Set a config value (keys: model, apiKey)
│
├── init                            # Initialize project configuration
│   --force                         # Overwrite existing config
│   --skip-hooks                    # Skip hook file generation
│   --skip-validate                 # Skip validate command detection
│   --skip-completions               # Skip shell completion installation
│   --project-dir <path>            # Project directory (defaults to cwd)
│
├── task
│   └── run                         # Trigger a task run
│       --definition <name|uuid>    # Definition name or UUID (required)
│       --prompt <text>             # Prompt text (required)
│       --branch <branch>           # Branch override
│       --new-branch                # Create a new branch
│       --no-pipeline-as-tool       # Disable pipeline-as-tool mode
│
├── skill
│   ├── install                     # Install all skills
│   └── list                        # List skills and install status
│
├── validate                        # Run validation commands
│   [name]                          # Optional: run a specific named command
│   --dry-run                       # Print commands without executing
│   --list                          # List all configured commands
│   --cmd <command>                 # Run an inline command
│   --save                          # Save --cmd to config
│   --remote                        # Run on the active sidecar
│   --sidecar-id <id>               # Remote execution in specific sidecar
│   --identity-file <path>          # SSH identity file for sidecar
│   --workdir <path>                # Working directory on sidecar
│   --project <path>                # Override project directory
│
├── sidecar
│   ├── list --org-id <id>          # List sidecars
│   ├── create                      # Create a sidecar
│   │   --org-id <id>               # Organization ID (required)
│   │   --name <name>               # Sidecar name (required)
│   │   --image <image>             # E2B template ID or container image
│   ├── use <id>                    # Set the active sidecar for this project
│   ├── current                     # Show the active sidecar
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
│   │   --env-file <path>           # Load env file (defaults to .env.local when flag is present)
│   ├── sync                        # Sync files to sidecar
│   │   --sidecar-id <id>           # Sidecar ID (defaults to active sidecar)
│   │   --identity-file <path>      # SSH identity file
│   │   --workdir <path>            # Destination path on sidecar (auto-detected when omitted)
│   │   --bundle                    # Sync via git bundle (no GitHub access required)
│   ├── env                         # Detect tech stack and print environment spec as JSON
│   │   --dir <path>                # Directory to analyse (default: .)
│   │   --no-save                   # Print only, do not save to .chunk/config.json
│   ├── build                       # Generate Dockerfile and build test image from env spec
│   │   --dir <path>                # Directory to write Dockerfile.test and build from
│   │   --tag <tag>                 # Image tag (e.g. myapp:latest)
│   ├── setup                       # Detect env, sync files, run install steps, snapshot
│   │   --dir <path>                # Directory to detect environment in (default: .)
│   │   --sidecar-id <id>           # Sidecar ID (defaults to active sidecar)
│   │   --org-id <id>               # Organization ID (used when creating a new sidecar)
│   │   --name <name>               # Sidecar name (used when creating a new sidecar)
│   │   --identity-file <path>      # SSH identity file
│   │   --snapshot-name <name>      # Snapshot name (defaults to <sidecar-name>-setup)
│   │   --skip-sync                 # Skip syncing files to the sidecar
│   │   --skip-snapshot             # Skip creating a snapshot after install
│   │   --force                     # Re-detect environment even if cached
│   └── snapshot
│       ├── create                  # Create a snapshot of a sidecar
│       │   --sidecar-id <id>       # Sidecar ID (defaults to active sidecar)
│       │   --name <name>           # Snapshot name (required)
│       └── get <snapshot-id>       # Get a snapshot by ID
│
├── completion
│   ├── install                     # Install zsh completion
│   └── uninstall                   # Remove zsh completion
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
- `config set` accepts only `model` and `apiKey` as keys.
- `chunk init` uses Claude to auto-detect the test command for the project.
- `sidecar sync --bundle` sends a full git bundle on first use, then incremental
  bundles (`<lastRef>..HEAD`) on subsequent syncs. The branch does not need to be
  pushed to GitHub. Set `bundleSync: true` in `.chunk/config.json` to use bundle
  sync by default without passing `--bundle` on every invocation; `sidecar setup`
  reads the same config key.
- `sidecar ssh -- <cmd>` forwards stdin when the process stdin is a pipe, enabling
  patterns like `cat bundle | chunk sidecar ssh -- git fetch ...`.
  It generates `.claude/settings.json` with pre-commit hooks. It never touches
  CircleCI — tokens are prompted inline only when a command actually needs them.
- Commands that require a CircleCI token (`task run`, `task config`, `sidecar *`,
  `validate --sidecar-id`) prompt for it inline at the point of need rather than
  failing with an error.
- `chunk auth set github` stores a GitHub token in the config file; previously
  only the `GITHUB_TOKEN` environment variable was supported.

## Flag Conventions

- Required flags use cobra's `MarkFlagRequired()`
- Comma-separated lists are split with `strings.Split(s, ",")`
- Dates use `YYYY-MM-DD` format, parsed with `time.Parse("2006-01-02", s)`
- Boolean toggles default to `false`
- Model flags fall back to config file values, then built-in defaults
