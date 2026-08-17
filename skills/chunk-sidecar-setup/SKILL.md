---
name: chunk-sidecar-setup
description: Use when the user says "set up chunk sidecar", "onboard to chunk", "first time sidecar", "configure sidecar from scratch", "walk me through sidecar setup", "set up smarter testing from scratch", "sidecar onboarding", "new sidecar environment", or "I've never used a chunk sidecar before". Also invoked by the chunk-sidecar skill when first-time setup is selected. This is the interactive onboarding wizard — it covers auth, orgID, sidecar creation, dependency installation, snapshot creation, and handoff to the dev loop.
version: 1.0.0
allowed-tools:
  - Bash(chunk --version)
  - Bash(chunk auth status)
  - Bash(chunk auth login)
  - Bash(chunk auth login --no-browser)
  - Bash(chunk config set:*)
  - Bash(chunk sidecar:*)
  - Bash(chunk validate:*)
  - Bash(cat .chunk/config.json)
  - Bash(cat .chunk/sidecar.json)
  - Bash(ls .circleci/test-suites.yml)
  - Bash(test -n*)
  - Bash(git remote get-url origin)
  - Bash(basename*)
  - Read
  - Write
  - Edit
  - Grep
  - Glob
---

# Chunk Sidecar Setup (Onboarding Wizard)

This skill walks you through setting up a `chunk` sidecar from scratch. A sidecar is a remote Linux container provisioned on CircleCI. It mirrors your local working tree and runs your build, test, and validate commands in a clean, isolated environment — no local port conflicts, no dependency drift, no "works on my machine."

**What you'll have by the end:**
- Auth and credentials verified
- A running sidecar with your project's dependencies installed
- A reusable snapshot so future sessions start in seconds
- `chunk validate` confirmed passing on the sidecar

Work through the stages in order. At each decision point, call `AskUserQuestion` as specified. Do not skip ahead.

---

## Stage 1 — Check Prerequisites

Run these three probes and evaluate the results:

```bash
chunk --version 2>&1 || echo "CHUNK_NOT_INSTALLED"
chunk auth status 2>&1
cat .chunk/config.json 2>&1 || echo "{}"
```

### `chunk --version`
If the command is not found, stop and explain: "`chunk` is not installed or not on your PATH. Install it first (e.g. `brew install circleci/tools/chunk` on macOS), then re-run this skill."

### `chunk auth status`
Read the output carefully — it prints the status of each credential:

| Credential  | Required for                              |
|-------------|-------------------------------------------|
| CircleCI    | All sidecar commands (required)           |
| GitHub      | Validate commands that fetch PRs/issues   |
| Anthropic   | Validate commands that invoke Claude      |

If CircleCI shows "Not set", run the OAuth login yourself — see [Logging in](#logging-in) below — then re-run `chunk auth status` and confirm it shows "Configured" before continuing. For GitHub and Anthropic, surface that they're missing but do not block if the user's validate commands don't need them.

#### Logging in

OAuth is the default. It opens a browser, exchanges the code over PKCE, and stores the token in the system keychain — no token pasting.

```bash
chunk auth login
```

- Run with a **Bash timeout of 300000ms**: it blocks until the browser callback lands (5 minute limit). Tell the user to finish the login in the browser window that opens.
- Stdin is not a terminal under the agent, so it skips the "press Enter" prompt and opens the browser straight away.
- No browser available (headless, SSH, remote sandbox)? Use `chunk auth login --no-browser` and relay the printed URL. The callback listens on `127.0.0.1` on **this** machine, so it only works from a browser on the same host.
- If the source already reads `Environment`, `CIRCLE_TOKEN` is set and wins over the keychain. Do not log in — treat it as configured.
- `chunk auth set circleci` (paste a personal API token) is the fallback for when OAuth fails or the user asks for token auth.

**Never run `echo $CIRCLE_TOKEN`, `env`, `printenv`, or any command that exposes credential env vars.** The `chunk auth status` output masks tokens — that is the only safe way to check credentials.

### Auth check-in

After evaluating auth, ask:

```
AskUserQuestion:
  header: "Auth check"
  question: "Auth looks good — ready to continue, or do you want to re-check something?"
  options:
    - "Continue to sidecar setup"  ← Recommended
    - "Re-run chunk auth status"
      → run chunk auth status again and show output
    - "I need to set a credential"
      → for CircleCI, run chunk auth login (see Logging in);
        for GitHub or Anthropic, tell user to run chunk auth set <service>
```

---

## Stage 2 — Confirm OrgID

A CircleCI org ID (UUID) is required before creating a sidecar. Interactive org selection does not work in agent sessions, so it must be pre-configured.

1. Check `cat .chunk/config.json` for `orgID`.
2. If absent, run `test -n "${CIRCLECI_ORG_ID:-}"` (do **not** print the value).
3. If **both** are unset:

```
AskUserQuestion:
  header: "OrgID required"
  question: "Your CircleCI org ID isn't configured. What would you like to do?"
  options:
    - "I have my org ID — enter it now"
      → ask user to paste the UUID
        run: chunk config set orgID <id>
    - "How do I find my org ID?"
      → explain: "In the CircleCI web app, open Organization Settings (the gear icon
         in the left nav). Your org ID is the UUID shown at the top of the page."
        then ask for it and run: chunk config set orgID <id>
```

4. If `orgID` is already in config or env, tell the user: "OrgID is configured — good to go."

---

## Stage 3 — Name Your Sidecar (Optional)

```
AskUserQuestion:
  header: "Sidecar name"
  question: "Do you want to name your sidecar, or use an auto-generated name?"
  options:
    - "Auto-generate a name"  ← Recommended
      description: "chunk picks a fun name like 'happy-quickly-tesla'. Easy to
                    identify, no typing required."
      → proceed without --name
    - "I'll choose a name"
      description: "Good if you want something memorable like 'dev' or your username."
      → ask user for the name, then use --name <name> in Stage 4
```

---

## Stage 4 — Create the Sidecar

Create the sidecar:

```bash
chunk sidecar create [--name <name>]
```

This provisions a fresh Linux container on CircleCI. It takes 30–90 seconds. While it starts, explain: "The sidecar is starting up. Once it's ready, we'll sync your local code to it."

Then sync your local working tree:

```bash
chunk sidecar sync
```

Sync mirrors your entire local tree (including staged and unstaged changes) to `~/workspace/<repo>` on the sidecar. You never need to commit or push first — the sidecar always sees exactly what's on your disk right now.

After sync, run `chunk sidecar current --json` and show the user the resolved `workspace` path so they know where their code lives on the sidecar.

---

## Stage 5 — Install Dependencies

```
AskUserQuestion:
  header: "Dependencies"
  question: "How should we install your project's dependencies on the sidecar?"
  options:
    - "Auto-detect with chunk sidecar setup"  ← Recommended
      description: "chunk detects your stack (Go, Node, Python, etc.) and runs
                    the right install commands automatically."
      → run: chunk sidecar setup --dir .
    - "I'll specify the install commands"
      description: "Run custom commands on the sidecar (e.g. 'go mod download')."
      → ask user for commands
        run each via: chunk validate --remote --cmd "<cmd>"
    - "Skip — deps are already in the base image"
      description: "Your sidecar image already has everything installed."
      → skip to Stage 6
```

### Install circleci-testsuite (Smarter Testing)

After dependency install, check locally:

```bash
ls .circleci/test-suites.yml 2>/dev/null && echo "FOUND" || echo "NOT_FOUND"
```

```
AskUserQuestion:
  header: "Smarter Testing"
  question: "Does this project use CircleCI Smarter Testing (test-suites.yml)?"
  options:
    - "Yes — install circleci-testsuite on the sidecar"
      description: "Installs the circleci CLI and circleci-testsuite plugin so
                    you can run 'doctor' validation on the sidecar."
      → install circleci CLI and circleci-testsuite:
           chunk validate --remote --cmd "curl -fLSs https://raw.githubusercontent.com/CircleCI-Public/circleci-cli/main/install.sh | sudo bash"
           chunk validate --remote --cmd "circleci update install"
           (follow any additional circleci-testsuite install steps for the org)
    - "No — skip"
      → continue
```

If `test-suites.yml` was found locally, recommend "Yes" in the question text.

---

## Stage 6 — Run Validate

Confirm the sidecar environment is working:

```bash
chunk validate
```

`chunk validate` runs all configured commands in `.chunk/config.json`. Commands marked `remote: true` run on the sidecar; others run locally. Zero exit = everything passed.

**If it fails:**
- Read the stderr output — `chunk validate` prints per-command headers and propagates the first non-zero exit.
- Fix missing binaries: `chunk validate --remote --cmd "<install-command>"`.
- Fix local file issues: edit locally, `chunk sidecar sync`, re-run `chunk validate`.
- Repeat until it exits zero.

When it passes, tell the user: "Validate passed. Your sidecar environment is healthy."

---

## Stage 7 — Create a Snapshot

A snapshot saves the current sidecar state as a reusable image. Future sessions create a new sidecar from this snapshot and skip the whole install process — typically 30 seconds to a working environment instead of several minutes.

**Always snapshot once validate passes — do not skip this step.**

Ask the user for a name:

```
AskUserQuestion:
  header: "Snapshot name"
  question: "What should we call this snapshot?"
  options:
    - "Use repo name + today's date"
      description: "e.g. 'chunk-cli-2026-08-12' — easy to identify later."
      → construct name as <repo-basename>-<YYYY-MM-DD>
        (read repo name from: basename $(git remote get-url origin) .git)
    - "I'll choose a name"
      description: "Type a descriptive name like 'go-dev-env' or 'main-snapshot'."
      → ask user for the name
```

Create the snapshot:

```bash
chunk sidecar snapshot create --name <snapshot-name>
```

Note the snapshot ID from the output. The source sidecar is automatically deleted after a successful snapshot — that is expected behavior. Local active-sidecar state is also cleared; `chunk sidecar current` will return empty until you launch from the snapshot in Stage 8.

Persist the snapshot ID:

```bash
chunk config set validation.sidecarImage <snapshot-id>
```

---

## Stage 8 — Launch From Snapshot

Create a fresh sidecar from the snapshot — this is the clean environment you'll use going forward:

```bash
chunk sidecar create --image <snapshot-id>
chunk sidecar sync
```

Then re-verify:

```bash
chunk validate
```

This second validate confirms the snapshot boots correctly and your code is in sync.

---

## Done — You're Ready

When Stage 8 validate passes, tell the user:

> Setup complete. Here's what was configured:
>
> - OrgID: saved to `.chunk/config.json`
> - Snapshot ID: `<snapshot-id>` saved as `validation.sidecarImage` in `.chunk/config.json`
> - Active sidecar: running and passing validate
>
> **Going forward:** run `/chunk-sidecar` to start the dev loop. It will detect your snapshot and offer to launch a new sidecar automatically when needed. For the tight loop: sync → validate, repeat with each set of local edits.

---

## Troubleshooting

- **`Could not select an organization` / `no interactive terminal`** — OrgID is missing. Return to Stage 2.
- **Auth errors (401/403, "token invalid")** — run `chunk auth status`. If the CircleCI token is stale or missing, re-run `chunk auth login` ([Logging in](#logging-in)). Never dump env vars.
- **`permission denied (publickey)` on sync or exec** — run `chunk sidecar add-ssh-key --public-key-file ~/.ssh/chunk_ai.pub`. If it persists, tell the user to remove `~/.ssh/chunk_ai*` to regenerate the keypair on next use.
- **`context deadline exceeded`** — sidecar is unhealthy. Run `chunk sidecar forget` and restart from Stage 4 with a new sidecar.
- **Missing binary after `chunk sidecar setup`** — install with `chunk validate --remote --cmd "<install>"`, verify with `chunk validate`, then re-snapshot (Stage 7 onward).
- **Snapshot `--image` won't boot a new sidecar** — snapshot IDs are org-scoped. Confirm the new sidecar is being created in the same org as the snapshot. Run `chunk sidecar snapshot list` to verify available IDs.
