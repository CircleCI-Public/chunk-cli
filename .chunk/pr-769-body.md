## still in progress...


Stacked on #767 (removes the sidecar section from chunk watch). This PR's own changes are the last three commits; review #767 first.

### Summary

Open chunk watch and send a prompt from there. No new command is used.

1. Write a prompt: press `n` and type the change you want. The right pane clears to a page for the new run; esc brings the selected run back.
2. Claude drafts it: Claude Code (Sonnet) makes the change on your machine, in the run's worktree. The right pane shows its activity live.
3. Send to the factory (last step): the diff appears in the pane. enter sends the draft and your prompt to the factory. The run appears in the dashboard, and its committed change is shown when it finishes.

The footer and the pane both show which step you're on and which key to press. The run pane now wraps long lines to its width instead of cutting them off, so diffs, check output and the outcome are readable in narrow terminals.

**How it's fast (~3 min for a small change, down from 10+):**

- Warm sidecars: when Claude starts drafting, watch asks the daemon to create the run's sidecars (POST /factory/warm), and the run takes them when you send.
- Warm caches: the daemon runs the project's checks once on the implementer's sidecar during the draft, so round 1's checks start warm. The implementer doesn't wait for this; only the checks do.
- No wasted round: the draft is sent with "finish this draft" guidance, so the implementer starts at once instead of after a review of an unfinished draft.
- chunk-cli's sidecar image now includes warm Go and lint caches: task lint takes 11 s on a brand-new sidecar, down from 1–3 min.

Larger code changes take longer, since the implementer has more to get passing and reviewers have more to read: 6–8 min for a 6–60 line Go change in chunk-cli.


### Test plan

- [x] TUI: each step, the live log and diff, sending, cancelling, errors, and the layout in narrow and editor (VS Code) terminals
- [x] Run pane: long lines, diffs and check output wrap to the pane's width, keeping colours and indent
- [x] Warm sidecars: built from the pre-draft files, handed only to the matching run, deleted if unused (real rsync to fake sidecars, plus an in-process daemon)
- [x] task test and task lint pass, and it works with #766 merged in
- [x] Manual, chunk-cli with real Claude and sidecars: README-sized changes pass in 1 round in ~3 min

## Notes for reviewers

- Three commits: the first is the whole feature (the TUI flow, warm sidecars and caches, starting the implementer early, Sonnet, the guidance, the in-pane views and the sidecar image); the second clears the run pane for a new run; the third wraps the run pane. Can split the speed-ups or the sidecar image into another PR if needed.
- Two judgment calls: drafts use Sonnet, and a draft is sent with
  "finish this draft" guidance, so the factory skips reviewing the unfinished draft first.
- The new sidecar image affects all of chunk-cli (chunk validate too), not just this
  feature. The old image (4c46e935…) is still there to switch back to.
- The draft runs on your own Claude login, so it uses your own Claude usage.
