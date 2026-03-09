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

- Merge `skills list` and `skills status` into a single `skills list` view
- Rewrite help text across all commands so docs, examples, and implementation agree
- Change the default `build-prompt` output path to `.chunk/context/review-prompt.md` (shipped separately as a breaking change with deprecation warning)
- Ensure `build-prompt` creates parent directories for the chosen output path

The command tree itself stays the same — no renames, no new top-level commands. `task` matches the product term used in the UI, and `build-prompt` is already clear. The focus is on tightening UX within the existing structure rather than reshuffling it.

## Proposed Architecture Direction

The codebase should follow stricter layering:

```text
commands/ -> core/ -> (storage, api, review_prompt_mining, ui, utils)
```

Key ideas:

- `src/index.ts` becomes a true composition root: create the program, register commands, and handle top-level errors only
- `commands/` become thin registration modules that own help text, options, and validation, then delegate to `core/`
- `core/` owns business logic and orchestration
- `storage/` handles persisted config file I/O only
- `api/` contains thin external service clients
- `utils/` contains pure helper functions
- `types/` contains type-only exports
- `config/` becomes the single source of truth for defaults, env var names, and pure path helpers (fixing the current import direction violation where `index.ts` imports from `commands/`)
- The enforced architecture rule should match the actual CI check: `storage/`, `api/`, `review_prompt_mining/`, `ui/`, `utils/`, and `skills/` must not import from `commands/` or `core/`, and `config/` must remain import-free from the rest of `src/`

For CLI workflows, `core/` orchestrators may still own user-facing progress output such as spinners and formatted status updates. Step functions beneath them should return data only and avoid terminal output. In this design, `core/` is explicitly the CLI orchestration layer, not an attempt at framework-agnostic domain code.

## Main Structural Changes

- Move `DEFAULT_ANALYZE_MODEL`, `DEFAULT_PROMPT_MODEL` into `src/config/index.ts` (fixes import direction violation)
- Move command-specific help text, option definitions, and parser helpers out of `src/index.ts` and into the corresponding `src/commands/*` modules
- Move org/repo auto-detection logic from `commands/build-prompt.ts` into `core/`
- Extract task setup logic from `src/commands/task.ts` into `src/core/task-config.ts`
- Extract task run logic from `src/commands/task.ts` into `src/core/task-run.ts`
- Add import-boundary enforcement test
- Migrate test files into mirrored structure incrementally (not as a big-bang reorg)

## Why This Helps

- Smaller command files are easier to scan and safer to change
- A composition-root `index.ts` reduces the biggest source of CLI drift and keeps command UX definitions easier to find
- Extracted core modules make business logic easier to test directly
- Centralized defaults reduce hidden behavior and cross-module coupling
- Clear layering improves local reasoning for both humans and AI agents
- An explicit `build-prompt` behavior matrix removes the highest-risk CLI ambiguity
- A more consistent CLI lowers documentation drift and user confusion

## Important Non-Goals

- Do not rename `task` away from the CLI, since it is already the product term used in the UI
- The `hook` command structure was evaluated and found to be well-suited to its purpose — no changes needed. See plan file or commit history for the full evaluation.
- Do not add architecture rules that exist only in docs; enforcement should be added in tests or linting where practical
- Do not break up `core/build-prompt.ts` — the file is ~300 lines, reads clearly top-to-bottom, and already delegates to `review_prompt_mining/` modules. Splitting it would add indirection without testability gain.
- Do not reorganize all test files in a single pass — migrate incrementally as each phase touches related files
- `SUMMARY.md` itself is temporary — delete it once the restructuring is done (Phase 8)

## Implementation Themes

The implementation plan focuses on:

- consolidating defaults and fixing the import direction violation (mechanical, ship first)
- extracting core logic from `commands/task.ts` (highest-value refactor)
- tightening `task` command UX and docs
- merging overlapping skills commands
- adding import-boundary enforcement and CLI help tests
- changing the default output path (user-facing breaking change, ship separately with deprecation warning)

For the detailed target state, see `ARCHITECTURE.md`, `CLI.md`, and `tasks.md`.
