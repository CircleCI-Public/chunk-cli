## Review Summary

**Files reviewed**: 12
**Issues identified**: 5

## Findings

### 1. `runCheck` falls through after `adapter.allow()` without terminating control flow
**`packages/hook/src/commands/exec.ts:119-130`** | **High**

In `runCheck`, after the trigger-matching block calls `adapter.allow()`, execution continues to the skip-if-no-changes block and then to `emitCheckResult`. The `adapter.allow()` call invokes `process.exit(0)` in the real adapter, but the TypeScript return type of `runCheck` is `Promise<void>`, not `Promise<never>`. If a test adapter or future adapter implementation does not call `process.exit`, the function silently falls through. The same pattern appears in `runFull` at line 289. Both `emitCheckResult` and `emitSentinelResult` are correctly typed as `never`, but the intermediate `adapter.allow()` calls in the parent functions are not guarded by a `return`.

Could the trigger-match and no-changes early-exit paths be restructured to `return adapter.allow()` (with `allow` typed as `never`), or add explicit `return` statements after each `adapter.allow()` call to make the control flow unambiguous?

### 2. `emitCheckResult` and `emitSentinelResult` are near-identical with subtle divergence
**`packages/hook/src/commands/exec.ts:150-230`** | **Medium**

`emitCheckResult` and `emitSentinelResult` share approximately 80% of their logic (the `switch` over `result.kind` for missing/pending/pass/fail). The key difference is that `emitCheckResult` accepts a `currentSessionId` parameter and has timeout logic in the `pending` case, while `emitSentinelResult` does not. This duplication means a bug fix in one path (e.g., changing the failure message format) must be replicated in the other. Per the review prompt's code organization rules, duplicate code should be extracted into shared functions.

Consider consolidating into a single function with an options parameter to control the session-aware and timeout behaviors.

### 3. `buildRunnerCommand` uses `shellQuote` inside single quotes, producing double-layered quoting
**`packages/hook/src/commands/exec.ts:413`** | **High**

`buildRunnerCommand` wraps the `--cmd` value as `--cmd '${shellQuote(flags.cmd)}'`. The `shellQuote` function escapes single quotes by replacing `'` with `'\''`. The result is then placed inside outer single quotes, producing a string like `--cmd 'echo '\''hello'\'''`. This is technically valid POSIX shell quoting for direct shell evaluation, but this string is presented to an AI agent in a block message as a suggested command to run. The agent will likely paste it into a `Bash` tool call where the outer quotes may not be interpreted the same way. Could this lead to the agent failing to re-run the command correctly when the original `--cmd` value contains single quotes?

### 4. `runTaskRun` does not validate UUID format of `definition_id` resolved from config
**`src/commands/task.ts:59-69`** | **Medium**

`runTaskRun` calls `getDefinitionByNameOrId(config, definition)` which either looks up a named definition or passes through a raw UUID. However, the resolved `definitionId` is sent directly to the CircleCI API without format validation. If `run.json` contains a malformed `definition_id` value (e.g., a typo), the error will surface as a cryptic API error rather than a clear local validation failure. The `isValidUuid` helper exists in the same file (line 133) but is only used in the interactive `config` wizard, not in the `run` path.

### 5. `expandPlaceholders` replaces unresolved placeholders with empty string silently
**`packages/hook/src/lib/placeholders.ts:105-107`** | **Medium**

When a placeholder like `{{SomeEvent.field}}` cannot be resolved from state or git context, it is replaced with an empty string and only a debug log is emitted. In a task instruction template, a silently dropped placeholder could cause a malformed command to be executed (e.g., `bun test {{CHANGED_FILES}}` becoming `bun test ` with a trailing space). Could unresolved placeholders either be left as-is (so the error is visible) or cause the expansion to fail explicitly, at least when the placeholder is in a command context?
