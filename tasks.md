# Implementation Tasks

Checklist for the codebase and CLI restructuring. See `ARCHITECTURE.md` and `CLI.md` for target state.

## Phase 1: Documentation
- [x] Write `ARCHITECTURE.md` (proposed)
- [x] Write `CLI.md` (proposed)
- [x] Write `tasks.md`

## Phase 2: Consolidate Defaults
- [ ] Move `DEFAULT_ANALYZE_MODEL` and `DEFAULT_PROMPT_MODEL` to `config/index.ts`
- [ ] Add `DEFAULT_OUTPUT_PATH` (`.chunk/context/review-prompt.md`) to `config/index.ts`
- [ ] Change `--output` default to `.chunk/context/review-prompt.md` (via `DEFAULT_OUTPUT_PATH`)
- [ ] Update `commands/build-prompt.ts` to import from `config/`
- [ ] Update `index.ts` to use `DEFAULT_OUTPUT_PATH` for `--output` default

## Phase 3: Extract Core Logic from commands/task.ts
- [ ] Create `src/core/run-setup.ts` with `runSetupWizard()`
- [ ] Create `src/core/run-trigger.ts` with `triggerRun()`
- [ ] Move `mapVcsTypeToOrgType()` and `buildProjectSlug()` to `core/run-setup.ts`
- [ ] Slim `commands/task.ts` to thin wrappers
- [ ] Update existing tests

## Phase 4: Break Up core/build-prompt.ts
- [ ] Create `src/core/build-prompt-steps.ts` with step functions:
  - `discoverTopReviewers()` — query GitHub for top reviewers by activity
  - `analyzeReviewPatterns()` — send comments to Claude for pattern analysis
  - `generatePromptFile()` — transform analysis into markdown prompt
- [ ] Refactor `extractCommentsAndBuildPrompt()` to call step functions (orchestrator handles spinners/display; step functions return data only)

## Phase 5: Rename task → run
- [ ] Rename `commands/task.ts` → `commands/run.ts`
- [ ] Update exports and function names
- [ ] Update `index.ts` command structure
- [ ] Update help text and examples
- [ ] Add hidden `task` deprecation alias
- [ ] Rename/update test files

## Phase 6: Merge skills list + status
- [ ] Remove `listSkills()` from `core/skills.ts`
- [ ] Merge `skills list` and `skills status` into single `skills list` view
- [ ] Remove `skills status` subcommand from `index.ts`
- [ ] Update `skills.unit.test.ts`

## Phase 7: Reorganize Tests
- [ ] Create directory structure under `__tests__/`
- [ ] Move test files to mirror source structure (consider combining with source moves from Phases 3/5 where possible to avoid double-churn)
- [ ] Verify imports

## Phase 8: Finalize Documentation
- [ ] Update `ARCHITECTURE.md` — remove "proposed" framing
- [ ] Update `CLI.md` — remove "proposed" framing
- [ ] Update `tasks.md` — mark complete or remove
- [ ] Update `CLAUDE.md` to reflect all changes
