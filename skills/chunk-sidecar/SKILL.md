---
name: chunk-sidecar
description: Use when the user says "validate on the sidecar", "run tests on the sidecar", "sync to sidecar", "sidecar dev loop", "check this on the sidecar", "validate remotely", "scaffold test-suites.yml", "set up smarter testing", "write .circleci/test-suites.yml", "run smarter testing doctor", or "diagnose smarter testing", or when you have made edits and want to verify them on a remote `chunk` sidecar instead of running locally. Also covers creating sidecars, snapshotting a configured environment, customizing the sidecar image via `chunk sidecar`, and scaffolding `.circleci/test-suites.yml` for CircleCI Smarter Testing.
version: 2.1.0
allowed-tools:
  - Bash(chunk --version)
  - Bash(chunk auth status)
  - Bash(chunk auth login)
  - Bash(chunk auth login --no-browser)
  - Bash(chunk config set:*)
  - Bash(chunk org list:*)
  - Bash(chunk sidecar:*)
  - Bash(chunk validate:*)
  - Bash(cat .chunk/config.json)
  - Bash(cat .chunk/sidecar.json)
  - Bash(test -n*)
  - Read
  - Write
  - Edit
  - Grep
  - Glob
---

# Chunk Sidecar

Run your build, test, and validate commands on a remote CircleCI-provisioned Linux environment instead of locally. The 90% job is the **sync → validate** dev loop. This skill also handles first-time setup, debugging, test-suite scaffolding, and snapshot management.

---

## Step 0 — Probe State, Then Ask

Run these probes **before anything else**. They are read-only and always safe:

```bash
chunk --version 2>&1 || echo "CHUNK_NOT_INSTALLED"
chunk auth status 2>&1
cat .chunk/config.json 2>&1 || echo "{}"
chunk sidecar current 2>&1
```

**If `chunk` is not installed:** Stop. Tell the user to install `chunk` before using this skill.

**If auth shows CircleCI "Not set":** Run the OAuth login for them — see [Logging in](#logging-in) — then re-run `chunk auth status`. Only continue once CircleCI shows "Configured".

**Otherwise, call AskUserQuestion** based on detected state (choose the block that matches):

---

### No active sidecar + no `validation.sidecarImage` in config (first time)

```
AskUserQuestion:
  header: "Sidecar intent"
  question: "No sidecar is configured yet. What would you like to do?"
  options:
    - "Walk me through first-time setup"
      → invoke the chunk-sidecar-setup skill via the Skill tool
    - "I have a snapshot ID — create from it"
      → ask user for the snapshot ID, then:
         chunk sidecar create --image <id>
         chunk sidecar sync
         → continue to Dev Loop section
    - "Diagnose what's missing"
      → go to Prerequisites section, report findings
    - "Scaffold test-suites.yml for Smarter Testing"
      → go to Scaffolding section
```

---

### No active sidecar + `validation.sidecarImage` IS set

```
AskUserQuestion:
  header: "Sidecar action"
  question: "You have a configured snapshot but no running sidecar. What would you like to do?"
  options:
    - "Launch from snapshot and start dev loop"  ← Recommended
      → chunk sidecar create --image <sidecarImage>
        chunk sidecar sync
        → continue to Dev Loop section
    - "Debug or fix something"
      → go to Troubleshooting section
    - "Scaffold test-suites.yml"
      → go to Scaffolding section
    - "Manage snapshots"
      → go to Snapshot Management section
```

---

### Active sidecar exists

```
AskUserQuestion:
  header: "Sidecar action"
  question: "Your sidecar is active. What would you like to do?"
  options:
    - "Sync and validate (dev loop)"  ← Recommended
      → go to Dev Loop section
    - "Debug or fix something"
      → go to Troubleshooting section
    - "Scaffold test-suites.yml"
      → go to Scaffolding section
    - "Manage snapshots"
      → go to Snapshot Management section
```

---

## Prerequisites

Before any `chunk sidecar` command, confirm:

1. `chunk --version` — CLI is installed and on PATH.
2. `chunk auth status` — exit code 0, CircleCI shows "Configured". If not, see [Logging in](#logging-in).
3. **OrgID** — read `cat .chunk/config.json` and check `orgID`. If missing, run `test -n "${CIRCLECI_ORG_ID:-}"`. If **both** are unset, run `chunk org list --json`: one org, use it automatically; several, show the list and ask **exactly once**; error or empty, stop and see [Logging in](#logging-in). Persist with `chunk config set orgID <id>`.

Never run `echo $CIRCLE_TOKEN`, `env`, `printenv`, or any command that exposes credential env vars.

---

## Logging in

OAuth is the default. It opens a browser, exchanges the code over PKCE, and stores the token in the system keychain — no token pasting, nothing on disk in plaintext.

```bash
chunk auth login
```

Run it with a **Bash timeout of 300000ms** — the command blocks until the browser callback lands (5 minute limit). No browser opens when the agent runs it: stdin is not a terminal, so `chunk auth login` prints the authorize URL and waits. **Relay that URL to the user and ask them to open it.** Never open a browser on their behalf.

The callback listens on `127.0.0.1` on **this** machine, so the URL only works from a browser on the same host. `--no-browser` forces the same print-and-wait behaviour when a terminal is present.

Fall back to `chunk auth set circleci` (paste a personal API token) only when OAuth is unavailable, or when the user explicitly asks for token auth. Both paths land in the keychain; `--insecure-storage` writes to `~/.config/chunk/config.json` instead.

If `chunk auth status` reports the CircleCI source as `Environment`, do **not** log in — `CIRCLE_TOKEN` is already set and takes precedence over the keychain, so a login would store a token that never gets used. Treat it as configured and move on.

---

## Dev Loop

For each round of edits:

1. `chunk sidecar sync` — pushes the local working tree (staged + unstaged) to the active sidecar. Skip if nothing changed.
2. `chunk validate` — runs configured commands. Commands marked `remote: true` in `.chunk/config.json` run on the sidecar; others run locally.
   - One command by name: `chunk validate <name>`
   - Ad-hoc remote command: `chunk validate --remote --cmd "<cmd>"`
3. Zero exit = pass. Non-zero = go to Troubleshooting.

---

## Snapshot Management

Snapshots save a configured sidecar state so future sessions boot fast.

- **Create:** `chunk sidecar snapshot create --name <name>` — captures current sidecar. The source sidecar is deleted after a successful snapshot.
- **List:** `chunk sidecar snapshot list`
- **Record in config:** `chunk config set validation.sidecarImage <snapshot-id>`
- **Launch from:** `chunk sidecar create --image <snapshot-id>` then `chunk sidecar sync`

Always snapshot after `chunk validate` passes.

---

## Scaffolding `.circleci/test-suites.yml`

CircleCI Smarter Testing splits your test suite into atoms and runs only the affected subset. Driven by `.circleci/test-suites.yml`.

### File shape

```yaml
---
name: <suite-name>
discover: <command that prints one test atom per line>
run: <command that runs `<< test.atoms >>` and writes junit XML to `<< outputs.junit >>`>
outputs:
  junit: <path/to/junit.xml>
```

### Stack templates

**Go:**
```yaml
discover: go list -f '{{ if or (len .TestGoFiles) (len .XTestGoFiles) }} {{ .ImportPath }} {{end}}' ./...
run: go tool gotestsum --junitfile="<< outputs.junit >>" -- -race << test.atoms >>
```

**Python (pytest):**
```yaml
discover: python -m pytest --collect-only -q
run: python -m pytest --junit-xml=<< outputs.junit >> << test.atoms >>
```

**Node (Jest):**
```yaml
discover: npx jest --listTests
run: JEST_JUNIT_OUTPUT_FILE=<< outputs.junit >> npx jest --reporters=default --reporters=jest-junit << test.atoms >>
```

### Validate after writing

1. `chunk sidecar sync`
2. Run doctor on the sidecar:
   ```
   chunk validate --remote --cmd 'script -q /dev/null circleci run testsuite "<suite-name>" --doctor'
   ```
3. Fix reported issues locally, sync, re-run doctor. Repeat until all checks pass.

If `circleci-testsuite` is not installed, fall back to manual validation (see v1 skill for manual steps).

---

## Troubleshooting

- **`Could not select an organization` / `no interactive terminal`** — orgID missing. Run `chunk org list --json`, auto-select if there is one org, otherwise ask once, then `chunk config set orgID <id>`.
- **Auth errors (401/403, "token invalid")** — run `chunk auth status`. If the CircleCI token is stale or missing, re-run `chunk auth login` ([Logging in](#logging-in)); otherwise follow the printed remediation.
- **Sidecar 404 on `current` / `sync` / `validate`** — sidecar was deleted externally. Run `chunk sidecar forget`, return to Step 0.
- **`permission denied (publickey)`** — run `chunk sidecar add-ssh-key --public-key-file ~/.ssh/chunk_ai.pub`. If it persists, tell user to remove `~/.ssh/chunk_ai*` to regenerate the keypair.
- **`context deadline exceeded` on SSH or API calls** — sidecar is unhealthy. If `sidecarImage` is set, create a fresh one from snapshot. Otherwise `chunk sidecar forget` and redo setup via `chunk-sidecar-setup`.
- **Missing binary on sidecar** — `chunk validate --remote --cmd "<install-command>"`. Re-snapshot after `chunk validate` passes.
- **`sync` errors about merge base** — ask user to push the branch (`git push -u origin <branch>`).
- **Snapshot `--image` won't boot** — snapshot IDs are org-scoped. Confirm both the snapshot and new sidecar are in the same org.

---

## Parallel Sessions

When `CLAUDE_SESSION_ID` is set, `chunk` auto-scopes the active-sidecar file to `.chunk/sidecar.<session-id>.json`. Two sessions in the same repo target different sidecars without conflict. Do not hand-edit those files.

## Out of Scope

- Running `chunk init` or bulk-editing `.chunk/config.json`. The skill may run `chunk config set` for `orgID` and `validation.sidecarImage` only.
- Editing files on the sidecar over SSH — they will be overwritten on the next `sync`.
