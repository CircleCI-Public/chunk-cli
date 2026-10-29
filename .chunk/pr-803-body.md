## Summary

* A chunk watch started from a freshly rebuilt binary replaced the daemon a running chunk factory build was using, which killed the run: the build printed "no such session" and watch showed no runs. 

* EnsureRunning now asks a daemon from another build for its sessions and leaves it alone while any is still running. It is replaced on the first call after its runs end, and a daemon that cannot answer is replaced as before.

## Test Plan

- [x] go test -race ./internal/chunkd/ ./internal/cmd/ and task lint pass
- [x] New tests in internal/chunkd/client_test.go: a daemon from another build with a running session is kept, and hasRunningSessions is false for finished-only sessions, an old daemon without the session API, and nothing listening
- [x] The keep test fails without the fix (the old code stops the fake daemon, which is the test process)
- [x] Manual: start chunk factory build, touch dist/chunk so it counts as a new build, open chunk watch; the run keeps going and shows in watch. After the run ends, the next chunk watch replaces the daemon

