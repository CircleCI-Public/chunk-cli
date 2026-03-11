# AGENTS.md

`chunk` is a context generation CLI that mines PR review comments from GitHub, analyzes them with Claude, and outputs a markdown prompt file tuned to a team's review patterns. Generated context goes in `.chunk/context/` for AI coding agents to pick up automatically.

## Common Commands

```bash
bun run dev build-prompt --org myorg  # Run from source
bun run typecheck                     # Type check without building
bun run build                         # Build all platform binaries → dist/
bun test                              # Run all tests
bun test:unit                         # Unit tests only
bun test:e2e                          # E2E tests only
bun run lint                          # Biome lint check
bun run lint:fix                      # Auto-fix lint issues
bun run format                        # Format code
./install-local.sh                    # Build and install locally
```

## Documentation Map

Read these when working in the relevant area:

- **[docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)** — module layering, dependency rules, change routing, data flow, file structure, implementation details
- **[docs/CLI.md](docs/CLI.md)** — full command tree, flag conventions, behavior decisions
- **[packages/hook/AGENTS.md](packages/hook/AGENTS.md)** — read when working on hook package code

## Key Architectural Constraints

- Dependencies flow strictly downward: `commands/ → core/ → leaf modules`
- `commands/` are thin: no business logic, no spinners — delegate to `core/`
- `core/` splits orchestrators (UI + workflow) from step functions (pure logic, `.steps.ts` files)
- Leaf modules (`storage/`, `api/`, `review_prompt_mining/`, `ui/`, `utils/`, `skills/`) must not import from `commands/` or `core/`
- `config/` is a leaf with no imports from any `src/` module

## Code Conventions

- **TypeScript strict mode**: No implicit any, strict null checks
- **Async/await**: Prefer over raw promises
- **Early returns**: Reduce nesting depth
- **Const over let**: Immutability by default
- **Template literals**: For string interpolation
- **Destructuring**: Objects and arrays where readable
- **Error handling**: Typed error classes (NetworkError, AuthError, GitError) bubble to command layer

## Testing Rules

- **E2E over mocks**: Critical workflows use real git operations in temp directories
- **No Claude API mocking**: Tests requiring `ANTHROPIC_API_KEY` skip gracefully if missing
- **Fakes over mocks**: Use fake servers instead of mocking libraries
- **Test isolation**: Each test creates its own temp directory and cleans up
- Naming: `*.unit.test.ts` (pure functions), `*.e2e.test.ts` (end-to-end workflows)
