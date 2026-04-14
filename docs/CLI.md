# CLI Command Tree

Complete command reference for the `chunk` CLI.

## Command Tree

```
chunk
├── auth
│   ├── set <provider>               # Store credential (circleci | anthropic)
│   ├── status                      # Check authentication status (CircleCI, Anthropic, GitHub)
│   └── remove <provider>           # Remove stored credential (circleci | anthropic)
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
│   --skip-circleci                 # Skip CircleCI org picker
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
│   --status                        # Check cache only, don't execute
│   --cmd <command>                 # Run an inline command
│   --save                          # Save --cmd to config
│   --force-run                     # Ignore cache, always run
│   --sandbox-id <id>               # Remote execution in sandbox
│   --identity-file <path>          # SSH identity file for sandbox
│   --workdir <path>                # Working directory on sandbox
│   --project <path>                # Override project directory
│
├── sandbox
│   ├── list --org-id <id>          # List sandboxes
│   ├── create                      # Create a sandbox
│   │   --org-id <id>               # Organization ID (required)
│   │   --name <name>               # Sandbox name (required)
│   │   --image <image>             # Container image
│   ├── exec                        # Execute command in sandbox
│   │   --org-id <id>               # Organization ID (required)
│   │   --sandbox-id <id>           # Sandbox ID (required)
│   │   --command <cmd>             # Command to run (required)
│   │   --args <args>               # Command arguments
│   ├── add-ssh-key                 # Add SSH key to sandbox
│   │   --org-id <id>               # Organization ID (required)
│   │   --sandbox-id <id>           # Sandbox ID (required)
│   │   --public-key <key>          # SSH public key string
│   │   --public-key-file <path>    # Path to public key file
│   ├── ssh                         # SSH into sandbox
│   │   --sandbox-id <id>           # Sandbox ID (required)
│   │   --identity-file <path>      # SSH identity file
│   │   --env-vars KEY=VALUE        # Set env var in remote session (repeatable)
│   │   --no-env-file               # Skip auto-loading .env.local
│   └── sync                        # Sync files to sandbox
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
  It generates `.claude/settings.json` with pre-commit hooks.

## Flag Conventions

- Required flags use cobra's `MarkFlagRequired()`
- Comma-separated lists are split with `strings.Split(s, ",")`
- Dates use `YYYY-MM-DD` format, parsed with `time.Parse("2006-01-02", s)`
- Boolean toggles default to `false`
- Model flags fall back to config file values, then built-in defaults
