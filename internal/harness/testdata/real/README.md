Real output captured on 2026-10-11 (codex-cli 0.162.0, Claude Code 2.1.170)
during the harness spike, then scrubbed: IDs replaced with fixed fake UUIDs,
emails and request IDs redacted, usage history replaced with made-up numbers.

- codex-exec-pass.jsonl     `codex exec --json` turn that ran a command and replied
- codex-exec-fail.jsonl     `codex exec --json` with an unknown model (error, turn.failed)
- codex-appserver-account.jsonl  app-server replies: account/read (id 2),
                            account/rateLimits/read (id 3), account/usage/read (id 4)
- claude-stream.jsonl       `claude -p --output-format stream-json --verbose`, with a rate_limit_event
- claude-oauth-usage.json   GET /api/oauth/usage with Claude Code's own login (200)
- claude-oauth-usage-401.json  the same with the `claude setup-token` token (401)

Fixtures that belong to one harness live next to it, e.g.
`internal/harness/codex/testdata/real/`.

Do not edit these; they pin the formats the parsers must handle.
