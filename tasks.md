# Implementation Tasks

Checklist for the codebase and CLI restructuring. See `ARCHITECTURE.md` and `CLI.md` for target state.

## Scope

This restructuring covers `src/` only. `packages/hook/` (`@chunk/hook`) is **out of scope** — it is already well-structured with its own command/lib separation, test suite, and conventions (see `packages/hook/AGENTS.md`). Its internal patterns (thin command orchestrators, pure lib functions, mirrored tests) are a useful reference for the target state of `src/`.

## Phase 1: Documentation
- [x] Write `ARCHITECTURE.md` (proposed)
- [x] Write `CLI.md` (proposed)
- [x] Write `tasks.md`

## Phase 2: Fix Import Direction and Consolidate Defaults
The main goal here is fixing the import direction: `index.ts` (entry point) currently imports model defaults from `commands/build-prompt.ts`, which violates the proposed layering. Moving the defaults to `config/` fixes this so both files import downward.

- [ ] Move `DEFAULT_ANALYZE_MODEL` and `DEFAULT_PROMPT_MODEL` from `commands/build-prompt.ts` to `config/index.ts` — after the move, both `commands/build-prompt.ts` and `index.ts` import from `config/`
- [ ] Add `DEFAULT_OUTPUT_PATH` (`.chunk/context/review-prompt.md`) to `config/index.ts`
- [ ] Change `--output` default to `.chunk/context/review-prompt.md` (via `DEFAULT_OUTPUT_PATH`)
- [ ] Auto-create `.chunk/context/` directory in `build-prompt` if it doesn't exist when using the default output path
- [ ] Add a deprecation warning: if `./review-prompt.md` exists in the working directory when using the new default path, print a one-time notice so users with scripts referencing the old path are aware
- [ ] Update `commands/build-prompt.ts` to import model defaults from `config/`
- [ ] Update `index.ts` to import model defaults and `DEFAULT_OUTPUT_PATH` from `config/`
- [ ] Resolve `build-prompt` flag semantics so implementation, help text, and README all agree:
  - no flags → auto-detect org and current repo from git remote
  - `--repos` only → auto-detect org from git remote and use the provided repos
  - `--org` provided + `--repos` provided → analyze the provided repos in the provided org
  - `--org` provided + `--repos` omitted → error (no way to enumerate all repos in an org)
  - if auto-detection is needed and fails, print a clear error telling the user to pass `--org`
- [ ] Note: Changing the `--output` default from `./review-prompt.md` to `.chunk/context/review-prompt.md` is a user-facing behavior change — document in release notes

## Phase 3: Extract Core Logic from commands/task.ts ⬅ highest-value phase
- [ ] Create `src/core/task-config.ts` with `runTaskConfigWizard()`
- [ ] Create `src/core/task-run.ts` with `runTask()`
- [ ] Move `mapVcsTypeToOrgType()` and `buildProjectSlug()` to `core/task-config.ts`
- [ ] Slim `commands/task.ts` to thin wrappers
- [ ] Update existing tests

## Phase 4: Break Up core/build-prompt.ts (lower priority — file is 300 lines, not unwieldy)
- [ ] Create `src/core/build-prompt-steps.ts` with step functions:
  - `discoverTopReviewers()` — query GitHub for top reviewers by activity
  - `analyzeReviewPatterns()` — send comments to Claude for pattern analysis
  - `generatePromptFile()` — transform analysis into markdown prompt
- [ ] Refactor `extractCommentsAndBuildPrompt()` to call step functions (orchestrator handles spinners/display; step functions return data only)

## Phase 5: Tighten task command UX
- [ ] Keep `task` as the top-level command group and preserve `chunk task config` / `chunk task run`
- [ ] Audit and rewrite `chunk task --help` output:
  - Ensure the description explains what "tasks" are (CircleCI pipeline runs)
  - Ensure subcommand descriptions are parallel in style
- [ ] Audit and rewrite `chunk task run --help` output:
  - Add a concrete usage example: `chunk task run --definition dev --prompt "Fix the failing test"`
  - Ensure flag descriptions are complete (e.g., clarify that `--definition` accepts a configured name or raw UUID)
  - Clarify that `.chunk/run.json` is still required even when a raw UUID is passed, because org/project context comes from config
- [ ] Audit and rewrite `chunk task config --help` output:
  - Add a one-line description of what the wizard sets up (`.chunk/run.json`)
- [ ] Align "Next steps" output printed at the end of `runTaskConfig` with the finalized help text (e.g., the suggested `chunk task run` invocation should use the same flag names and format shown in `--help`)
- [ ] Rename extracted core symbols/files as needed to match `task-*` naming consistently
- [ ] Rename/update test files

## Phase 6: Merge skills list + status
- [ ] Merge `skills list` and `skills status` into single `skills list` view that shows: skill name, description, and per-agent install state (current/outdated/missing)
- [ ] Remove standalone `listSkills()` from `core/skills.ts` (fold into `getSkillsStatus()` or a new combined function)
- [ ] Remove `skills status` subcommand from `index.ts`
- [ ] Update `commands/skills.ts`: remove `runSkillsList()` and `runSkillsStatus()`, replace with single `runSkillsList()` that renders the merged view
- [ ] Update `skills.unit.test.ts`

## Phase 7: Reorganize Tests
- [ ] Create directory structure under `__tests__/` (see ARCHITECTURE.md for target layout)
- [ ] Move test files to mirror source structure — combine with source moves from Phases 3/5 where possible to avoid double-churn (e.g., when creating `core/task-config.ts` in Phase 3, create `__tests__/core/task-config.unit.test.ts` at the same time rather than first moving the old test and then moving it again)
- [ ] Verify imports
- [ ] Add CLI help/usage tests covering `chunk build-prompt`, `chunk task config`, and `chunk task run`
- [ ] Add an import-boundary check so the documented machine-enforced rule fails CI when violated. Suggested approach: a unit test that checks `src/storage/`, `src/api/`, `src/review_prompt_mining/`, `src/ui/`, and `src/utils/` for imports from `commands/` or `core/`, and checks that `src/config/` imports nothing from the rest of `src/`. Keep lateral leaf-to-leaf imports as review guidance rather than part of the first enforcement pass.

## Phase 8: Finalize Documentation
- [ ] Update `ARCHITECTURE.md` — remove "proposed" framing
- [ ] Update `CLI.md` — remove "proposed" framing
- [ ] Delete `SUMMARY.md` — it served as a pitch document for the restructuring; once complete, `ARCHITECTURE.md` and `CLI.md` are the living docs
- [ ] Update `tasks.md` — mark complete or remove
- [ ] Update `CLAUDE.md` to reflect all changes (including monorepo workspace structure and `packages/hook/`)
- [ ] Confirm hook tests are runnable from root — currently `bun test` only runs `src/**/*.unit.test.ts`; hook tests require `cd packages/hook && bun test` or a new workspace-aware script
- [ ] Update `README.md` examples and command reference to match the final CLI
- [ ] Add release notes / migration notes for:
  - new default output path for `build-prompt`
  - deprecation warning for old `./review-prompt.md` path
  - finalized `build-prompt` auto-detection behavior
