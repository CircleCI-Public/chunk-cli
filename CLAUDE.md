# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`chunk` is a context generation CLI that mines real PR review comments from GitHub, analyzes them with Claude (via the Anthropic API), and outputs a markdown prompt file tuned to a team's review patterns. The generated context is designed to be used with AI coding agents (e.g., placed in `.chunk/context/` for Claude Code to pick up automatically).

## Common Commands

```bash
# Development
bun run dev build-prompt --org myorg  # Run from source with hot reload
bun run dev auth login                # Test auth command
bun run typecheck                     # Type check without building

# Building
bun run build                         # Build all platform binaries → dist/
bun run build:darwin-arm64            # Build single platform

# Testing
bun test                              # Run all tests
bun test src/__tests__/prompt.unit.test.ts  # Run specific test file
bun test --test-name-pattern="context"      # Run tests matching pattern
bun test:unit                         # Unit tests only
bun test:e2e                          # E2E tests only

# Code Quality
bun run lint                          # Check code with Biome
bun run lint:fix                      # Auto-fix linter issues
bun run format                        # Format code

# Installation
./install-local.sh                    # Build and install locally to ~/.local/bin
```

## Architecture Overview

### Data Flow: `chunk build-prompt`

1. **Entry Point** (`src/index.ts`): Command routing and top-level error handling
2. **Command Layer** (`src/commands/build-prompt.ts`): Parse args, validate inputs (org, repos, dates, models)
3. **Reviewer Discovery** (`src/review_prompt_mining/top-reviewers/`): Query GitHub GraphQL API for top reviewers by activity in the org
4. **Comment Fetching** (`src/review_prompt_mining/top-reviewers/review-fetcher.ts`): Fetch detailed review comments for the top reviewers
5. **Analysis** (`src/review_prompt_mining/analyze/`): Send review comments to Claude to identify recurring patterns and team standards
6. **Context Generation** (`src/review_prompt_mining/generate-prompt/`): Use Claude to transform the analysis into a markdown prompt file
7. **Output Files**: Writes `<output>.md` (final prompt), `<output>-analysis.md` (pattern analysis), `<output>-details.json` (raw comments)

### Key Concepts

**Three-Step Pipeline**
1. **Discover** — find the top reviewers in the org by PR review activity (GitHub GraphQL API)
2. **Analyze** — send their comments to Claude to extract recurring patterns and standards
3. **Generate** — use Claude to transform the analysis into a well-structured markdown prompt

**Context File Usage**
- The generated prompt is a markdown file that codifies team review standards
- Place it in `.chunk/context/` so AI coding agents (Claude Code, etc.) load it automatically as context
- The file instructs agents to apply the same patterns human reviewers consistently enforce

**Configuration Resolution**
Model and other settings resolve in this priority order:
1. CLI flags (`--analyze-model`, `--prompt-model`, etc.)
2. Built-in defaults

## File Structure

```
src/
├── index.ts              # Entry point, command routing
├── commands/             # CLI command implementations
│   ├── build-prompt.ts   # Main command: run full context generation pipeline
│   ├── auth.ts           # API key management
│   ├── config.ts         # Configuration management
│   └── upgrade.ts        # Self-update mechanism
├── core/                 # Business logic
│   ├── build-prompt.ts   # Orchestrates the three-step pipeline
│   ├── agent.ts          # Anthropic API client, streaming
│   ├── context.ts        # Context file gathering
│   ├── filesystem-prompts.ts  # Pattern file detection (.chunk/context/)
│   └── upgrade.ts        # Upgrade logic
├── review_prompt_mining/ # Pipeline implementation
│   ├── top-reviewers/    # GitHub reviewer discovery and comment fetching
│   ├── analyze/          # Claude analysis of reviewer patterns
│   ├── generate-prompt/  # Claude context prompt generation
│   ├── graphql-client.ts # GitHub GraphQL API client
│   └── types.ts          # Shared types
├── storage/              # Persistence
│   └── config.ts         # User config (~/.config/chunk/)
├── config/               # Configuration constants
│   └── index.ts          # Models, pricing, paths
├── ui/                   # Terminal UI utilities (colors, spinner, format)
└── __tests__/            # Test suite
    ├── *.unit.test.ts    # Unit tests (pure functions, parsers)
    └── *.e2e.test.ts     # E2E tests
```

## Testing Philosophy

- **E2E over mocks**: Critical workflows use real git operations in temporary directories
- **No Claude API mocking**: Tests that need Claude API require `ANTHROPIC_API_KEY` env var; skip gracefully if missing
- **No TUI testing**: Interactive mode requires manual QA
- **Fakes over mocks**: Use fake servers (e.g., release server) instead of mocking libraries
- **Test isolation**: Each test creates its own temp directory and cleans up after

### Test Naming Convention
- `*.unit.test.ts`: Pure functions, parsers, utilities
- `*.e2e.test.ts`: End-to-end workflows (git, auth, sessions, upgrade)

### Running Tests
- Unit tests run fast (~100ms per file)
- E2E tests may be slower due to git operations and temporary directories
- Anthropic integration test (`anthropic-integration.e2e.test.ts`) requires API key

## Important Implementation Details

### Pipeline Output Files
Running `chunk build-prompt --org <org>` produces three files alongside the final prompt:
- `<output>.md` — the generated context prompt (main output)
- `<output>-analysis.md` — Claude's analysis of reviewer patterns
- `<output>-details.json` — raw review comment data for the top reviewers

### .chunk/context/ Directory
- AI coding agents (like Claude Code) auto-scan this directory for `.md` context files
- Place the generated prompt here so agents load it automatically
- Only top-level files, lowercase `.md` extension (no subdirectories)

### GitHub GraphQL API
- Requires `GITHUB_TOKEN` with `repo` scope
- Rate limit is checked before starting; the tool backs off if limits are hit
- `--since` limits the date range; `--repos` limits which repos are scanned

## Code Conventions

- **TypeScript strict mode**: No implicit any, strict null checks
- **Async/await**: Prefer over raw promises
- **Early returns**: Reduce nesting depth
- **Const over let**: Immutability by default
- **Template literals**: For string interpolation
- **Destructuring**: Objects and arrays where readable
- **Error handling**: Typed error classes (NetworkError, AuthError, GitError) bubble to command layer

## Binary Distribution

The tool is distributed as pre-compiled binaries via GitHub Releases:
- `chunk-darwin-arm64`, `chunk-darwin-x64`, `chunk-linux-arm64`, `chunk-linux-x64`
- Installation via `install.sh` tries binary download first (requires `gh` CLI), falls back to source build
- `~/.local/share/chunk/repo` is always cloned/updated for team prompts and self-update
- Self-update via `chunk upgrade` re-runs `install.sh`

## Development Tips

- When adding new pattern files, update `PATTERN_FILES` array in `src/core/filesystem-prompts.ts`
- The pipeline is orchestrated in `src/core/build-prompt.ts`; each of the three steps is a separate module under `src/review_prompt_mining/`
- Cost calculation requires model pricing data in `src/config/index.ts`
