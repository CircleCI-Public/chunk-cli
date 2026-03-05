## Review Summary

**Scope**: `git diff 80bb113..2ad0ef0` -- exec commands and related changes
**Files reviewed**: 6
**Issues found**: 2

## Findings

### 1. Missing `return` after `adapter.allow()` causes silent fall-through if exit is suppressed
**`packages/hook/src/commands/exec.ts:122`** | **High**

In `runCheck`, `adapter.allow()` is called on lines 122 and 133 without a following `return`. While `adapter.allow()` is typed as `never` (it calls `process.exit(0)`), the function continues to subsequent logic (sentinel reading, `emitCheckResult`) if `process.exit` does not actually terminate the process. The same pattern appears in `runFull` at line 327.

This is not theoretical: Node.js `process.exit` listeners can delay termination, and testing frameworks routinely stub `process.exit`. If `allow()` does not terminate, `runCheck` falls through to `emitCheckResult` which reads the sentinel and may block on a stale result -- the opposite of the intended "allow" behavior.

Suggested fix: add `return` after each `adapter.allow()` call, or restructure as early returns:

```typescript
if (triggerPatterns.length > 0 && !matchesTrigger(adapter, event, triggerPatterns)) {
    // ...
    return adapter.allow();
}
```

---

### 2. `flags.name` is interpolated unquoted into shell command string
**`packages/hook/src/commands/exec.ts:441`** | **High**

`buildRunnerCommand` quotes `flags.cmd` and `flags.fileExt` via `shellQuote`, but `flags.name` is interpolated directly into the command string without quoting:

```typescript
const parts = ["chunk hook exec run", flags.name, "--no-check"];
```

If `flags.name` contains spaces or shell metacharacters, the resulting command shown in the "no results" block message will be broken or misleading. While this string is displayed as instructions rather than executed directly, it is presented to the agent as a command to run, and a malformed command would cause the agent to fail or behave unexpectedly.

Suggested fix: apply the same `shellQuote` treatment to `flags.name`:

```typescript
const parts = ["chunk hook exec run", `'${shellQuote(flags.name)}'`, "--no-check"];
```
