## Summary

Today we can only see when `chunk factory build` and `chunk factory plan` are invoked. We can't see whether someone who opens `chunk watch` goes on to plan, send work to the factory, and get a run. This adds Amplitude events for that path (FACT-674).

```
watch_opened
   ├─ n → draft_started → draft_sent (run_id)
   └─ p → plan_opened → plan_feedback_sent × N → plan_approved (run_id)
```

Every event from one sitting of `chunk watch` carries the same `watch_session_id`, so a funnel can join them. Plan events also carry `plan_id`, and the two events that start a run carry `run_id`.

| Event | When it fires | Properties | Answers |
|---|---|---|---|
| `watch_opened` | `chunk watch` starts | `is_remote` | Funnel entry (Q1) |
| `draft_started` | `n` is pressed | — | Draft vs. plan split (Q2) |
| `draft_sent` | The factory takes the draft | `run_id` | Runs without a plan (Q4) |
| `plan_opened` | A plan loads, new or existing (not if it fails to load) | `is_new`, `plan_id` | Plan vs. draft split (Q2), planning without sending (Q3) |
| `plan_feedback_sent` | A chat message, or a batch of saved comments | `feedback_type` (`message` / `comments`), `comment_count`, `plan_id` | Rounds of feedback before sending (Q2a) |
| `plan_approved` | The factory takes a confirmed plan. Never at ctrl+s alone, and not if the approval fails | `plan_id`, `run_id` | Plans sent to the factory (Q2, Q3), runs with a plan (Q4) |

All events also carry `watch_session_id` and `app_name`.

## Changes

- `internal/telemetry/event.go`: `telemetry.Track(ctx, name, props)` reports from a context, and `WithProperties` attaches properties every later event carries (how `watch_session_id` travels)
- `internal/telemetry/telemetry.go`: every event gets a top-level `app_name: chunk-cli`, which callers can't override. Amplitude drops the nested `Context.App` copy, so before this it never saw the app name
- Call sites in `internal/cmd/watch.go`, `internal/ui/watch/` and `internal/ui/plan/`

## Privacy

Events carry only IDs, counts and fixed values. Nothing the user typed (prompts, plan text, messages, comments) is sent, and a test asserts that.

## Not in this PR

- **PR opened / merged / closed** (Q5, Q6). The daemon already follows each run's PR, but telemetry only sends when a process exits, and the daemon runs for days. That needs its own change, which goes through the factory planning loop as a follow-up.

## Test plan

- [x] `internal/telemetry`: `Track` carries the context's properties (inner ones win), a derived context doesn't leak properties back to its parent, `Track` with no Sender or a nil context does nothing, and `app_name` can't be overridden
- [x] `internal/ui/watch`:
  - `n` sends `draft_started`, and a draft the factory takes sends `draft_sent` with its `run_id` and `watch_session_id`
  - a draft the factory refuses sends nothing
  - opening a new plan sends `plan_opened` with `is_new: true` and no `plan_id`; reopening one sends `is_new: false` with its `plan_id`; a plan that fails to load sends nothing
- [x] `internal/ui/plan`:
  - ctrl+s alone sends nothing, confirming sends `plan_approved` with `plan_id` and `run_id`, and declining or a failed approval sends nothing
  - chat messages and saved comments each send `plan_feedback_sent` with the plan's ID (including the first message, which creates the plan) and never the text written
- [x] All six events pass the Data team's `audit-segment-event` rules
- [x] `go test -race` passes for `internal/telemetry`, `internal/ui/plan`, `internal/ui/watch` and `internal/cmd`, and `task lint` reports 0 issues
- [ ] After release: events show up in Amplitude with `app_name: chunk-cli`, and luisjimenez can build the funnel

🤖 Generated with [Claude Code](https://claude.com/claude-code)
