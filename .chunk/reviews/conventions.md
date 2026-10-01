Review the changes on this branch against the conventions in AGENTS.md.

Check that dependencies flow downward (cmd/ -> internal/), that cmd/ stays a
thin wrapper, that errors are wrapped with context using %w, that code uses
early returns, and that names follow Go conventions without stuttering.
Report each violation with its file and line.
