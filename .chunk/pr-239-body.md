## Summary

Restores the durability #188 lost by moving factory-bot's work queue into factory. Stacked on #188.

Each event is now saved to Postgres before Slack gets its 200 (503 if the save fails, so Slack resends), and a worker hands it to the `Handler` from there. If the pod crashes, the event stays in the table and is handled after the restart.

Same guarantees as factory-bot's triager: up to 3 deliveries, a 300s budget per event, one at a time, `FAILED` for inspection, and shutdown waits for running handlers. The one difference is that the worker runs inside the slackbot service instead of a separate one.

## Changes

- `internal/workqueue`: moved from factory-bot, Postgres backend only, with tests and comments brought in line with this repo's rules
- `V0011__queue_slackbot_events.sql`: the queue table, same shape as factory-bot's
- `api/slackbot`: `events.post` queues events instead of starting a goroutine
- `cmd/slackbot`: connects to Postgres (`POSTGRESQL_FACTORY_URI`) and runs the worker
- `api/slackbot/AGENTS.md`: the FACT-700 known-limit section is replaced with how the queue works

Not here: deploying (FACT-696 will need `POSTGRESQL_FACTORY_URI`), skipping Slack's redeliveries by `event_id`, and removing factory-bot's copy.

## Test plan

- [x] Acceptance tests on a real database: a mention is handled through the queue, and an event queued while the service is down is handled once it starts
- [x] A failed handler is retried, an event the app ignores isn't queued, and Slack gets a 503 when the queue can't be written
- [x] factory-bot's workqueue tests pass against Postgres, and the 16 request-check cases from #188 still pass
- [x] V0011 applies with Flyway on top of `main`, and CI's `test-migrator` passes
- [ ] After FACT-696: a mention sent during a pod restart is still handled

FACT-700
