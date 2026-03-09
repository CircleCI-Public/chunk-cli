# Proposed Architecture

> **Note**: This document describes the *target* module structure, not necessarily the current state. It serves as a reference for all restructuring work.

## Layering Rules

Dependencies flow strictly downward:

```
commands/ → core/ → (storage, api, review_prompt_mining, ui, utils)
```

- `commands/` may import from `core/`, `types/`, and `config/`
- `core/` may import from `storage/`, `api/`, `review_prompt_mining/`, `ui/`, `utils/`, `types/`, and `config/`
- `storage/`, `api/`, `review_prompt_mining/`, `ui/`, `utils/` must NOT import from `commands/` or `core/`
- `config/` is a leaf — no imports from any `src/` module

No upward or lateral dependencies between peer modules at the same layer.

## commands/ Rules

Thin wrappers only. Each command file should:

- Parse flags and validate inputs
- Call one core function
- Return `CommandResult`
- Target 30–60 lines
- Contain NO business logic, NO spinners, NO complex control flow

Example structure:
```typescript
export async function runBuildPrompt(flags: ParsedFlags): Promise<CommandResult> {
  // validate / transform flags
  // call core function
  // return { exitCode: 0 }
}
```

## core/ Rules

Business logic and orchestration. Each public function does one conceptual step.

- May use `ui/` for formatting and spinners
- Orchestrator functions (e.g., `extractCommentsAndBuildPrompt`) call step functions
- Step functions return data; orchestrators handle display
- Keep functions focused: one function, one responsibility

## config/ Rules

Single source of truth for ALL defaults.

- Constants only, no side effects
- All model defaults (`DEFAULT_ANALYZE_MODEL`, `DEFAULT_PROMPT_MODEL`, etc.)
- All path defaults (`DEFAULT_OUTPUT_PATH`)
- Environment variable names
- No imports from other `src/` modules

## storage/ Rules

File I/O for persisted configuration.

- No business logic
- Read/write/validate config files
- Return typed data structures

## Naming Conventions

| CLI Command | Code File | Export Pattern |
|-------------|-----------|----------------|
| `chunk run setup` | `core/run-setup.ts` | `runSetupWizard()` |
| `chunk run task` | `core/run-trigger.ts` | `triggerRun()` |
| `chunk build-prompt` | `core/build-prompt.ts` | `extractCommentsAndBuildPrompt()` |

- CLI `run` → code `run-*.ts`
- "task" refers to the pipeline run triggered by `chunk run task`

## Test Organization

Mirror `src/` structure under `__tests__/`:

```
src/__tests__/
├── commands/
│   ├── run-command.unit.test.ts
│   └── ...
├── core/
│   ├── build-prompt-steps.unit.test.ts
│   ├── run-setup.unit.test.ts
│   └── run-trigger.unit.test.ts
├── storage/
│   ├── run-config.unit.test.ts
│   └── run-config-fs.unit.test.ts
├── ui/
│   └── format.unit.test.ts
└── utils/
    ├── git-remote.unit.test.ts
    └── circleci-api.unit.test.ts
```

Naming: `<module>.unit.test.ts` or `<module>.e2e.test.ts`

## Module Structure

```
src/
├── index.ts                        # CLI routing (thin)
├── commands/                       # Thin wrappers only (30-60 lines each)
│   ├── build-prompt.ts
│   ├── run.ts                      # Was: task.ts
│   ├── auth.ts
│   ├── config.ts
│   ├── skills.ts
│   └── upgrade.ts
├── config/
│   └── index.ts                    # ALL defaults: models, paths, env vars
├── core/
│   ├── build-prompt.ts             # Orchestrator calling step functions
│   ├── build-prompt-steps.ts       # discoverReviewers(), analyzePatterns(), generatePrompt()
│   ├── run-setup.ts                # Extracted setup wizard
│   ├── run-trigger.ts              # Extracted trigger logic
│   ├── agent.ts
│   ├── skills.ts
│   └── upgrade.ts
├── storage/
│   ├── config.ts
│   └── run-config.ts
├── review_prompt_mining/
├── api/
├── ui/
├── utils/
├── skills/
├── types/
└── __tests__/                      # Mirrored structure
    ├── commands/
    ├── core/
    ├── storage/
    ├── ui/
    └── utils/
```
