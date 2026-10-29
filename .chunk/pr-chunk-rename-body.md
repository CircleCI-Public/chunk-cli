## Summary

The `chunk factory plan` screen now says chunk where it used to say Claude: the chat replies are labelled `chunk`, and the status lines read "chunk is thinking…", "esc Stop chunk", "Send N comments to chunk" and so on. Text that names Claude Code as something to install is unchanged, and so is the new-run draft in `chunk watch`.

## Test plan

- [ ] `chunk factory plan`: replies are labelled `chunk`, and "chunk is thinking…" shows while it works
- [ ] Queue a comment: the header and button say "waiting for chunk" / "Send 1 comment to chunk"
- [ ] `go test -race ./internal/ui/...` and `task lint` pass

🤖 Generated with [Claude Code](https://claude.com/claude-code)
