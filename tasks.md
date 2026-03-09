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
- [ ] Resolve `build-prompt` flag semantics so implementation, help text, and README all agree:
  - `--org` omitted → auto-detect org from git remote
  - `--org` provided + `--repos` omitted → analyze all repos in the org
- [ ] Note: Changing the `--output` default from `./review-prompt.md` to `.chunk/context/review-prompt.md` is a user-facing behavior change — document in release notes

## Phase 3: Extract Core Logic from commands/task.ts
- [ ] Create `src/core/task-config.ts` with `runTaskConfigWizard()`
- [ ] Create `src/core/task-run.ts` with `runTask()`
- [ ] Move `mapVcsTypeToOrgType()` and `buildProjectSlug()` to `core/task-config.ts`
- [ ] Slim `commands/task.ts` to thin wrappers
- [ ] Update existing tests

## Phase 4: Break Up core/build-prompt.ts
- [ ] Create `src/core/build-prompt-steps.ts` with step functions:
  - `discoverTopReviewers()` — query GitHub for top reviewers by activity
  - `analyzeReviewPatterns()` — send comments to Claude for pattern analysis
  - `generatePromptFile()` — transform analysis into markdown prompt
- [ ] Refactor `extractCommentsAndBuildPrompt()` to call step functions (orchestrator handles spinners/display; step functions return data only)

## Phase 5: Tighten task command UX
- [ ] Keep `task` as the top-level command group and preserve `chunk task config` / `chunk task run`
- [ ] Update help text and examples so the `task` command tree is clearer and more consistent
- [ ] Align any "Next steps" output in `runTaskConfig` with the finalized help text and examples
- [ ] Rename extracted core symbols/files as needed to match `task-*` naming consistently
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
- [ ] Add CLI help/usage tests covering `chunk task config` and `chunk task run`
- [ ] Add an import-boundary check so forbidden upward imports fail CI

## Phase 8: Finalize Documentation
- [ ] Update `ARCHITECTURE.md` — remove "proposed" framing
- [ ] Update `CLI.md` — remove "proposed" framing
- [ ] Update `tasks.md` — mark complete or remove
- [ ] Update `CLAUDE.md` to reflect all changes
- [ ] Update `README.md` examples and command reference to match the final CLI
- [ ] Add release notes / migration notes for:
  - new default output path for `build-prompt`
  - finalized `build-prompt` auto-detection behavior
