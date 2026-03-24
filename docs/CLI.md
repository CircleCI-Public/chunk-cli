# CLI Command Tree

Complete command reference for the `chunk` CLI.

## Command Tree

```
chunk
├── auth
│   ├── status                      # Check authentication status
│   └── logout                      # Clear stored API key
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
├── task
│   └── run                         # Trigger a task run
│       --definition <name|uuid>    # Definition name or UUID (required)
│       --prompt <text>             # Prompt text (required)
│       --branch <branch>           # Branch override
│       --new-branch                # Create a new branch
│       --no-pipeline-as-tool       # Disable pipeline-as-tool mode
│
├── skills
│   ├── install                     # Install all skills
│   └── list                        # List skills and install status
│
├── hook                            # AI coding agent lifecycle hooks
│   ├── repo init [dir]             # Initialize hook config in a repo
│   ├── setup [dir]                 # One-shot: env update + repo init
│   ├── env update                  # Write hook environment file
│   ├── scope activate              # Mark project as active
│   ├── scope deactivate            # Remove active marker
│   ├── state save                  # Replace stored event state
│   ├── state append                # Append to stored event state
│   ├── state load [field]          # Output state as JSON
│   ├── state clear                 # Delete state file
│   ├── exec run <name>             # Run a command, save sentinel
│   ├── exec check <name>           # Read sentinel, enforce result
│   ├── task check <name>           # Check task result from subagent
│   └── sync check <specs...>       # Run grouped sequential checks
│
├── validate                        # Run validation commands
│   --sandbox-id <id>               # Remote execution in sandbox
│   --org-id <id>                   # Organization ID (required with sandbox-id)
│   --dry-run                       # Print commands without executing
│   └── init                        # Initialize validation config
│       --profile <lang>            # Language profile (node/python/go/ruby/java/rust)
│       --force                     # Overwrite existing config
│       --skip-env                  # Skip environment setup
│
├── sandboxes
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
│   ├── ssh                         # SSH into sandbox (not yet implemented)
│   ├── sync                        # Sync files to sandbox (not yet implemented)
│   └── prepare                     # Prepare sandbox env (not yet implemented)
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
- `validate init` uses Claude to auto-detect the test command for the project.
- Hook commands are detailed in **[docs/HOOKS.md](HOOKS.md)**.

## Flag Conventions

- Required flags use cobra's `MarkFlagRequired()`
- Comma-separated lists are split with `strings.Split(s, ",")`
- Dates use `YYYY-MM-DD` format, parsed with `time.Parse("2006-01-02", s)`
- Boolean toggles default to `false`
- Model flags fall back to config file values, then built-in defaults
