# Proposed Restructuring Summary

This document summarizes the proposed codebase and CLI changes described in `ARCHITECTURE.md`, `CLI.md`, and `tasks.md`.

## Goals

- Make the codebase easier to navigate, modify, and reason about
- Make responsibilities clearer between command parsing, orchestration, storage, API clients, and UI
- Make the CLI easier to understand without changing product terminology that already exists in the UI
- Reduce friction for AI coding agents by making module boundaries and command behavior more predictable

## Proposed CLI Direction

The `task` command tree stays in place:

```text
chunk task config
chunk task run --definition <name> --prompt <text>
```

This keeps the CLI aligned with the product term used in the UI while still tightening the command structure and help text.

Other key CLI proposals:

- Keep `build-prompt` as the main prompt-generation pipeline command
- Change the default `build-prompt` output path to `.chunk/context/review-prompt.md`
- Support `build-prompt` org auto-detection from the git remote when `--org` is omitted
- Allow `--org` without requiring `--repos`; omitting `--repos` means "all repos in the org"
- Merge `skills list` and `skills status` into a single `skills list` view that shows bundled skills plus install state
- Make help text, examples, README docs, and implementation agree on the same CLI behavior

## Proposed Architecture Direction

The codebase should follow stricter layering:

```text
commands/ -> core/ -> (storage, api, review_prompt_mining, ui, utils)
```

Key ideas:

- `commands/` are thin wrappers that parse flags, validate inputs, call one core function, and return `CommandResult`
- `core/` owns business logic and orchestration
- `storage/` handles persisted config file I/O only
- `api/` contains thin external service clients
- `utils/` contains pure helper functions
- `types/` contains type-only exports
- `config/` becomes the single source of truth for defaults and env var names

For CLI workflows, `core/` orchestrators may still own user-facing progress output such as spinners and formatted status updates. Step functions beneath them should return data only and avoid terminal output.

## Main Structural Changes

- Move `DEFAULT_ANALYZE_MODEL`, `DEFAULT_PROMPT_MODEL`, and the default output path into `src/config/index.ts`
- Extract task setup logic from `src/commands/task.ts` into `src/core/task-config.ts`
- Extract task run logic from `src/commands/task.ts` into `src/core/task-run.ts`
- Break `src/core/build-prompt.ts` into an orchestrator plus focused step functions in `src/core/build-prompt-steps.ts`
- Reorganize tests to mirror the source tree more closely

## Why This Helps

- Smaller command files are easier to scan and safer to change
- Extracted core modules make business logic easier to test directly
- Centralized defaults reduce hidden behavior and cross-module coupling
- Clear layering improves local reasoning for both humans and AI agents
- A more consistent CLI lowers documentation drift and user confusion

## Important Non-Goals

- Do not rename `task` away from the CLI, since it is already the product term used in the UI
- Do not change the `hook` command structure as part of this effort
- Do not add architecture rules that exist only in docs; enforcement should be added in tests or linting where practical

## Implementation Themes

The implementation plan focuses on:

- consolidating defaults
- extracting core logic from command files
- tightening `task` command UX and docs
- merging overlapping skills commands
- reorganizing tests
- adding checks so architecture and CLI docs do not drift from the implementation

For the detailed target state, see `ARCHITECTURE.md`, `CLI.md`, and `tasks.md`.
