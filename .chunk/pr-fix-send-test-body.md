## Summary

`main` fails `TestSSendsCommentsFromThePlan`. #814's test expects the button to read `s Send 1 comment to Claude`, but #821, which merged first, renamed it to `to chunk`. Each PR passed CI on its own; together they don't. This updates the test's expected text.

## Test plan

- [ ] `go test -race ./internal/ui/plan/` passes
- [ ] CI on `main` goes green after merge

🤖 Generated with [Claude Code](https://claude.com/claude-code)
