## Summary

In `chunk factory plan`, `s` sends your queued comments to Claude from the plan pane, so you no longer have to tab to the chat and back each round. The action bar button now reads `s Send N comments to Claude`. A message drafted in the chat isn't sent.

## Test plan

- [ ] Comment on the plan with `c`, press `s`: Claude gets the comments and the plan pane keeps focus
- [ ] Type a message in the chat first: `s` leaves it in the chat
- [ ] Press `s` while Claude is answering: comments are kept and a warning shows
- [ ] `go test -race ./internal/ui/...` and `task lint` pass

🤖 Generated with [Claude Code](https://claude.com/claude-code)
