## Review Summary

**Scope**: `git diff ea640fa^..ea640fa` (hook entry point and integration tests)
**Files reviewed**: 6
**Issues found**: 2

## Findings

### 1. `adapter.allow()` calls inside `if` blocks rely on `process.exit` but TypeScript control flow continues
**`packages/hook/src/index.ts:85-88`** | **High**

Throughout `registerExec`, `registerTask`, `registerSync`, and other registration functions, `adapter.allow()` is called inside `if` blocks without a `return` statement. While `adapter.allow()` is typed as `never` (it calls `process.exit(0)`), if the adapter implementation were ever changed to not terminate the process (e.g., for testing with a mock adapter that does not call `process.exit`), every one of these call sites would silently fall through and continue executing the rest of the action handler with potentially invalid state.

For example, in `exec run` at line 85-88:
```typescript
if (!isEnabled(name)) {
    log(TAG, `Exec "${name}" not enabled, allowing`);
    adapter.allow();
}
// execution continues here if allow() didn't terminated
```

This pattern repeats at lines 85, 100-105, 109-114, 150-153, 162-166, 169-173, 216-220, 229-233, 236-240, 296-300, 309-313, 316-320. Could a future refactoring of the adapter (e.g., for unit testing with a non-exiting mock) cause these to fall through and execute commands with missing or invalid event data?

Adding `return` after each `adapter.allow()` call would make the intent explicit and protect against this.

### 2. User-controlled `--matcher` pattern is passed directly to `new RegExp()` without validation
**`packages/hook/src/index.ts:101`** | **High**

The `--matcher` CLI option value is passed directly to `new RegExp(opts.matcher)` without any try/catch. If a user provides an invalid regex pattern (e.g., `--matcher "(["`), this will throw an uncaught exception, causing the process to crash with an unhandled error and a confusing stack trace rather than a clean error message.

This occurs at three locations: line 101 (exec run), line 161 (exec check), line 229 (task check), and line 309 (sync check).

Suggested fix: wrap the `new RegExp()` call in a try/catch and exit with a clear error message, or validate the pattern before use.
