## Summary

**Part 2** of [FACT-549](https://linear.app/circleci/issue/FACT-549/support-simple-interactive-prompts). Part 1 is #731.

- **Why:** #731 lets a developer hand the factory a first pass made in Claude Code or Codex. This lets them make that first pass without leaving chunk.
- **This PR:** run with no prompt in a terminal, `chunk factory start` opens a TUI. You type a prompt, Claude Code makes a single first pass and you see the diff. Then you hand it off or keep it.
- **Next:** following up with more prompts before handing off, in a separate ticket.

1. **Type the prompt.** Enter sends it, and Alt+Enter adds a new line.
2. **Claude Code makes a first pass** in the run's worktree, on your machine with your own login. What it does is shown as it goes, and Ctrl-C stops it and keeps its work.
3. **See the diff:** Claude's summary and a per-file diff stat.
4. **Enter hands it to the factory**, as `chunk factory build --continue` would. **q** keeps the worktree and prints the `--continue` command. A pass with no changes can only be kept.

### Design notes

- **The factory doesn't change.** The TUI hands over the same `factory.Handoff` as `start` and `--diff`.
- **Permissions:** `claude -p --permission-mode acceptEdits`. Edits in the worktree are accepted. Anything else is left to the developer's own Claude Code permissions, since no one is there to approve it, and the git commands that would hide the work are disallowed. It doesn't use `--dangerously-skip-permissions` on a developer's machine.
- **No questions at hand-off:** the hand-off skips "what should it do differently?", since the pass was just made for the prompt.
- **Scripts are unaffected:** the TUI only opens when stdout is a terminal, so `cd "$(chunk factory start)"` still asks for the prompt as an argument.
- **No Claude Code installed:** the command is refused before the TUI opens, with a suggestion to make the first pass yourself.

## Test plan

**Automated**
- [x] A first pass leaves Claude's edits in the run's worktree and leaves your checkout untouched (fake `claude` on PATH)
- [x] A failed pass reports why: a non-zero exit, an error result or no result
- [x] TUI: typing, an empty prompt, cancelling, stopping Claude, keeping a pass, handing off, and refusing to hand off a pass with no changes
- [x] Hand-off reaches the daemon as `--continue` with no guidance
- [x] `task test` and `task lint` pass

**Manual**
- [x] prompt → pass → q, driven in a pty against the built binary with a fake `claude`
- [ ] A real Claude pass → Enter → refined to passing on real sidecars
- [ ] Ctrl-C while Claude works keeps the run and prints `--continue`
