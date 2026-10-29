> **Stacked on #767.** Review that first; this PR is retargeted to `main` once it merges.

## Summary

**Open `chunk watch` and send a prompt from there.** There's no new command.

1. **Write a prompt:** press **`n`** and type the change you want.
2. **Claude drafts it:** Claude Code (Sonnet) makes the change on your machine, in the run's worktree. The right pane shows its activity live.
3. **Send to the factory (last step):** the diff appears in the pane. **enter** sends the draft and your prompt to the factory. The run appears in the dashboard, and its committed change is shown when it finishes.

The footer and the pane both show which step you're on and which key to press.

## How it's fast (~2½ min per run, down from 10+)

- **Warm sidecars:** when Claude starts drafting, `watch` asks the daemon to create the run's sidecars (`POST /factory/warm`), and the run takes them when you send.
- **Warm caches:** the daemon runs the project's checks once on the implementer's sidecar during the draft, so round 1's checks start warm. The implementer doesn't wait for this; only the checks do.
- **No wasted round:** the draft is sent with "finish this draft" guidance, so the implementer starts at once instead of after a review of an unfinished draft.
- **chunk-cli's sidecar image** now includes warm Go and lint caches: `task lint` takes 11 s on a brand-new sidecar, down from 1–3 min.

No new command: the draft goes through `factory.Start` and is sent the same way as `chunk factory build --continue`.

## Notes for reviewers

- **One commit:** the TUI flow and the speed-ups together. Happy to split the speed-ups or the sidecar image into separate PRs if that's easier to review.
- **Two judgment calls, happy to change either:** drafts use **Sonnet**, and a draft is sent with "finish this draft" guidance, so the factory **skips reviewing the unfinished draft first**.
- **The new sidecar image affects all of chunk-cli** (`chunk validate` too), not just this feature. The old image (`4c46e935…`) is still there to switch back to.
- **The draft runs on your own Claude login**, so it uses your own Claude usage.

## Test plan

- [x] TUI: each step, the live log and diff, sending, cancelling, errors, and the layout in narrow and editor (VS Code) terminals
- [x] Warm sidecars: built from the pre-draft files, handed only to the matching run, deleted if unused (real rsync to fake sidecars, plus an in-process daemon)
- [x] `task test` and `task lint` pass, and it works with #766 merged in
- [x] Manual, chunk-cli with real Claude and sidecars: passed in 1 round in ~2½ min
- [ ] Manual, React demo app
