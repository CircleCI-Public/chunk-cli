## What

Adds `slackuser.Resolver`, the lookup from a Slack user to the CircleCI user who connected them, and `fakeslackuser`, an in-memory version for tests.

```go
UserFor(ctx, teamID, slackUserID string) (id uuid.UUID, ok bool, err error)
```

## Why the bot needs this

When someone types `@chunk plans`, the webhook Slack sends identifies them only in Slack's terms:

```
team: T024BE7LD     (the Slack workspace)
user: U07ABC123     (their Slack user ID)
text: "@chunk plans"
```

The plans API only knows CircleCI users, so the bot has to turn the Slack ID into a CircleCI user ID before it can answer "show me *my* plans". The resolver does that translation:

```
@chunk plans
     │
     ▼
Slack webhook: "user U07ABC123 said 'plans'"
     │
     ▼
Resolver: U07ABC123 → CircleCI user 3f2a9c...    ← this PR
     │
     ▼
Plans API: "plans for 3f2a9c..."
     │
     ▼
Reply in Slack with the plans
```

## How to read the answer

| Result | Meaning | What the bot should do |
|---|---|---|
| `id, ok=true` | That's CircleCI user `id` | Fetch their plans |
| `ok=false`, `err=nil` | They never connected their accounts | Reply "Connect your account" with a button |
| `err != nil` | The lookup itself failed | Reply "something went wrong, try again" |

"Not connected" is a normal answer rather than an error, so the bot can handle it with a friendly prompt.

## What's in this PR and what isn't

- **In this PR:** the interface, which is the agreed shape of the question and answer, and a fake for tests. Nothing real is looked up yet.
- **Next:** the real lookup. A spike will decide between reusing Notifications' existing Slack account linking (the Connect button under Slack on app.circleci.com/settings/user) and keeping our own table.
- **After that:** the connect flow, where a user authorizes Slack and we save "U07ABC123 = 3f2a9c...".

The interface is landing on its own so the bot's handlers can be built and tested now, without waiting on the real lookup.

## Using the fake in bot tests

```go
r := fakeslackuser.New()
r.Connect("T1", "U1", circleciUserID) // U1 in workspace T1 is connected
// any other Slack user → ok=false

r.SetError(errors.New("boom"))        // every lookup fails, to test error paths
```

## Notes

- Lookups are keyed on Slack workspace as well as user, because Slack user IDs are only unique within a workspace.
- Following `internal/testing/CLAUDE.md`, the fake takes its data up front and doesn't count calls. It's safe for concurrent use, since the bot handles events in background goroutines.
- I put the package at the top level (`slackuser/`), following how the repo does `plan.Store`. If it should live inside the bot package instead, say so in review.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
