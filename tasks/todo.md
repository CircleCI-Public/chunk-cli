# Hook Implementation - TS Parity

## Implementation Order (bottom-up)

- [x] sentinel.go - Add missing fields, readSentinel, removeSentinel, block counters
- [x] git.go - New file: detectChanges, computeFingerprint, getChangedFiles
- [x] check.go - New file: evaluateSentinel, trigger matching, blockWithLimit, guardStopEvent
- [x] scope.go - File path filtering, TTL expiry, session-aware deactivate
- [x] exec.go - Full check/run with staleness, triggers, change detection, content hash
- [x] task.go - Full check with result reading, instructions, schema, timeout
- [x] sync.go - Group sentinel, sequential walk, collected issues
- [x] Tests - Integration tests for new behavior
- [x] Lint + test pass
