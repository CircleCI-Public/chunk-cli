---
name: smelt-sidecar
description: Use when the user says "validate on the sidecar", "run tests on the sidecar", "sync to sidecar", "sidecar dev loop", "check this on the sidecar", "validate remotely", "scaffold test-suites.yml", "set up smarter testing", "write .circleci/test-suites.yml", "run smarter testing doctor", or "diagnose smarter testing", or when you have made edits and want to verify them on a remote `smelt` sidecar instead of running locally. Also covers creating sidecars, snapshotting a configured environment, customizing the sidecar image via `smelt sidecar`, and scaffolding `.circleci/test-suites.yml` for CircleCI Smarter Testing.
version: 2.1.0
allowed-tools:
  - Bash(smelt --version)
  - Bash(smelt auth status)
  - Bash(smelt auth login)
  - Bash(smelt auth login --no-browser)
  - Bash(smelt config set:*)
  - Bash(smelt org list:*)
  - Bash(smelt sidecar:*)
  - Bash(smelt validate:*)
  - Bash(cat .smelt/config.json)
  - Bash(cat .smelt/sidecar.json)
  - Bash(test -n*)
  - Read
  - Write
  - Edit
  - Grep
  - Glob
---

# Smelt Sidecar

Run your build, test, and validate commands on a remote CircleCI-provisioned Linux environment instead of locally. The 90% job is the **sync → validate** dev loop. This skill also handles first-time setup, debugging, test-suite scaffolding, and snapshot management.

---

## Step 0 — Probe State, Then Ask

Run these probes **before anything else**. They are read-only and always safe:

```bash
smelt --version 2>&1 || echo "SMELT_NOT_INSTALLED"
smelt auth status 2>&1
cat .smelt/config.json 2>&1 || echo "{}"
smelt sidecar current 2>&1
```

**If `smelt` is not installed:** Stop. Tell the user to install `smelt` before using this skill.

**If auth shows CircleCI "Not set":** Run the OAuth login for them — see [Logging in](#logging-in) — then re-run `smelt auth status`. Only continue once CircleCI shows "Configured".

**Otherwise, call AskUserQuestion** based on detected state (choose the block that matches):

---

### No active sidecar + no `validation.sidecarImage` in config (first time)

```
AskUserQuestion:
  header: "Sidecar intent"
  question: "No sidecar is configured yet. What would you like to do?"
  options:
    - "Walk me through first-time setup"
      → invoke the smelt-sidecar-setup skill via the Skill tool
    - "I have a snapshot ID — create from it"
      → ask user for the snapshot ID, then:
         smelt sidecar create --image <id>
         smelt sidecar sync
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
      → smelt sidecar create --image <sidecarImage>
        smelt sidecar sync
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

Before any `smelt sidecar` command, confirm:

1. `smelt --version` — CLI is installed and on PATH.
2. `smelt auth status` — exit code 0, CircleCI shows "Configured". If not, see [Logging in](#logging-in).
3. **OrgID** — read `cat .smelt/config.json` and check `orgID`. If missing, run `test -n "${CIRCLECI_ORG_ID:-}"`. If **both** are unset, run `smelt org list --json`: one org, use it automatically; several, show the list and ask **exactly once**; error or empty, stop and see [Logging in](#logging-in). Persist with `smelt config set orgID <id>`.

Never run `echo $CIRCLE_TOKEN`, `env`, `printenv`, or any command that exposes credential env vars.

---

## Logging in

OAuth is the default. It opens a browser, exchanges the code over PKCE, and stores the token in the system keychain — no token pasting, nothing on disk in plaintext.

```bash
smelt auth login
```

Run it with a **Bash timeout of 300000ms** — the command blocks until the browser callback lands (5 minute limit). No browser opens when the agent runs it: stdin is not a terminal, so `smelt auth login` prints the authorize URL and waits. **Relay that URL to the user and ask them to open it.** Never open a browser on their behalf.

The callback listens on `127.0.0.1` on **this** machine, so the URL only works from a browser on the same host. `--no-browser` forces the same print-and-wait behaviour when a terminal is present.

Fall back to `smelt auth set circleci` (paste a personal API token) only when OAuth is unavailable, or when the user explicitly asks for token auth. Both paths land in the keychain; `--insecure-storage` writes to `~/.config/smelt/config.json` instead.

If `smelt auth status` reports the CircleCI source as `Environment`, do **not** log in — `CIRCLE_TOKEN` is already set and takes precedence over the keychain, so a login would store a token that never gets used. Treat it as configured and move on.

---

## Dev Loop

Once per project, after the sidecar is working, mark the commands that should run
on it. A sidecar being up routes nothing on its own: `smelt sidecar setup` marks
only `install` and gate-role commands, and a sidecar set up by hand has none
marked. `smelt validate --list` tags each command `[local|remote, role]`, so read
it first, then:

```bash
smelt validate --mark-remote           # mark all but autofix commands
smelt validate --mark-remote test      # or one at a time
```

The bare form deliberately skips `autofix` commands (formatters, codegen) and
prints which ones it left alone — those rewrite files, and on the sidecar the edits
never reach the local working tree. Mark one by name only if the user wants that.
Already-marked commands report no change, so re-running is safe, and the flag
persists in `.smelt/config.json` across sessions.

**Caveat:** this routing only applies while `validation.sidecarImage` is unset.
Once a snapshot ID is recorded there, `smelt validate` sends every command to the
sidecar regardless of marking or role — the same as `--remote`. On such a project,
run formatters directly instead of through `smelt validate`.

Then, for each round of edits:

1. `smelt sidecar sync` — pushes the local working tree (staged + unstaged) to the active sidecar. Skip if nothing changed.
2. `smelt validate` — runs configured commands. Commands marked `remote: true` in `.smelt/config.json` run on the sidecar; others run locally.
   - One command by name: `smelt validate <name>`
   - Ad-hoc remote command: `smelt validate --remote --cmd "<cmd>"`
   - Everything on the sidecar regardless of marking: `smelt validate --remote`
3. Zero exit = pass. Non-zero = go to Troubleshooting.

---

## Snapshot Management

Snapshots save a configured sidecar state so future sessions boot fast.

- **Create:** `smelt sidecar snapshot create --name <name>` — captures current sidecar. The source sidecar is deleted after a successful snapshot.
- **List:** `smelt sidecar snapshot list`
- **Record in config:** `smelt config set validation.sidecarImage <snapshot-id>`
- **Launch from:** `smelt sidecar create --image <snapshot-id>` then `smelt sidecar sync`

Always snapshot after `smelt validate` passes.

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

1. `smelt sidecar sync`
2. Run doctor on the sidecar:
   ```
   smelt validate --remote --cmd 'script -q /dev/null circleci run testsuite "<suite-name>" --doctor'
   ```
3. Fix reported issues locally, sync, re-run doctor. Repeat until all checks pass.

If `circleci-testsuite` is not installed, fall back to manual validation (see v1 skill for manual steps).

---

## Troubleshooting

- **`Could not select an organization` / `no interactive terminal`** — orgID missing. Run `smelt org list --json`, auto-select if there is one org, otherwise ask once, then `smelt config set orgID <id>`.
- **Auth errors (401/403, "token invalid")** — run `smelt auth status`. If the CircleCI token is stale or missing, re-run `smelt auth login` ([Logging in](#logging-in)); otherwise follow the printed remediation.
- **Sidecar 404 on `current` / `sync` / `validate`** — sidecar was deleted externally. Run `smelt sidecar forget`, return to Step 0.
- **`permission denied (publickey)`** — run `smelt sidecar add-ssh-key --public-key-file ~/.ssh/smelt_ai.pub`. If it persists, tell user to remove `~/.ssh/smelt_ai*` to regenerate the keypair.
- **`context deadline exceeded` on SSH or API calls** — sidecar is unhealthy. If `sidecarImage` is set, create a fresh one from snapshot. Otherwise `smelt sidecar forget` and redo setup via `smelt-sidecar-setup`.
- **Missing binary on sidecar** — `smelt validate --remote --cmd "<install-command>"`. Re-snapshot after `smelt validate` passes.
- **`smelt validate` ran everything locally** — no command is marked `remote: true`. Run `smelt validate --mark-remote` (see Dev Loop). Do not paper over it with `--remote` on every call.
- **`sync` errors about merge base** — ask user to push the branch (`git push -u origin <branch>`).
- **Snapshot `--image` won't boot** — snapshot IDs are org-scoped. Confirm both the snapshot and new sidecar are in the same org.

---

## Parallel Sessions

`smelt` scopes active-pool state to your session and branch (under `XDG_DATA_HOME`, keyed on `CLAUDE_CODE_SESSION_ID` or the Stop hook payload), so two sessions in the same repo target different pools and never sync into the same remote workspace. A session with no pool yet takes over one the project already has, unless another session owns it. Nothing to configure, and do not hand-edit those files.

## Out of Scope

- Running `smelt init` or bulk-editing `.smelt/config.json`. The skill may run `smelt config set` for `orgID` and `validation.sidecarImage` only.
- Editing files on the sidecar over SSH — they will be overwritten on the next `sync`.
