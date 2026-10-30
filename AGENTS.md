# AGENTS.md

`chunk` is a Go CLI (built with cobra) for inner loop validation. Its primary capability is **sidecars** — ephemeral Linux environments on CircleCI that a developer's working tree is synced to so quality checks (test, lint, format) run there instead of locally, catching environment-specific failures before they reach CI.

It also provides `factory plan`, which plans a change with Claude in factory; once approved, the chunk daemon builds it and checks it against the review prompts in `.chunk/reviews/` and the validation commands.
`factory bootstrap` sets a project up for it: it writes `.chunk/config.json` if there is none, and records a sidecar image with the project's environment (`validation.sidecarImage`).

## Common Commands

```bash
task build              # Build binary → dist/chunk
task test               # Run all tests
task acceptance-test    # Run acceptance tests
task lint               # Run linters
task fmt                # Format code
```

## Documentation Map

Read these when working in the relevant area:

- **[docs/GETTING_STARTED.md](docs/GETTING_STARTED.md)** — user guide: auth, init, sidecar setup, dev loop, skills
- **[docs/SKILLS.md](docs/SKILLS.md)** — skills reference: installing and using agent skills
- **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** — module layering, dependency rules, data flow, environment variables
- **[docs/CLI.md](docs/CLI.md)** — complete command tree, flag conventions, behavior decisions

## Key Architectural Constraints

- Dependencies flow strictly downward: `cmd/ → internal/`
- `cmd/` contains cobra command definitions — thin wrappers that delegate to `internal/`
- Leaf packages must not import from `cmd/`

## Code Conventions

- **Early returns**: Reduce nesting depth
- **Error handling**: Wrap errors with context using `fmt.Errorf("...: %w", err)`
- **Naming**: Follow Go conventions — short, unexported by default, no stuttering (e.g. `pipeline.ID` not `pipeline.PipelineID`)

## Testing Rules

- **Integration over mocks**: Critical workflows use real git operations in temp directories
- **No Claude API mocking**: Tests requiring `ANTHROPIC_API_KEY` skip gracefully if missing
- **Fakes over mocks**: Use fake HTTP servers instead of mocking libraries
- **Test isolation**: Each test creates its own temp directory and cleans up
- **Race detector**: Always run tests with `-race`
- Acceptance tests live in `acceptance/`
