## Review Summary

**Files reviewed**: 7
**Issues identified**: 4

## Findings

### 1. Missing `return` before `adapter.allow()` obscures control flow
**`packages/hook/src/index.ts:85-88`** | **Medium**

Throughout the entry point, early-exit guard clauses call `adapter.allow()` without a preceding `return`. For example:

```typescript
if (!isEnabled(name)) {
    log(TAG, `Exec "${name}" not enabled, allowing`);
    adapter.allow();
}
```

While `adapter.allow()` is typed as `never` (it calls `process.exit(0)`), omitting `return` makes the intent ambiguous to readers and means correctness depends entirely on the runtime behavior of `process.exit`. If the adapter implementation were ever swapped to one that does not terminate the process (e.g., a test double that records the call), execution would silently fall through into the main logic. This pattern is repeated in `registerExec`, `registerTask`, `registerSync`, and the matcher/scope guards -- at least 12 call sites. Could a `return adapter.allow()` convention prevent a subtle fall-through bug if the adapter contract changes?

---

### 2. Unvalidated regex from `--matcher` can crash the process
**`packages/hook/src/index.ts:101`** | **High**

The `--matcher` option is passed directly to `new RegExp(opts.matcher)` without a try/catch. If a user provides an invalid regex pattern (e.g., `--matcher "[invalid"`), this will throw an unhandled exception and exit with code 1 instead of a user-friendly error. Since hooks are invoked by the AI agent, a crash here produces an opaque failure. The same unguarded `new RegExp` appears in `registerExec` (both `run` and `check`), `registerTask`, and `registerSync` -- five call sites total.

---

### 3. Unused variable `mergedEnv` in first integration test
**`packages/hook/src/__tests__/integration.test.ts:226`** | **Medium**

In the "exits 0 when command passes" test, `mergedEnv` is constructed but only referenced in the diagnostic `console.error` block that fires on failure. The variable `env` is passed to `runCli` while `mergedEnv` is a separate spread used only for debug logging. This is not a bug, but the diagnostic block is substantial (~15 lines of `console.error`) and reads like temporary debugging scaffolding left behind. Could this diagnostic block be removed or moved to a shared helper to keep the test focused?

---

### 4. `sentinelId` sanitization uses replace instead of validation
**`packages/hook/src/lib/sentinel.ts:55`** | **High**

The change from `name.replace(/\//g, "-")` to `name.replace(/[^a-zA-Z0-9_-]/g, "-")` is a good hardening step for path traversal prevention. However, the function still relies on a SHA-256 hash suffix for uniqueness, which means two different names that differ only in replaced characters (e.g., `foo.bar` and `foo/bar`) will produce the same `safeName` prefix but different hashes. The real concern is that sanitization-by-replacement can mask collisions in human-readable sentinel file names. Could this be paired with a validation step that rejects names containing unexpected characters early (at the CLI argument parsing layer), rather than silently normalizing them?
