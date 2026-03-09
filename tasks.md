# Implementation Tasks

Checklist for the codebase and CLI restructuring. See `ARCHITECTURE.md` and `CLI.md` for target state.

## Scope

This restructuring covers `src/` only. `packages/hook/` (`@chunk/hook`) is **out of scope** — it is already well-structured with its own command/lib separation, test suite, and conventions (see `packages/hook/AGENTS.md`). Its internal patterns (thin command orchestrators, pure lib functions, mirrored tests) are a useful reference for the target state of `src/`.

## Phase 1: Documentation
- [x] Write `ARCHITECTURE.md` (proposed)
- [x] Write `CLI.md` (proposed)
- [x] Write `tasks.md`

## Phase 2a: Fix Import Direction and Consolidate Defaults
The main goal here is fixing the import direction: `index.ts` (entry point) currently imports model defaults from `commands/build-prompt.ts`, which violates the proposed layering. Moving the defaults to `config/` fixes this so both files import downward. This phase is mechanical with no user-facing changes — ship it immediately.

- [ ] Move `DEFAULT_ANALYZE_MODEL` and `DEFAULT_PROMPT_MODEL` from `commands/build-prompt.ts` to `config/index.ts` — after the move, both `commands/build-prompt.ts` and `index.ts` import from `config/`
- [ ] Update `commands/build-prompt.ts` to import model defaults from `config/`
- [ ] Update `index.ts` to import model defaults from `config/`

## Phase 2b: Finalize `build-prompt` Behavior and Docs
This phase is intentionally separate from Phase 2a because it changes user-visible behavior and documentation even if the code changes are still small.

- [ ] Resolve `build-prompt` flag semantics so implementation, help text, and README all agree:
  - no flags → auto-detect org and current repo from git remote
  - `--repos` only → auto-detect org from git remote and use the provided repos
  - `--org` provided + `--repos` provided → analyze the provided repos in the provided org
  - `--org` provided + `--repos` omitted → error (no way to enumerate all repos in an org)
  - if auto-detection is needed and fails, print a clear error telling the user to pass `--org`
- [ ] Move org/repo auto-detection logic from `commands/build-prompt.ts` into `core/` — currently `commands/build-prompt.ts:31-38` calls `detectGitHubOrgAndRepo()` and applies fallback logic, which is business logic that belongs in the orchestration layer, not in a thin command wrapper
- [ ] Update `chunk build-prompt --help` examples and wording to match the finalized behavior matrix
- [ ] Update `README.md` command reference/examples for the finalized `build-prompt` behavior

## Phase 2c: Make `index.ts` a Composition Root
The goal here is to reduce CLI drift by keeping command UX definitions close to the command modules they belong to.

- [ ] Move command-specific help text, examples, parser helpers, and option definitions out of `src/index.ts` and into the corresponding `src/commands/*` modules
- [ ] Keep `src/index.ts` limited to program creation, top-level command registration, and top-level error handling
- [ ] Introduce per-command registration helpers (for example `registerBuildPromptCommand()` / `registerTaskCommands()`) as needed
- [ ] Keep `index.ts` free of command-specific defaults, validation rules, and help-copy maintenance

## Phase 3: Extract Core Logic from commands/task.ts ⬅ highest-value phase
- [ ] Create `src/core/task-config.ts` with `runTaskConfigWizard()`
- [ ] Create `src/core/task-run.ts` with `runTask()`
- [ ] Move `mapVcsTypeToOrgType()` and `buildProjectSlug()` to `core/task-config.ts`
- [ ] Slim `commands/task.ts` to thin wrappers
- [ ] Update existing tests — place new test files in `src/__tests__/core/` to start the mirrored structure

## Phase 4: Tighten task command UX
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

## Phase 5: Merge skills list + status
- [ ] Merge `skills list` and `skills status` into single `skills list` view that shows: skill name, description, and per-agent install state (current/outdated/missing)
- [ ] Remove standalone `listSkills()` from `core/skills.ts` (fold into `getSkillsStatus()` or a new combined function)
- [ ] Remove `skills status` subcommand from `index.ts`
- [ ] Update `commands/skills.ts`: remove `runSkillsList()` and `runSkillsStatus()`, replace with single `runSkillsList()` that renders the merged view
- [ ] Update `skills.unit.test.ts`

## Phase 6: Change Default Output Path
This is a user-facing breaking change — ship it separately from the mechanical refactors above.

- [ ] Add `DEFAULT_OUTPUT_PATH` (`.chunk/context/review-prompt.md`) to `config/index.ts`
- [ ] Change `--output` default to `.chunk/context/review-prompt.md` (via `DEFAULT_OUTPUT_PATH`)
- [ ] Auto-create parent directories for the chosen `build-prompt` output path if they don't exist
- [ ] Add a deprecation warning: if `./review-prompt.md` exists in the working directory when using the new default path, print a one-time notice so users with scripts referencing the old path are aware
- [ ] Update `index.ts` to import `DEFAULT_OUTPUT_PATH` from `config/`
- [ ] Document the change in release notes

## Phase 7: Add Enforcement and Help Tests
These can be added incrementally as each phase lands, but should all be in place by the end.

- [ ] Add an import-boundary check so the documented machine-enforced rule fails CI when violated. Suggested approach: a unit test that checks `src/storage/`, `src/api/`, `src/review_prompt_mining/`, `src/ui/`, `src/utils/`, and `src/skills/` for imports from `commands/` or `core/`, and checks that `src/config/` imports nothing from the rest of `src/`. Keep lateral leaf-to-leaf imports as review guidance rather than part of the first enforcement pass.
- [ ] Add CLI help/usage tests covering `chunk build-prompt`, `chunk task config`, and `chunk task run`
- [ ] Add at least one help/usage test for the top-level `chunk` command so `index.ts` stays a thin composition root and command registration drift is caught earlier
- [ ] Place new test files in the mirrored `src/__tests__/` structure; move existing test files into the structure opportunistically when touching them for other phases (avoid a dedicated big-bang move)

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

## Decided Against

### Break up `core/build-prompt.ts`
Previously proposed as a phase to extract step functions into `core/build-prompt-steps.ts`. Decided against because:
- The file is ~300 lines and reads clearly top-to-bottom with ASCII section dividers
- The three steps already delegate to `review_prompt_mining/` modules — extracting them would create thin wrappers around single calls
- Splitting would mean reading two files instead of one with no real testability gain
- Can revisit if the file grows significantly or a concrete testing need arises

### Big-bang test reorganization
Previously proposed as a dedicated phase to move all test files at once into a mirrored directory structure. Decided against because:
- Moving all 8 test files in one pass adds churn for marginal benefit
- Better to place new test files in the right structure as each phase lands, and move existing ones opportunistically when touching them
