# Sandbox Validate Reference

## Before You Start

Check what validate commands are configured:
```
cat .chunk/config.json
```
This shows the commands (name, run, role) that `chunk validate` will execute. Understanding them upfront helps you interpret failures correctly — a `gate` command failing means the build is broken; a `precheck` failing is an early warning. If the file is missing or has no commands, tell the user — there's nothing to validate until `chunk init` has been run.

---

## The Sync → Validate Loop

For each round of edits:

1. **Sync** — `chunk sandbox sync` pushes the local working tree (including staged and unstaged changes) to the active sandbox at `/workspace/<repo>`. You do **not** need to commit or push first. Skip if nothing has changed locally since the last sync.

2. **Validate** — `chunk validate --remote` runs the configured commands on the sandbox.
   - Run a specific command by name: `chunk validate --remote <name>`
   - Run an ad-hoc command: `chunk validate --remote --cmd "<cmd>"`

3. **Check the exit code** — zero means all commands passed; non-zero means at least one failed. See **Interpreting Failures** below.

> **Never edit files directly on the sandbox over SSH.** Any changes made there will be overwritten the next time you sync. Always fix locally, then sync.

---

## What Validate Output Looks Like

`chunk validate` runs each configured command and prints a header before each one:

```
=== [test] task test ===
...test output...

=== [lint] task lint ===
...lint output...
```

On failure, it prints the command's stdout/stderr and exits with the command's exit code. The header tells you exactly which command failed and what it ran.

---

## Interpreting Failures

There are two categories of failure — distinguish them before fixing anything:

**Code failures** — the logic is wrong, tests are failing, lint is unhappy. The error output points to specific files and line numbers that exist in your local tree. Fix locally and re-sync.

**Environmental failures** — the sandbox is missing a binary, has the wrong runtime version, or can't reach a service. Symptoms:
- `command not found` for something that should be installed
- version mismatch errors (e.g. Go feature used that the installed version doesn't support)
- connection refused or DNS failures for external services

If the failure looks environmental, do **not** keep re-syncing. Instead:
1. SSH into the sandbox to investigate: `chunk sandbox ssh -- bash -c "<diagnostic command>"`
2. Check what's actually installed: `which <tool>`, `<tool> --version`, `cat /etc/os-release`
3. If the environment is misconfigured, the right fix is to re-run setup (see `sandbox-setup.md`) or restore from a known-good snapshot — not to patch it in place.

---

## Parallel Sessions

When `CLAUDE_SESSION_ID` is set, `chunk` auto-scopes the active-sandbox file to `.chunk/sandbox.<session-id>.json`. Two concurrent sessions in the same repo target different sandboxes without conflict. Do not override this behavior or hand-edit those files.
