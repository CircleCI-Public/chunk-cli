# Queue Slack events before handling them (FACT-700)

Repo: circleci/factory. Base branch: `jesse/slackbot-receiver` (PR #188), not `main`. Open the PR against that branch so it stacks on #188.

## Why

`api/slackbot/events.go` `events.post` answers Slack with a 200, then runs the `Handler` in a bare `go func` with `context.WithoutCancel`. So an event is lost if the pod restarts mid-handle (Slack has had its 200 and won't resend), a failed handler is only logged, nothing bounds how many run at once, and shutdown doesn't wait for them. `api/slackbot/AGENTS.md` records this as "Known limit: events are not queued yet (FACT-700)". FACT-694 (`@chunk plans`) is blocked on it.

factory-bot already solved this for its triager with a Postgres-backed work queue. Move it here as close to verbatim as possible, changing only import paths, the test fixture and the dropped SQS backend, so the review is "same code, new home".

## Scope

1. **Port `internal/workqueue` from circleci/factory-bot** into `internal/workqueue` here: `workqueue.go`, `postgres.go`, `application.go`, `README.md`, `AGENTS.md`, `CLAUDE.md`, and the tests for the queue and the Postgres backend.
   - Leave out the SQS backend (`sqs.go`, `sqs_test.go`) and `rabbitmq_test.go`. Drop `"sqs"` from `realBackends` and anything that only exists for SQS (`fakesqs`, `ceilSQSSeconds` notes).
   - factory-bot's tests use its `pgtest` fixture. Use this repo's `internal/testing/dbtest` instead.
   - Update the README's test index to match the files that were ported.
2. **Migration** `migrator/sql/V0011__queue_slackbot_events.sql`: the same table shape as factory-bot's `migrator/sql/V0001__queue_triager_slack_events.sql` (its columns are dictated by `postgres.go`), named `queue_slackbot_events`. Follow `migrator/sql/AGENTS.md`: new file only, verify with Flyway the way CI does.
3. **`events.post`**: once the request is verified and `dispatch` says the event is handled, add it to the queue instead of starting a goroutine. If adding fails, answer 503, so Slack retries, the same as factory-bot's `api/triager/slack.go`. Events the app skips are not queued.
4. **The queue's handler** calls the existing `Handler` (`Mention` / `Joined`). `Handler`, `Mention`, `Joined`, `ThreadAnchor` and `Command` keep their current shape.
5. **`cmd/slackbot/main.go`**: connect to Postgres the way `cmd/conductor/main.go` does (`POSTGRESQL_FACTORY_URI`, `postgresql.Load`), load the backend and queue as factory-bot's `cmd/triager/main.go` does, and pass the queue to `slackbot.New`. Settings, with a comment on each like factory-bot's: max deliveries 3, concurrency 1, retry delay 1s, a handle timeout.
6. **Docs**: delete the "Known limit: events are not queued yet (FACT-700)" section in `api/slackbot/AGENTS.md` and replace it with a short note on how events are queued, and update the slackbot section of `ARCHITECTURE.md`. Update the `Handler` doc comment in `api/slackbot/handler.go`, which says calls are not durable or retried.
7. Add the slackbot's Postgres setting to its Helm values / deploy config wherever conductor's is set, if the slackbot is deployed from this repo yet.

## Acceptance

Acceptance tests in `acceptance/slackbot`, on a real database via `dbtest`:

- A verified mention is handled exactly once.
- A handler that fails is retried, and after 3 failed deliveries the event is retired to `FAILED`, not retried forever.
- An event queued before a restart is handled after it.
- When the queue can't be written, Slack gets a 503.
- An event the app doesn't act on is not queued.
- The existing 16 rejection cases still pass unchanged.

All tests run with `-race`. The ported workqueue tests pass against Postgres.

## Out of scope

- The SQS and RabbitMQ backends.
- Skipping Slack's redeliveries by `event_id` (a follow-up).
- Raising concurrency above 1.
- Any change to signature checking, the event types or the `Handler` interface.
- Hanabel's `@chunk plans` handler (#221), which stacks on this.
