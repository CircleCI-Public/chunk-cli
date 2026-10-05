Add a `--failed` flag to `chunk session list`. When set, show only sessions
whose state is `failed`; apply the same filtering to text and JSON output.
Keep the command's current output unchanged when the flag is absent. Add focused
tests for both output modes and document the flag in `docs/CLI.md`.
