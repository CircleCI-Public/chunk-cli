---
name: chunk-sidecar
description: Use this skill for any work involving a remote chunk sidecar — whether that's setting one up for the first time, customizing and snapshotting it, or running the sync → validate loop against it. Trigger when the user says "validate on the sidecar", "run tests on the sidecar", "sync to sidecar", "sidecar dev loop", "check this on the sidecar", "validate remotely", "set up the sidecar", "prep the sidecar", "get the sidecar ready", "snapshot the sidecar", "create a sidecar for this repo", or any time they want to run builds, tests, or validation in a remote environment instead of locally. When in doubt, use this skill — it covers the full sidecar lifecycle.
version: 1.0.0
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

Sidecars are ephemeral Linux environments provisioned via CircleCI. They isolate work, avoid local port conflicts, and can be reset to known-good snapshots. Your local tree is mirrored to `/workspace/<repo>` on the sidecar each time you sync.

There are two distinct phases of sidecar work — read the relevant reference when you get there:

- **Setting up a sidecar** (first-time install + snapshot): `references/sidecar-setup.md`
- **Validating changes** (sync → validate loop): `references/sidecar-validate.md`

---

## Step 1: Prerequisites

Run these checks in order. Stop and report to the user if anything fails.

1. `chunk --version` — confirms the CLI is installed and on PATH.
2. `chunk auth status` — validates the configured credentials. Rely on the **exit code**: non-zero means a configured credential failed validation. Zero does **not** mean every credential is set — a missing CircleCI token prints "Not set" and still exits zero. Read the output: if CircleCI shows "Not set", stop and ask the user to run `chunk auth set circleci` before proceeding.

Do **not** run `echo $CIRCLE_TOKEN`, `env`, `printenv`, or any other command that reads credential environment variables. That leaks secrets.

---

## Step 2: Find the Active Sidecar

Run `chunk sidecar current`. Four cases:

- **It prints a sidecar** — use it; proceed to Step 3.
- **No active sidecar, but `chunk sidecar list` shows one or more** — ask the user which one they want and run `chunk sidecar use <id>`. Do not guess.
- **User has a snapshot ID and wants to boot from it** — run `chunk sidecar create --name <name> --image <snapshot-id>`. Ask the user for the name if they haven't provided one.
- **No sidecars exist at all** — this is a setup task. Read `references/sidecar-setup.md`.

---

## Step 3: Choose Your Path

Once there's an active sidecar, read the reference for what the user actually wants to do:

- **First-time setup or re-snapshotting** (installing runtimes, build tools, capturing state): read `references/sidecar-setup.md`.
- **Ongoing dev work** (syncing changes and running validate): read `references/sidecar-validate.md`.

---

## Troubleshooting

- **`no organization configured`** — pass `--org-id <id>` explicitly. Ask the user for the org ID; do not guess.
- **Auth errors (401/403, "token invalid", "unauthorized")** — run `chunk auth status` and follow its printed remediation. Never dump env vars.
- **Sidecar 404 on `current`, `sync`, or `validate`** — the sidecar was deleted externally. Run `chunk sidecar forget`, then `chunk sidecar use <id>` or create a new one (with user confirmation).
- **`permission denied (publickey)` on sync, ssh, or exec** — the sidecar does not have your SSH key registered. Run `chunk sidecar add-ssh-key --public-key-file ~/.ssh/chunk_ai.pub` (or pass `--public-key "<ssh-ed25519 ...>"` directly). If the issue persists, tell the user they can remove `~/.ssh/chunk_ai*` to regenerate the keypair on next use.
- **`sync` errors about merge base or upstream** — the local branch has no remote upstream. Ask the user to push the branch (`git push -u origin <branch>`) or rebase onto a tracked ref.
- **Snapshot `--image` will not boot a new sidecar** — snapshot IDs are org-scoped. Confirm the new sidecar is being created in the same org as the snapshot.

---

## Out of Scope

This skill does **not**:

- Modify `.chunk/config.json` (that is `chunk init`'s job; user-owned).
- Install or change pre-commit hooks (that is `chunk init`).
- Run `chunk init`.
- Edit files on the sidecar over SSH — they will be wiped by the next `sync`.
