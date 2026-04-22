---
name: chunk-sandbox
description: Use when the user says "validate on the sandbox", "run tests on the sandbox", "sync to sandbox", "sandbox dev loop", "check this on the sandbox", "validate remotely", or when you have made edits and want to verify them on a remote `chunk` sandbox instead of running locally. Also covers creating sandboxes, snapshotting a configured environment, and customizing the sandbox image via `chunk sandbox`.
version: 1.0.0
allowed-tools:
  - Bash(chunk --version)
  - Bash(chunk auth status)
  - Bash(chunk sandbox:*)
  - Bash(chunk validate:*)
  - Bash(cat .chunk/config.json)
  - Bash(cat .chunk/sandbox.json)
  - Read
  - Grep
  - Glob
---

# Chunk Sandbox Skill

Run the user's build, test, and validate commands on a remote `chunk` sandbox instead of locally. The 90% job is the **sync → validate** loop. This skill also covers one-time setup (create, snapshot, environment customization).

Sandboxes are ephemeral Linux environments provisioned via CircleCI. They isolate work, avoid local port conflicts, and can be reset to known-good snapshots. Your local tree is mirrored to `/workspace/<repo>` on the sandbox each time you sync.

## Step 1: Prerequisites

Run these checks in order. Stop and report to the user if anything fails.

1. `chunk --version` — confirms the CLI is installed and on PATH.
2. `chunk auth status` — validates the configured credentials. Rely on the **exit code**: non-zero means a *configured* credential failed validation. Zero does **not** mean every credential is set — a missing CircleCI or GitHub token prints "Not set" and still exits zero. Read the output: if CircleCI shows "Not set", stop and ask the user to run `chunk auth set circleci` before proceeding (the sandbox commands in Step 2 will otherwise fail with an auth error). The command's output masks tokens; do not dig into env vars yourself.

Do **not** run `echo $CIRCLE_TOKEN`, `env`, `printenv`, or any other command that reads credential environment variables. That leaks secrets into conversation context. If `chunk auth status` reports a failure or shows a required credential as "Not set", surface its printed remediation (e.g. "Run `chunk auth set circleci`") and stop.

## Step 2: Find the active sandbox

Run `chunk sandbox current`. Three cases:

- **It prints a sandbox** — use it; go to Step 4.
- **No active sandbox, but `chunk sandbox list` shows one or more** — ask the user which one they want and run `chunk sandbox use <id>`. Do not guess.
- **No sandboxes exist at all** — ask the user before creating one. Sandboxes consume CircleCI compute and you do not know the user's org preference. If the user confirms, run `chunk sandbox create --name <name>` (add `--org-id <id>` only if the user provided one).

## Step 3: One-time setup

Skip this step unless the user explicitly asks to "prep the sandbox", "snapshot it", "set up the environment", or similar. This is a one-time flow that produces a reusable snapshot so future sessions boot fast.

1. `chunk sandbox env` — detects the tech stack and emits a JSON environment spec.
2. Review the spec with the user.
3. `chunk sandbox env | chunk sandbox build --tag <image-tag>` — writes `Dockerfile.test` and builds an image.
4. `chunk sandbox create --name <name> --image <image-tag>` — creates a sandbox from that image.
5. Install any extra deps over SSH: `chunk sandbox ssh -- bash -c "<install commands>"`.
6. `chunk sandbox snapshot create --name <checkpoint-name>` — captures the configured state and returns a snapshot ID.

Future sessions boot from the snapshot: `chunk sandbox create --name <new-name> --image <snapshot-id>`.

## Step 4: The tight loop

For each round of edits:

1. `chunk sandbox sync` — pushes the local working tree (including staged and unstaged changes) to the active sandbox. You do **not** need to commit or push first. Skip this call if nothing has changed locally since the last sync.
2. `chunk validate --remote` — runs the project's configured validate commands on the active sandbox. The `--remote` flag tells validate to use `.chunk/sandbox.json`; without it, validate runs locally.
   - One command by name: `chunk validate --remote <name>`.
   - Ad-hoc command: `chunk validate --remote --cmd "<cmd>"`.
3. Read the exit code. Zero = pass. Non-zero = go to Step 5.

## Step 5: Interpreting failures

When validate returns non-zero:

- Parse stderr — `chunk validate` prints per-command headers and propagates the first non-zero exit.
- Map error paths back to local files: the sandbox mirrors your tree at `/workspace/<repo>` (or the workspace configured in `.chunk/sandbox.json`).
- Fix locally, then repeat Step 4. Do **not** edit files over SSH — changes will be overwritten on the next sync.
- If the error looks environmental (missing binary, wrong language version, unreachable service), go to Troubleshooting.

## Parallel sessions

When `CLAUDE_SESSION_ID` is set, `chunk` auto-scopes the active-sandbox file to `.chunk/sandbox.<session-id>.json`. Two concurrent sessions in the same repo target different sandboxes without conflict. Do not override this behavior or hand-edit those files.

## Troubleshooting

- **`no organization configured`** — pass `--org-id <id>` explicitly to the failing command. Ask the user for the org ID; do not guess.
- **Auth errors (401/403, "token invalid", "unauthorized")** — run `chunk auth status` and follow its printed remediation (`chunk auth set circleci` / `github` / `anthropic`). Never dump env vars.
- **Sandbox 404 on `current`, `sync`, or `validate`** — the sandbox was deleted externally. Run `chunk sandbox forget`, then `chunk sandbox use <id>` or create a new one (with user confirmation).
- **`permission denied (publickey)` on sync, ssh, or exec** — the sandbox does not have your SSH key registered. Run `chunk sandbox add-ssh-key --public-key-file ~/.ssh/chunk_ai.pub` (or pass `--public-key "<ssh-ed25519 ...>"` directly). The command requires one of those flags; invoking it bare returns "A public key is required." If the issue persists, tell the user they can remove `~/.ssh/chunk_ai*` to regenerate the keypair on next use.
- **`sync` errors about merge base or upstream** — the local branch has no remote upstream. Ask the user to push the branch (`git push -u origin <branch>`) or rebase onto a tracked ref.
- **Snapshot `--image` will not boot a new sandbox** — snapshot IDs are org-scoped. Confirm the new sandbox is being created in the same org as the snapshot.

## Out of scope

This skill does **not**:

- Modify `.chunk/config.json` (that is `chunk init`'s job; user-owned).
- Install or change pre-commit hooks (that is `chunk init`).
- Run `chunk init`.
- Edit files on the sandbox over SSH — they will be wiped by the next `sync`.
