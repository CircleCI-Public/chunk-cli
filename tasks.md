# Implementation Tasks

Checklist for the codebase and CLI restructuring. See `ARCHITECTURE.md` and `CLI.md` for target state.

## Phase 1: Documentation
- [x] Write `ARCHITECTURE.md` (proposed)
- [x] Write `CLI.md` (proposed)
- [x] Write `tasks.md`

## Phase 2: Consolidate Defaults
- [ ] Move `DEFAULT_ANALYZE_MODEL` and `DEFAULT_PROMPT_MODEL` from `commands/build-prompt.ts` to `config/index.ts`
- [ ] Add `DEFAULT_OUTPUT_PATH` (`.chunk/context/review-prompt.md`) to `config/index.ts`
- [ ] Change `--output` default to `.chunk/context/review-prompt.md` (via `DEFAULT_OUTPUT_PATH`)
- [ ] Update `commands/build-prompt.ts` to import model defaults from `config/`
- [ ] Update `index.ts` to import model defaults and `DEFAULT_OUTPUT_PATH` from `config/` (currently imports `DEFAULT_ANALYZE_MODEL` / `DEFAULT_PROMPT_MODEL` from `commands/build-prompt.ts`)
- [ ] Note: Changing the `--output` default from `./review-prompt.md` to `.chunk/context/review-prompt.md` is a user-facing behavior change — document in release notes

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
- [ ] Update `index.ts` command structure: `chunk run` triggers directly (with `--definition`/`--prompt` flags), `chunk run setup` is the setup wizard
- [ ] Update help text and examples (including the "Next steps" output in `runTaskConfig` which currently prints `chunk task run --definition ...`)
- [ ] Add hidden `task` deprecation alias
- [ ] Rename/update test files

## Phase 6: Merge skills list + status
- [ ] Merge `skills list` and `skills status` into single `skills list` view that shows: skill name, description, and per-agent install state (current/outdated/missing)
- [ ] Remove standalone `listSkills()` from `core/skills.ts` (fold into `getSkillsStatus()` or a new combined function)
- [ ] Remove `skills status` subcommand from `index.ts`
- [ ] Update `commands/skills.ts`: remove `runSkillsList()` and `runSkillsStatus()`, replace with single `runSkillsList()` that renders the merged view
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
