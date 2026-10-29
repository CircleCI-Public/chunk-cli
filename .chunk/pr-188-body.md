## Summary

Moves the Slack events receiver from factory-bot into factory as a new **`slackbot`** service, so the chunk Slack app lives next to the plans it will show.

```
"@chunk plans" in Slack
        │
        ▼
Slack POSTs to /slack/events
        │
        ▼
slackbot checks the signature and timestamp   (bad or stale → 400)
        │
        ▼
answers Slack 200 straight away               (Slack retries anything slower than 3s)
        │
        ▼
Handler.Mention(...) runs in the background
```

In factory-bot, events were forwarded to the triager (Rollbar lookups). Here they go to a `Handler` in the same process. **This is the seam FACT-694 plugs into:**

```go
type Handler interface {
    Mention(ctx context.Context, m Mention) error  // someone @mentioned the app
    Joined(ctx context.Context, j Joined) error    // the app was added to a channel
}
```

For now the binary uses `LogHandler`, which only logs, so the bot doesn't reply yet and needs no Slack bot token.

## Changes

- `cmd/slackbot`: the new binary, modeled on `cmd/mcpapi`
- `api/slackbot`: serves `POST /slack/events`, with signature and timestamp checks ported as-is
- Changed from factory-bot:
  - The bot user ID is config (`SLACK_BOT_USER_ID`), not hardcoded (`U0B3HU2G3HP`), since the app is being renamed and test apps have other IDs
  - Reads `team_id`, because looking up a Slack user's CircleCI user needs the workspace as well as the user
  - Answers Slack before handling the event, instead of waiting on the triager
- `internal/testing/fakeslack.Signature` signs test requests as Slack does, so tests sign without importing the service
- `Taskfile.yml` builds the new binary, and `ARCHITECTURE.md` and `acceptance/CLAUDE.md` document it

## Known limit: events aren't queued yet

The handler runs in a goroutine in the same process, so an event can be lost on a restart mid-handle. **#239**, stacked on this PR, fixes that by moving factory-bot's Postgres work queue into factory (FACT-700).

## Not in this PR

- **GitHub webhooks** stay in factory-bot. Its receiver also feeds PR review, so factory-bot can't be archived yet.
- **Rollbar triage** stays in factory-bot for now. It's unrelated to plans.
- **Deploying** (a `values.yml` release, Vault secrets, the Slack request URL) is FACT-696.

## Test plan

- [x] `acceptance/slackbot` starts the real binary and runs factory-bot's `TestE2E_Receiver_Slack` table (16 cases):
  - accepts valid events, a mention, an unknown outer type, and timestamps just inside the 5-minute window on both sides
  - answers Slack's URL verification challenge
  - rejects (400) a wrong-secret or forged signature, a signature without `v0=`, missing signature or timestamp headers, a non-numeric, stale or future timestamp, malformed JSON, and a body over 5 MB
- [x] `api/slackbot` checks that:
  - only mentions and the bot itself joining a channel reach the `Handler`, with team, channel, user, text and thread (checked synchronously, so a regression fails reliably)
  - a mention sent over HTTP reaches `Handler.Mention`
  - `ThreadAnchor` is the thread, or the message when it starts one
  - the signature matches a known-good fixture (verified with `openssl`)
- [x] Breaking the future-timestamp check on purpose makes the table fail
- [x] `go test -race` passes for the new packages and `acceptance/mcpapi`, and lint reports 0 issues
- [ ] After FACT-696 deploys it: Slack's request URL verifies against the service, and a mention shows up in its logs


