## Review Summary

**Files reviewed**: 6
**Issues identified**: 3

## Findings

### 1. Race condition in state file read-modify-write cycle
**`packages/hook/src/lib/state.ts:68-78`** | **High**

Could `saveEvent` corrupt the state file if two hook processes fire concurrently for the same project? The function reads the file, modifies the in-memory object, then writes it back -- a classic TOCTOU race. If two events (e.g., `PreToolUse` and `UserPromptSubmit`) trigger hooks simultaneously, one write could silently overwrite the other's data. Consider an atomic write strategy (write to a temp file then rename) or file locking.

### 2. `handleLoad` exits the process on missing field, but other subcommands do not
**`packages/hook/src/commands/state.ts:97`** | **High**

When `load` cannot find the requested field, it calls `process.exit(1)`. This makes `runState` impossible to test in-process (the test runner would exit) and is inconsistent with `save` and `clear`, which return normally on no-ops. Could this return a status or throw a typed error instead, letting the caller decide the exit code? The doc comment says exit code 1 is for "infra errors", but a missing field is arguably normal control flow, not an infrastructure failure.

### 3. `activateScope` couples to `process.cwd()` making it difficult to test deterministically
**`packages/hook/src/commands/scope.ts:826-827`** | **Medium**

The "CWD trust" path reads `process.cwd()` directly inside `activateScope`. The test suite works around this by using `process.cwd()` as the `projectDir` argument, which means the CWD-trust tests are tautological -- they always pass because the condition `process.cwd() === projectDir` is guaranteed true when you pass `process.cwd()` as the argument. Would it be worth injecting the CWD (e.g., as an optional parameter defaulting to `process.cwd()`) so tests can exercise the trust boundary with a controlled value that differs from the actual CWD?
