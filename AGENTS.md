# AGENTS.md

`smelt` is a Go CLI (built with cobra) for inner loop validation and a local software factory. By default everything runs on the developer's machine, with Docker for factory runs. Its optional **CircleCI backend** adds **sidecars** — ephemeral Linux environments on CircleCI that a developer's working tree is synced to so quality checks (test, lint, format) run there instead of locally, catching environment-specific failures before they reach CI.

**CircleCI is opt-in.** smelt may contact a CircleCI-owned endpoint only when the user chose it: `factory.backend: circleci` in `.smelt/config.json` (`config.ProjectConfig.UsesCircleCI()` is the one place that asks), `--backend circleci`, or an explicitly CircleCI command (`auth`, `sidecar`, `task`, `org`, `prune`, ...). A CircleCI token on the machine is never consent. `internal/circleci` may be imported only by the packages listed in `.golangci.yml`, and `task check:circleci` fails on stray references to CircleCI's repositories, packages and endpoints.

It also provides `build-prompt`, a secondary tool that mines PR review comments from GitHub, analyzes them with Claude, and outputs a markdown prompt file tuned to a team's review patterns. Generated context goes in `.smelt/context/` for AI coding agents to pick up automatically.

**The name lives in `internal/brand`.** The command, project and app folders, `SMELT_` environment prefix (`brand.Env`), keychain and label prefixes, factory branch prefix, Dockerfile name and image tag prefix are all derived there. smelt never reads or writes the locations of CircleCI's own CLI, which may be installed on the same machine; `task check:names` fails on the old name outside the license attribution and the CircleCI API-contract strings (marked `// CircleCI API contract: do not rename`).

## Common Commands

```bash
task build              # Build binary → dist/smelt
task test               # Run all tests
task acceptance-test    # Run acceptance tests
task lint               # Run linters, shellcheck on install.sh, the CircleCI sweep (task check:circleci) and the name guard (task check:names)
task fmt                # Format code
```

## Documentation Map

Read these when working in the relevant area:

- **[docs/GETTING_STARTED.md](docs/GETTING_STARTED.md)** — user guide: auth, init, sidecar setup, dev loop, skills, build-prompt
- **[docs/SKILLS.md](docs/SKILLS.md)** — skills reference: installing and using agent skills
- **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** — module layering, dependency rules, data flow, environment variables
- **[docs/CLI.md](docs/CLI.md)** — complete command tree, flag conventions, behavior decisions
- **[install.sh](install.sh)** — the curl installer; `internal/installscript` tests it end to end, `internal/upgrade` shares its archive naming and checksums
- **[docs/HOOKS.md](docs/HOOKS.md)** — pre-commit hooks: runs fmt, lint, test before commits via `.claude/settings.json`

## Key Architectural Constraints

- Dependencies flow strictly downward: `cmd/ → internal/`
- `cmd/` contains cobra command definitions — thin wrappers that delegate to `internal/`
- Leaf packages must not import from `cmd/`

## Code Standards

These apply to every change, whoever writes it. On factory runs, the
`maintainability` review in `.smelt/reviews/` checks the change against them.

### Reuse before you write (DRY)

- Search for an existing helper before writing one. Shared building blocks:
  `gitexec.Runner` (git), `httpcl` (HTTP with retries), `iostream` (streams,
  status, `OutputFn`), `ui` (prompts), `filecache`, `hashutil`, and `userError`
  (user-facing errors in `cmd/`).
- Give each piece of knowledge one home: a constant, a type or a function.
  Don't repeat a literal that means the same thing everywhere (use
  `smeltd.BackendDocker`, not `"docker"`). Don't copy a block to tweak it:
  extract the shared part and pass in the difference.
- Extract when *logic* repeats, not when two lines merely look alike. A shared
  helper that needs a flag to choose between its callers' behaviour is the
  wrong abstraction; keep those cases separate.

### Abstractions

- Define interfaces where they're consumed, and keep them small. Add one only
  for a second implementation or a real test seam (`sidecar.Backend`,
  `review.Execer`). Accept interfaces, return concrete types.
- One responsibility per package, within the dependency rules above. `cmd/`
  parses flags, calls `internal/` and formats output. No business logic.
- Separate decisions from side effects. Put "what should happen" in a pure
  function (`decideInitAuth`, `factory.Feedback`) and keep I/O in a thin
  caller. Test pure functions with table-driven tests.

### Structs and signatures

- Use an options struct when a function needs more than about four parameters,
  or several of the same type (`factory.RunOptions`, `sidecar.PoolOptions`).
- No boolean parameters that switch behaviour (`run(x, true, false)`). Use a
  named type, an options-struct field, or two functions.
- Return a struct rather than three or more values.
- Make the zero value useful. Add a `New…` constructor only when an invariant
  needs one.
- Use pointer receivers for types that are large or mutated, and don't mix
  receiver kinds on one type.

### Concurrency

- Run independent I/O concurrently: sidecars, subprocesses, network calls.
  Keep ordered or trivial steps sequential. Concurrency has to earn its
  complexity.
- Reuse the patterns already here: a `sync.WaitGroup` with per-item errors
  joined by `errors.Join` (`Sidecars.onReviewers`), and a semaphore to bound
  fan-out (`Relay.Push`). Prefer `errgroup` when the group should stop at the
  first failure.
- Every goroutine has a way to exit. Every blocking call takes a
  `context.Context` and honours cancellation. Shared state is guarded, or owned
  by one goroutine.

### State and dependencies

- No mutable package-level state in production code. A package-level `var` is
  allowed only as a test seam (`mintClaudeToken`), and production code never
  reassigns it.
- Pass dependencies in (clients, runners, clocks) rather than reaching for
  globals.

### Readability

- Use early returns to keep nesting shallow. Keep functions short and
  single-purpose. `gocyclo` fails above 30; aim well below that.
- Comments say *why*, not *what*. Every exported identifier has a doc comment.
  No commented-out code, no dead code, and no TODO without a linked issue.
- Wrap errors with context: `fmt.Errorf("...: %w", err)`. User-facing errors go
  through `userError` in `cmd/`.
- Follow Go naming: short, unexported by default, no stuttering (`pipeline.ID`,
  not `pipeline.PipelineID`).

## Testing Rules

- **Integration over mocks**: Critical workflows use real git operations in temp directories
- **No Claude API mocking**: Tests requiring `ANTHROPIC_API_KEY` skip gracefully if missing
- **Fakes over mocks**: Use fake HTTP servers instead of mocking libraries
- **Test isolation**: Each test creates its own temp directory and cleans up
- **Race detector**: Always run tests with `-race`
- Acceptance tests live in `acceptance/`
