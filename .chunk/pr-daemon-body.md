## Summary

A `chunk watch` started from a newly rebuilt binary replaced the daemon that a running `chunk factory build` was using, which killed the run. The build printed "no such session" and watch showed no runs.

`EnsureRunning` replaces a daemon from another build on purpose (see `BuildID`), but it never checked whether that daemon was busy. Now, before replacing it, it asks the daemon for its sessions (`GET /factory`) and leaves it alone while any run is still going.

- The daemon gets replaced on the first `EnsureRunning` call after its runs end.
- A daemon that can't answer (a build without the session API, or no reply) is replaced as before.
- The cost: while a run is going, a newer `chunk watch` talks to the older daemon, which might leave some newer fields empty. That's better than killing the run.

## Test plan

- [x] `go test -race ./internal/chunkd/ ./internal/cmd/` and `task lint` pass
- [x] New tests in `internal/chunkd/client_test.go`: a daemon from another build with a running session is kept, and `hasRunningSessions` is false for finished-only sessions, an old daemon without the session API, and nothing listening
- [x] The keep test fails without the fix (the old code stops the fake daemon, which is the test process)
- [x] Manual: start `chunk factory build`, `touch dist/chunk` so it counts as a new build, open `chunk watch`; the run keeps going and shows in watch, and the daemon is replaced by the next `chunk watch` after the run ends

🤖 Generated with [Claude Code](https://claude.com/claude-code)
