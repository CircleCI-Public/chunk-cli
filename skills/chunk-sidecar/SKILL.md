---
name: chunk-sidecar
description: Use when the user says "validate on the sidecar", "run tests on the sidecar", "sync to sidecar", "sidecar dev loop", "check this on the sidecar", "validate remotely", or when you have made edits and want to verify them on a remote `chunk` sidecar instead of running locally. Also covers creating sidecars, snapshotting a configured environment, and customizing the sidecar image via `chunk sidecar`.
version: 1.1.0
allowed-tools:
  - Bash(chunk --version)
  - Bash(chunk auth status)
  - Bash(chunk sidecar:*)
  - Bash(chunk validate:*)
  - Bash(cat .chunk/config.json)
  - Bash(cat .chunk/sidecar.json)
  - Read
  - Grep
  - Glob
---

# Chunk Sidecar Skill

Run the user's build, test, and validate commands on a remote `chunk` sidecar instead of locally. The 90% job is the **sync → validate** loop. This skill also covers one-time setup (create, snapshot, environment customization).

Sidecars are ephemeral Linux environments provisioned via CircleCI. They isolate work, avoid local port conflicts, and can be reset to known-good snapshots. Your local tree is mirrored to `/workspace/<repo>` on the sidecar each time you sync.

## Step 1: Prerequisites

Run these checks in order. Stop and report to the user if anything fails.

1. `chunk --version` — confirms the CLI is installed and on PATH.
2. `chunk auth status` — validates the configured credentials. Rely on the **exit code**: non-zero means a *configured* credential failed validation. Zero does **not** mean every credential is set — a missing CircleCI or GitHub token prints "Not set" and still exits zero. Read the output: if CircleCI shows "Not set", stop and ask the user to run `chunk auth set circleci` before proceeding (the sidecar commands in Step 2 will otherwise fail with an auth error). The command's output masks tokens; do not dig into env vars yourself.

Do **not** run `echo $CIRCLE_TOKEN`, `env`, `printenv`, or any other command that reads credential environment variables. That leaks secrets into conversation context. If `chunk auth status` reports a failure or shows a required credential as "Not set", surface its printed remediation (e.g. "Run `chunk auth set circleci`") and stop.

## Step 2: Find or create the active sidecar

Run `chunk sidecar current`. Three cases:

- **It prints a sidecar** — use it; go to Step 4.
- **No active sidecar, and `validation.sidecarImage` is set in `.chunk/config.json`** — create a new sidecar from the snapshot, sync, and go straight to Step 4:
  ```
  chunk sidecar create --name <name> --org-id <orgID> --image <sidecarImage>
  chunk sidecar sync
  ```
  Read `orgID` and `validation.sidecarImage` from `.chunk/config.json`. Ask the user for the org ID if it is not present in the config.
- **No active sidecar, no `sidecarImage` configured** — full environment setup is needed. Inform the user, confirm the org ID (read from `.chunk/config.json` or ask), create a sidecar, then go to Step 3:
  ```
  chunk sidecar create --name <name> --org-id <orgID>
  ```

Always pass `--org-id` to `chunk sidecar create` — interactive org selection does not work in Claude sessions.

## Step 3: One-time setup

This step produces a reusable snapshot so future sessions boot fast. Follow it whenever a fresh sidecar has no snapshot to boot from (Step 2 case 3).

1. `chunk sidecar setup --dir . --name <name>` — detects the stack, syncs files, and runs install steps on the sidecar.
2. Verify the sidecar is working correctly: `chunk validate`. This uses per-command routing — commands marked `remote: true` run on the sidecar, the rest run locally. If any command fails with a missing binary or dependency, see Troubleshooting below, then re-run `chunk validate` until it passes.
3. Snapshot the working sidecar: `chunk sidecar snapshot create --name <snapshot-name>`. This captures the configured state and returns a snapshot ID. **Always snapshot after confirming the sidecar is working — do not skip this step.**
4. Record the snapshot ID in `.chunk/config.json`: `chunk config set validation.sidecarImage <snapshot-id>`.
5. Create a **new** sidecar from the snapshot and set it as active — this is the clean environment you will use going forward:
   ```
   chunk sidecar create --name <new-name> --org-id <orgID> --image <snapshot-id>
   chunk sidecar sync
   ```
6. Re-verify with `chunk validate` to confirm the snapshot-booted sidecar is healthy before entering the loop.

## Step 4: The tight loop

For each round of edits:

1. `chunk sidecar sync` — pushes the local working tree (including staged and unstaged changes) to the active sidecar. You do **not** need to commit or push first. Skip this call if nothing has changed locally since the last sync.
2. `chunk validate` — runs the project's configured validate commands using per-command routing: commands marked `remote: true` in `.chunk/config.json` run on the sidecar, the rest run locally.
   - One command by name: `chunk validate <name>`.
   - Ad-hoc command on the sidecar: `chunk validate --remote --cmd "<cmd>"`.
3. Read the exit code. Zero = pass. Non-zero = go to Step 5.

## Step 5: Interpreting failures

When validate returns non-zero:

- Parse stderr — `chunk validate` prints per-command headers and propagates the first non-zero exit.
- Map error paths back to local files: the sidecar mirrors your tree at `/workspace/<repo>` (or the workspace configured in `.chunk/sidecar.json`).
- Fix locally, then repeat Step 4. Do **not** edit files over SSH — changes will be overwritten on the next sync.
- If the error looks environmental (missing binary, wrong language version, unreachable service), go to Troubleshooting.

## Parallel sessions

When `CLAUDE_SESSION_ID` is set, `chunk` auto-scopes the active-sidecar file to `.chunk/sidecar.<session-id>.json`. Two concurrent sessions in the same repo target different sidecars without conflict. Do not override this behavior or hand-edit those files.

## Troubleshooting

- **`no organization configured`** — pass `--org-id <id>` explicitly to the failing command. Read it from `.chunk/config.json` (`orgID` field) or ask the user.
- **Auth errors (401/403, "token invalid", "unauthorized")** — run `chunk auth status` and follow its printed remediation (`chunk auth set circleci` / `github` / `anthropic`). Never dump env vars.
- **Sidecar 404 on `current`, `sync`, or `validate`** — the sidecar was deleted externally. Run `chunk sidecar forget`, then return to Step 2.
- **`permission denied (publickey)` on sync, ssh, or exec** — the sidecar does not have your SSH key registered. Run `chunk sidecar add-ssh-key --public-key-file ~/.ssh/chunk_ai.pub` (or pass `--public-key "<ssh-ed25519 ...>"` directly). The command requires one of those flags; invoking it bare returns "A public key is required." If the issue persists, tell the user they can remove `~/.ssh/chunk_ai*` to regenerate the keypair on next use.
- **SSH key registration or API calls time out (`context deadline exceeded`)** — the sidecar is unhealthy. If `validation.sidecarImage` is set in `.chunk/config.json`, create a fresh sidecar from the snapshot (Step 2 case 2). If not, run `chunk sidecar forget` and repeat Step 3 with a new sidecar.
- **Missing dependency or binary not on `$PATH` on the sidecar** — the environment setup steps may not have installed everything needed, or a runtime was installed to a non-standard path. Use `chunk validate --remote --cmd "<install-or-symlink-command>"` to install the missing tool or make it accessible. Once `chunk validate` passes, re-snapshot so future sidecars include it.
- **`sync` errors about merge base or upstream** — the local branch has no remote upstream. Ask the user to push the branch (`git push -u origin <branch>`) or rebase onto a tracked ref.
- **Snapshot `--image` will not boot a new sidecar** — snapshot IDs are org-scoped. Confirm the new sidecar is being created in the same org as the snapshot.

## Out of scope

This skill does **not**:

- Modify `.chunk/config.json` (that is `chunk init`'s job; user-owned).
- Install or change pre-commit hooks (that is `chunk init`).
- Run `chunk init`.
- Edit files on the sidecar over SSH — they will be wiped by the next `sync`.
