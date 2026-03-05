## Review Summary

**Scope**: `git diff df77ed9..80bb113` -- state and scope commands
**Files reviewed**: 5
**Issues found**: 2

## Findings

### 1. Race condition in state read-modify-write cycle
**`packages/hook/src/lib/state.ts:58-62`** | **High**

`saveEvent` performs a non-atomic read-modify-write: it calls `readState()` to parse the existing JSON, mutates the object in memory, then calls `writeFileSync()` to overwrite the file. If two hook processes fire concurrently for the same project (e.g., two `PreToolUse` events in rapid succession), one process can read stale data after the other has already started its write, causing the first process's event data to be silently dropped.

The sentinel module in this same codebase already solves this with a mkdir-based spinlock (noted in `sentinel.ts` line 15). Could `saveEvent` use the same coordination mechanism to protect the state file?

---

### 2. `handleLoad` calls `process.exit(1)` on missing field, bypassing caller control
**`packages/hook/src/commands/state.ts:101`** | **High**

When `loadField` returns `undefined`, `handleLoad` calls `process.exit(1)`. The module doc comment (lines 18-19) states exit code 1 means "infra error (cannot write file, bad args, etc.)", but a missing field is a normal runtime condition -- the requested event may simply not have been saved yet. A hook that runs `state load UserPromptSubmit.prompt` before any `UserPromptSubmit` event has fired will get a hard exit-1, which could cause the calling hook chain to treat this as a failure rather than an expected empty state.

Consider returning an empty string or a distinct exit code (e.g., 2) to distinguish "field not found" from actual infrastructure errors, or at minimum document this behavior so callers can handle it.
