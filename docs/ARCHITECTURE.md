# Architecture

This document describes the intended end state of the current CLI restructuring work.

- Use it as the design target when routing new changes
- Do not assume every file move or extraction described here has already landed in `main`
- When touching transitional code, prefer changes that move the implementation toward this shape without rewriting unrelated areas

## Layering Rules

Dependencies flow strictly downward:

```
commands/ → core/ → (storage, api, review_prompt_mining, ui, utils)
```

- `commands/` may import from `core/`, `types/`, and `config/`
- `core/` may import from `storage/`, `api/`, `review_prompt_mining/`, `ui/`, `utils/`, `types/`, and `config/`
- `storage/`, `api/`, `review_prompt_mining/`, `ui/`, `utils/` must NOT import from `commands/` or `core/`
- `config/` is a leaf — no imports from any `src/` module

No upward dependencies.

Machine-enforced boundary:

- leaf modules (`storage/`, `api/`, `review_prompt_mining/`, `ui/`, `utils/`, `skills/`) must not import from `commands/` or `core/`
- `config/` must not import from any other `src/` module

`types/` remains runtime-free and can be checked separately if needed; it is not part of the first import-boundary rule.

Design guidance beyond the enforced rule:

- keep lateral imports between leaf modules rare
- `ui/` is the main shared leaf for formatting and display helpers
- if a helper exists mainly to print, format, or color terminal output, prefer `ui/` over `utils/`

> **Example**: `review_prompt_mining/top-reviewers/output.ts` imports from `ui/colors` and `ui/format`. This is a conforming lateral import under the `ui/` exception above.

## Data Flow: `chunk build-prompt`

1. **Entry Point** (`src/index.ts`): Command routing and top-level error handling
2. **Command Layer** (`src/commands/build-prompt.ts`): Parse args, validate inputs (org, repos, dates, models)
3. **Reviewer Discovery** (`src/review_prompt_mining/top-reviewers/`): Query GitHub GraphQL API for top reviewers by activity in the org
4. **Comment Fetching** (`src/review_prompt_mining/top-reviewers/review-fetcher.ts`): Fetch detailed review comments for the top reviewers
5. **Analysis** (`src/review_prompt_mining/analyze/`): Send review comments to Claude to identify recurring patterns and team standards
6. **Context Generation** (`src/review_prompt_mining/generate-prompt/`): Use Claude to transform the analysis into a markdown prompt file
7. **Output Files**: Writes `<output>.md` (final prompt), `<output>-analysis.md` (pattern analysis), `<output>-details.json` (raw comments)

### Three-Step Pipeline

1. **Discover** — find the top reviewers in the org by PR review activity (GitHub GraphQL API)
2. **Analyze** — send their comments to Claude to extract recurring patterns and standards
3. **Generate** — use Claude to transform the analysis into a well-structured markdown prompt

### Context File Usage

- The generated prompt is a markdown file that codifies team review standards
- Place it in `.chunk/context/` so AI coding agents (Claude Code, etc.) load it automatically as context
- The file instructs agents to apply the same patterns human reviewers consistently enforce
- Only top-level files, lowercase `.md` extension (no subdirectories)

### Configuration Resolution

Model and other settings resolve in this priority order:
1. CLI flags (`--analyze-model`, `--prompt-model`, etc.)
2. Built-in defaults

## index.ts Rules

`src/index.ts` is the composition root only. It should:

- Create the top-level Commander program
- Register top-level command groups from `commands/`
- Handle top-level process exit and uncaught error presentation
- Avoid command-specific business logic, help text, defaults, parser helpers, and validation rules

Command-specific option definitions, examples, and help text should live with the corresponding `commands/*` module so CLI behavior is defined close to the implementation it triggers.

## commands/ Rules

Thin registration modules only. Each command file should:

- Define one command or command group
- Own that command's help text, examples, option parsing, and validation
- Call one core function per action handler
- Return `CommandResult` from action handlers
- Export a registration helper that `index.ts` can compose
- Target 30–80 lines when practical; treat this as a guideline, not a hard limit
- Contain NO business logic, NO spinners, NO complex control flow

Example structure:
```typescript
export function registerBuildPromptCommand(program: Command): void {
  program
    .command("build-prompt")
    // options, help text, examples
    .action(async (flags) => {
      process.exit((await runBuildPrompt(flags)).exitCode);
    });
}
```

## core/ Rules

Business logic and orchestration. Each public function does one conceptual step.

### Orchestrators vs Step Functions

`core/` contains two kinds of functions:

- **Orchestrator functions** (e.g., `extractCommentsAndBuildPrompt`, `runTaskConfigWizard`) coordinate workflow steps and handle display (spinners, progress, interactive prompts). They live in the main module file (e.g., `core/build-prompt.ts`).
- **Step functions** are pure or near-pure functions that return data only — no spinners, no direct terminal output, no interactive prompts. They live in a `.steps.ts` sibling file (e.g., `core/build-prompt.steps.ts`).

This separation gives testability (step functions are easy to unit test without mocking UI) while keeping orchestration centralized in `core/` (not scattered across commands/).

**File convention:**
```
core/build-prompt.ts          # orchestrator: UI + workflow coordination
core/build-prompt.steps.ts    # step functions: pure logic, return data only
```

**Example step function:** `resolveOrgAndRepos()` in `core/build-prompt.steps.ts` — resolves org/repo from CLI flags with git remote auto-detection. Pure decision logic, no terminal output.

### General Rules

- Orchestrators may use `ui/` for formatting, spinners, and interactive prompts; step functions must not
- Interactive wizards (like `runTaskConfigWizard`) are orchestrators, not step functions — they coordinate user input and business logic, so they belong in `core/` and are allowed to use `ui/`
- This is an intentional CLI-specific tradeoff: `core/` owns orchestration and user-facing interaction, while leaf modules stay display-free
- Keep functions focused: one function, one responsibility
- `core/` should not become a grab-bag for shared helpers; if logic is pure and reusable outside one workflow, prefer a leaf module

`core/` is not intended to be framework-agnostic domain logic. In this CLI, `core/` is the orchestration layer: it coordinates business logic, workflow sequencing, and user-facing CLI interaction, while leaf modules stay display-free.

## Enforcement

These rules should be machine-checked where possible:

- Add an import-boundary check in tests or linting so forbidden upward imports fail CI
- Keep the machine-checked rule narrow and explicit: `storage/`, `api/`, `review_prompt_mining/`, `ui/`, `utils/`, and `skills/` must not import from `commands/` or `core/`, and `config/` must not import from other `src/` modules
- Add CLI help tests for the public command tree so docs and implementation do not drift
- Prefer one focused refactor per phase to keep reviewable diffs small

## config/ Rules

Single source of truth for defaults, env var names, and config-level path helpers.

- Side-effect-free only: constants and pure helper functions are allowed
- All model defaults (`DEFAULT_ANALYZE_MODEL`, `DEFAULT_PROMPT_MODEL`, etc.)
- All path defaults (`DEFAULT_OUTPUT_PATH`)
- Environment variable names
- No imports from other `src/` modules

## storage/ Rules

File I/O for persisted configuration.

- No business logic
- Read/write/validate config files
- Return typed data structures

## api/ Rules

External service clients. Currently contains `circleci.ts` (CircleCI API client, types, and error classes).

- One file per external service
- No business logic — thin wrappers around HTTP calls
- Return typed responses; export service-specific error classes alongside the client
- Note: `review_prompt_mining/graphql-client.ts` is the GitHub GraphQL client. It lives under `review_prompt_mining/` because it's specific to the review mining pipeline, whereas `api/` holds general-purpose service clients.

## utils/ Rules

Prefer pure utility functions. Avoid terminal formatting and display logic here; put presentation-oriented helpers in `ui/`.

## types/ Rules

Type-only exports. No runtime code, no side effects.

## skills/ Rules

Embedded skill content loaded at build time. Leaf module — no imports from other `src/` modules.

## Change Routing

When deciding where new code belongs:

- Add a CLI flag or subcommand in `commands/`, then delegate immediately to `core/`
- Add a workflow, wizard, or multi-step operation in `core/`
- Add a persisted config schema, read/write, or file validation in `storage/`
- Add a thin external API client in `api/`
- Add terminal formatting, color, or prompt helpers in `ui/`
- Add a pure helper with no presentation concerns in `utils/`
- Add defaults, env var names, or path helpers in `config/`
- Add shared runtime-free types in `types/`

## Packages

This is a Bun workspace monorepo. Packages live under `packages/` and are declared in the root `package.json` (`"workspaces": ["packages/hook"]`).

### `@chunk/hook` (`packages/hook/`)

The hook package implements the `chunk hook` command group — 7 subcommand families (exec, task, sync, state, scope, repo, env) for AI coding agent hook lifecycle automation. It is the largest body of code in the repo (~2,500+ lines of source, 17 lib modules, 26 test files).

**Integration point**: The main CLI imports a single function:

```typescript
import { registerHookCommands } from "@chunk/hook";
const hook = program.command("hook").description("...");
registerHookCommands(hook);
```

The hook package has **no imports from `src/`** and `src/` has **no knowledge of hook internals**. The package follows its own internal conventions documented in `packages/hook/AGENTS.md`. The `src/` layering rules in this document do not apply inside `packages/hook/`.

> **Evaluated**: The hook command structure (7 subcommand families) was reviewed during restructuring planning. The current design is well-suited: families map to distinct responsibilities, machine-invoked commands benefit from explicitness, and the `hook` namespace cleanly isolates them from the rest of the CLI. No changes needed.

> **Naming collision**: `src/commands/task.ts` (CircleCI pipeline runs) and `packages/hook/src/commands/task.ts` (delegated subagent work) are completely unrelated. The former is the `chunk task` command; the latter is `chunk hook task`.

### When to create a package vs. keep in `src/`

Create a separate package when:

- The code has a **distinct domain** with its own concepts that don't overlap with the main CLI (e.g., sentinels, adapters, scope markers)
- It has a **clean API surface** — ideally one export function as the entire contract
- It has **its own dependency tree** (even if minimal)
- It's **large enough** that separate conventions and test organization reduce cognitive load

Keep in `src/` when:

- The code is **tightly coupled** to the main CLI's data flow (e.g., `review_prompt_mining/` feeds directly into `core/build-prompt.ts`)
- It **shares types and utilities** heavily with the rest of `src/`
- It's **small enough** that packaging overhead (separate package.json, workspace config, path aliases) adds more friction than clarity

By this framework, nothing currently in `src/` needs to become a package. If a new major feature area emerged with a similarly distinct domain and clean boundary, that would be the signal.

## Naming Conventions

| CLI Command | Code File | Export Pattern |
|-------------|-----------|----------------|
| `chunk task config` | `core/task-config.ts` | `runTaskConfigWizard()` |
| `chunk task run --definition ...` | `core/task-run.ts` | `runTask()` |
| `chunk build-prompt` | `core/build-prompt.ts` | `extractCommentsAndBuildPrompt()` |

- CLI `task` → code `task-*.ts`
- `chunk task run` triggers a pipeline run; `chunk task config` is the setup wizard

## Test Organization

Mirror `src/` structure under `src/__tests__/`. New test files should go in the mirrored location; existing flat test files migrate opportunistically when touched.

```
src/__tests__/
├── commands/
│   ├── task-command.unit.test.ts
│   └── ...
├── api/
│   └── circleci-api.unit.test.ts
├── core/
│   ├── task-config.unit.test.ts
│   └── task-run.unit.test.ts
├── storage/
│   ├── run-config.unit.test.ts
│   └── run-config-fs.unit.test.ts
├── ui/
│   └── format.unit.test.ts
└── utils/
    └── git-remote.unit.test.ts
```

Naming: `<module>.unit.test.ts` or `<module>.e2e.test.ts`

## Module Structure

```
src/
├── index.ts                        # Composition root only: create program, register commands, top-level error handling
├── commands/                       # Command registration modules (thin)
│   ├── build-prompt.ts             # registerBuildPromptCommand()
│   ├── task.ts                     # registerTaskCommands()
│   ├── auth.ts                     # registerAuthCommands()
│   ├── config.ts                   # registerConfigCommands()
│   ├── skills.ts                   # registerSkillsCommands()
│   └── upgrade.ts                  # registerUpgradeCommand()
├── config/
│   └── index.ts                    # ALL defaults: models, paths, env vars
├── core/
│   ├── build-prompt.ts             # Pipeline orchestrator: UI + workflow coordination
│   ├── build-prompt.steps.ts       # Pure step functions: resolveOrgAndRepos(), deriveOutputPaths()
│   ├── task-config.ts              # Extracted task setup wizard
│   ├── task-run.ts                 # Extracted task trigger logic
│   ├── agent.ts
│   ├── skills.ts
│   └── upgrade.ts
├── storage/
│   ├── config.ts
│   └── run-config.ts
├── review_prompt_mining/           # PR review mining pipeline
├── api/                            # External service clients (leaf)
├── ui/                             # Terminal display helpers (leaf, lateral-import exception)
├── utils/                          # Pure utility functions (leaf)
├── skills/                         # Embedded skill content (leaf)
├── types/                          # Type-only exports (leaf)
└── __tests__/                      # Mirrored structure (migrate incrementally)
    ├── commands/
    ├── api/
    ├── core/
    ├── storage/
    ├── ui/
    └── utils/
```

## Implementation Details

### Pipeline Output Files

Running `chunk build-prompt --org <org>` produces three files alongside the final prompt:
- `<output>.md` — the generated context prompt (main output)
- `<output>-analysis.md` — Claude's analysis of reviewer patterns
- `<output>-details.json` — raw review comment data for the top reviewers

### GitHub GraphQL API

- Requires `GITHUB_TOKEN` with `repo` scope
- Rate limit is checked before starting; the tool backs off if limits are hit
- `--since` limits the date range; `--repos` limits which repos are scanned

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
