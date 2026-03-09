# Proposed Restructuring Summary

> **Lifecycle**: This document is a pitch/overview for the restructuring effort. Once the restructuring is complete, it should be deleted — `ARCHITECTURE.md` and `CLI.md` are the long-lived reference docs.

This document summarizes the proposed codebase and CLI changes described in `ARCHITECTURE.md`, `CLI.md`, and `tasks.md`.

## Goals

- Make the codebase easier to navigate, modify, and reason about
- Make responsibilities clearer between command parsing, orchestration, storage, API clients, and UI
- Make the CLI easier to understand without changing product terminology that already exists in the UI
- Reduce friction for AI coding agents by making module boundaries and command behavior more predictable

## Proposed CLI Direction

Changes:

- Change the default `build-prompt` output path to `.chunk/context/review-prompt.md`
- Merge `skills list` and `skills status` into a single `skills list` view
- Rewrite help text across all commands so docs, examples, and implementation agree

The command tree itself stays the same — no renames, no new top-level commands. `task` matches the product term used in the UI, and `build-prompt` is already clear. The focus is on tightening UX within the existing structure rather than reshuffling it.

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
- `config/` becomes the single source of truth for defaults and env var names (fixing the current import direction violation where `index.ts` imports from `commands/`)
- The enforced architecture rule should match the actual CI check: leaf modules must not import from `commands/` or `core/`, and `config/` must remain import-free from the rest of `src/`

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
- An explicit `build-prompt` behavior matrix removes the highest-risk CLI ambiguity
- A more consistent CLI lowers documentation drift and user confusion

## Important Non-Goals

- Do not rename `task` away from the CLI, since it is already the product term used in the UI
- The `hook` command structure was evaluated and found to be well-suited to its purpose — no changes needed. See plan file or commit history for the full evaluation.
- Do not add architecture rules that exist only in docs; enforcement should be added in tests or linting where practical
- `SUMMARY.md` itself is temporary — delete it once the restructuring is done (Phase 8)

## Implementation Themes

The implementation plan focuses on:

- consolidating defaults
- extracting core logic from command files
- tightening `task` command UX and docs
- merging overlapping skills commands
- reorganizing tests
- adding checks so architecture and CLI docs do not drift from the implementation

For the detailed target state, see `ARCHITECTURE.md`, `CLI.md`, and `tasks.md`.
