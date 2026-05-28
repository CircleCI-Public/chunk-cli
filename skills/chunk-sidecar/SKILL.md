---
name: chunk-sidecar
description: Use when the user says "validate on the sidecar", "run tests on the sidecar", "sync to sidecar", "sidecar dev loop", "check this on the sidecar", "validate remotely", "scaffold test-suites.yml", "set up smarter testing", "write .circleci/test-suites.yml", "run smarter testing doctor", or "diagnose smarter testing", or when you have made edits and want to verify them on a remote `chunk` sidecar instead of running locally. Also covers creating sidecars, snapshotting a configured environment, customizing the sidecar image via `chunk sidecar`, and scaffolding `.circleci/test-suites.yml` for CircleCI Smarter Testing.
version: 1.6.0
allowed-tools:
  - Bash(chunk --version)
  - Bash(chunk auth status)
  - Bash(chunk config set:*)
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

# Chunk Sidecar Skill

Run the user's build, test, and validate commands on a remote `chunk` sidecar instead of locally. The 90% job is the **sync → validate** loop. This skill also covers one-time setup (create, snapshot, environment customization).

Sidecars are ephemeral Linux environments provisioned via CircleCI. They isolate work, avoid local port conflicts, and can be reset to known-good snapshots. Your local tree is mirrored to `~/workspace/<repo>` on the sidecar each time you sync — the absolute path depends on the SSH user's home (e.g. `/home/circleci/workspace/<repo>` on `cimg/*` images, `/home/user/workspace/<repo>` on the default Ubuntu template). To see the resolved workspace, run `chunk sidecar current --json` and read the `workspace` field.

`CIRCLE_TOKEN` is forwarded over SSH automatically, so authenticated CircleCI API calls work out of the box once a token is configured locally.

The bare default sidecar image does **not** include the `circleci` CLI or the `circleci-testsuite` Smarter Testing plugin. If your validate commands need either, install them during one-time setup (Step 3) and snapshot the result so future sidecars boot with them ready. A snapshot built from a `cimg/*` base does not include `circleci-testsuite` either.

## Step 1: Prerequisites

Run these checks in order. Stop and report to the user if anything fails.

1. `chunk --version` — confirms the CLI is installed and on PATH.
2. `chunk auth status` — validates the configured credentials. Rely on the **exit code**: non-zero means a *configured* credential failed validation. Zero does **not** mean every credential is set — a missing CircleCI or GitHub token prints "Not set" and still exits zero. Read the output: if CircleCI shows "Not set", stop and ask the user to run `chunk auth set circleci` before proceeding (the sidecar commands in Step 2 will otherwise fail with an auth error). The command's output masks tokens; do not dig into env vars yourself.

Do **not** run `echo $CIRCLE_TOKEN`, `env`, `printenv`, or any other command that reads credential environment variables. That leaks secrets into conversation context. If `chunk auth status` reports a failure or shows a required credential as "Not set", surface its printed remediation (e.g. "Run `chunk auth set circleci`") and stop.

3. **CircleCI orgID preflight** — run **before any** `chunk sidecar` subcommand. Interactive org selection does not work in agent sessions.

   1. `cat .chunk/config.json` and confirm `orgID` is set to a non-empty value.
   2. If `orgID` is absent from the config, check whether `CIRCLECI_ORG_ID` is set **without printing its value** (e.g. `test -n "${CIRCLECI_ORG_ID:-}"`). Do not run `printenv`, `env`, or `echo` on credential variables.
   3. If **both** are unset, **stop** — do not call `chunk sidecar create`, `chunk sidecar list`, or any other `chunk sidecar` command. Ask the user for their org ID **exactly once** using this message:

      > No CircleCI orgID found in `.chunk/config.json`. Sidecar cannot select an org in a non-interactive session. Please provide your CircleCI org ID. To persist it for future sessions, I will run: `chunk config set orgID <id>`

      After the user provides the ID, run `chunk config set orgID <id>`, then continue to Step 2.
   4. If only `CIRCLECI_ORG_ID` is set (config still lacks `orgID`), `chunk sidecar create` will detect it automatically. Persisting with `chunk config set orgID <id>` is recommended but not required before Step 2.

## Step 2: Find or create the active sidecar

Run `chunk sidecar current`. Three cases:

- **It prints a sidecar** — use it; go to Step 4.
- **No active sidecar, and `validation.sidecarImage` is set in `.chunk/config.json`** — create a new sidecar from the snapshot, sync, and go straight to Step 4:
  ```
  chunk sidecar create --image <sidecarImage>
  chunk sidecar sync
  ```
  Read `validation.sidecarImage` from `.chunk/config.json`; orgID is resolved automatically from config or `CIRCLECI_ORG_ID`.
- **No active sidecar, no `sidecarImage` configured** — full environment setup is needed. Inform the user, then go to Step 3:
  ```
  chunk sidecar create
  ```

`chunk sidecar create` resolves orgID automatically: first from `.chunk/config.json`, then from `CIRCLECI_ORG_ID`. The Step 1 preflight guarantees one of those is set before this point. `--name` is optional; a random adjective-adverb-noun name (e.g. `happy-quickly-tesla`) is generated automatically if omitted.

## Step 3: One-time setup

This step produces a reusable snapshot so future sessions boot fast. Follow it whenever a fresh sidecar has no snapshot to boot from (Step 2 case 3).

1. `chunk sidecar setup --dir .` — detects the stack, syncs files, and runs install steps on the sidecar. Pass `--name <name>` if you want a specific name; otherwise one is generated automatically.
2. Verify the sidecar is working correctly: `chunk validate`. This uses per-command routing — commands marked `remote: true` run on the sidecar, the rest run locally. If any command fails with a missing binary or dependency, see Troubleshooting below, then re-run `chunk validate` until it passes.
3. Snapshot the working sidecar: `chunk sidecar snapshot create --name <snapshot-name>`. This captures the configured state and returns a snapshot ID. **Always snapshot after confirming the sidecar is working — do not skip this step.** Snapshot names are limited to 255 characters; the CLI will reject longer names before making the API call. **The source sidecar is deleted after a successful snapshot** to avoid leaking the build instance, and local active-sidecar state is cleared — expect `chunk sidecar current` to return empty until you launch a new one in step 5. To look up snapshot IDs later, run `chunk sidecar snapshot list`.
4. Record the snapshot ID in `.chunk/config.json`: `chunk config set validation.sidecarImage <snapshot-id>`.
5. Create a **new** sidecar from the snapshot and set it as active — this is the clean environment you will use going forward:
   ```
   chunk sidecar create --image <snapshot-id>
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
- Map error paths back to local files: the sidecar mirrors your tree at `~/workspace/<repo>` (or the workspace configured in `.chunk/sidecar.json`). Run `chunk sidecar current --json` to see the resolved absolute path.
- Fix locally, then repeat Step 4. Do **not** edit files over SSH — changes will be overwritten on the next sync.
- If the error looks environmental (missing binary, wrong language version, unreachable service), go to Troubleshooting.

## Scaffolding `.circleci/test-suites.yml`

CircleCI Smarter Testing splits a project's test suite into **atoms** (independent units) and runs only the subset the platform picks for a given shard. The split is driven by `.circleci/test-suites.yml`. `chunk init` skips generating this file by default because the built-in templates only cover Go and pytest — for other stacks, write it directly.

### When to scaffold

- The user asks to "scaffold test-suites.yml", "set up smarter testing", or "write `.circleci/test-suites.yml`".
- During the validate loop, you notice `.circleci/test-suites.yml` is missing and the project has a recognizable test runner.

### File shape

```yaml
---
name: <suite-name>
discover: <shell command that prints one test atom per line>
run: <shell command that runs the atoms in `<< test.atoms >>`, writing junit XML to `<< outputs.junit >>`>
outputs:
  junit: <path/to/junit.xml>
```

CircleCI substitutes at run time:
- `<< test.atoms >>` — space-separated subset of atoms picked for this shard.
- `<< outputs.junit >>` — the path declared under `outputs.junit`; the platform ingests this file to report results.

### Choosing `discover` and `run`

An atom is the smallest independent unit the runner accepts. Match the runner's natural sharding granularity:

- **Go** — atoms are import paths.
  ```yaml
  discover: go list -f '{{ if or (len .TestGoFiles) (len .XTestGoFiles) }} {{ .ImportPath }} {{end}}' ./...
  run: go tool gotestsum --junitfile="<< outputs.junit >>" -- -race << test.atoms >>
  ```
- **Python (pytest)** — atoms are node ids.
  ```yaml
  discover: python -m pytest --collect-only -q
  run: python -m pytest --junit-xml=<< outputs.junit >> << test.atoms >>
  ```
- **Node (Jest)** — atoms are test file paths.
  ```yaml
  discover: npx jest --listTests
  run: JEST_JUNIT_OUTPUT_FILE=<< outputs.junit >> npx jest --reporters=default --reporters=jest-junit << test.atoms >>
  ```
- **Other stacks** — keep the same pattern: `discover` prints one atom per line; `run` accepts whatever subset `discover` could output. Install a junit reporter if the runner does not emit junit natively.

### Constraints

- `discover` must be deterministic and exit non-zero on error — the platform calls it once per pipeline.
- `run` must accept any subset of `discover`'s output, including a single atom and the full set. Verify locally with a hand-picked subset before committing.
- `outputs.junit` must be a junit XML file the runner actually writes. The platform reads it to report pass/fail and timing.
- `.circleci/test-suites.yml` is referenced from `.circleci/config.yml` via the `circleci-testsuite` plugin invoked directly in a test job (not via the Smarter Testing orb). After writing the suite, confirm the `circleci-testsuite exec --suite <name>` call uses the suite `name` you chose.

### After writing

Validate on the sidecar — the `circleci-testsuite` plugin and `circleci` CLI are pre-installed there, matching the CI environment.

1. `chunk sidecar sync` — push the new file to the active sidecar.
2. Run the doctor diagnostic on the sidecar:
   ```
   chunk validate --remote --cmd 'script -q /dev/null circleci run testsuite "<suite-name>" --doctor'
   ```
   Doctor validates YAML structure, the discover command, the run command with sample atoms, JUnit output, and config wiring in a single pass. Read its output and fix any reported issues locally, then sync and re-run doctor. Repeat until all checks pass.

If `circleci-testsuite` is not installed on the sidecar (see Step 3 for installing it), fall back to manual validation:

1. `chunk validate --remote --cmd "<discover-command>"` — confirm it prints one atom per line and exits zero.
2. `chunk validate --remote --cmd "<run-command with one or two atoms>"` — confirm a junit XML file appears at the `outputs.junit` path.
3. `chunk validate --remote --cmd "circleci config validate"` — confirm `.circleci/config.yml` parses and the `circleci-testsuite exec --suite <name>` call uses the suite `name` you chose.

### Beyond scaffolding

This skill covers writing `test-suites.yml` and validating it with doctor on the sidecar. For deeper Smarter Testing configuration — test impact analysis, dynamic test splitting, auto-rerun, CI job wiring, or troubleshooting doctor failures beyond YAML/command issues — see the **`circleci-smarter-testing`** skill (available as a CircleCI plugin skill via `CircleCI-Public/skills`). It provides runner-specific templates, reference docs, and a full onboarding workflow.

## Parallel sessions

When `CLAUDE_SESSION_ID` is set, `chunk` auto-scopes the active-sidecar file to `.chunk/sidecar.<session-id>.json`. Two concurrent sessions in the same repo target different sidecars without conflict. Do not override this behavior or hand-edit those files.

## Troubleshooting

- **`Could not select an organization`**, **`no interactive terminal available`**, or **`Pass --org-id instead`** — orgID was missing when a sidecar command ran. Return to Step 1 orgID preflight: ask the user exactly once, then `chunk config set orgID <id>`. Do not explore peripheral commands (`chunk config show`, `chunk --help | grep -i org`, etc.).
- **Anti-pattern: orgID absent** — if `orgID` is not in `.chunk/config.json` and `CIRCLECI_ORG_ID` is unset, do not call `chunk sidecar create` or probe the CLI for org discovery. Ask the user once and persist with `chunk config set orgID <id>`.
- **Auth errors (401/403, "token invalid", "unauthorized")** — run `chunk auth status` and follow its printed remediation (`chunk auth set circleci` / `github` / `anthropic`). Never dump env vars.
- **Sidecar 404 on `current`, `sync`, or `validate`** — the sidecar was deleted externally. Run `chunk sidecar forget`, then return to Step 2.
- **`permission denied (publickey)` on sync, ssh, or exec** — the sidecar does not have your SSH key registered. Run `chunk sidecar add-ssh-key --public-key-file ~/.ssh/chunk_ai.pub` (or pass `--public-key "<ssh-ed25519 ...>"` directly). The command requires one of those flags; invoking it bare returns "A public key is required." If the issue persists, tell the user they can remove `~/.ssh/chunk_ai*` to regenerate the keypair on next use.
- **SSH key registration or API calls time out (`context deadline exceeded`)** — the sidecar is unhealthy. If `validation.sidecarImage` is set in `.chunk/config.json`, create a fresh sidecar from the snapshot (Step 2 case 2). If not, run `chunk sidecar forget` and repeat Step 3 with a new sidecar.
- **Missing dependency or binary not on `$PATH` on the sidecar** — the environment setup steps may not have installed everything needed, or a runtime was installed to a non-standard path. Use `chunk validate --remote --cmd "<install-or-symlink-command>"` to install the missing tool or make it accessible. Once `chunk validate` passes, re-snapshot so future sidecars include it — note this deletes the current sidecar, so launch a new one from the snapshot to keep working.
- **`sync` errors about merge base or upstream** — the local branch has no remote upstream. Ask the user to push the branch (`git push -u origin <branch>`) or rebase onto a tracked ref.
- **Snapshot `--image` will not boot a new sidecar** — snapshot IDs are org-scoped. Confirm the new sidecar is being created in the same org as the snapshot. Run `chunk sidecar snapshot list` to verify available snapshot IDs.

## Out of scope

This skill does **not**:

- Hand-edit `.chunk/config.json` or run `chunk init` for bulk project setup (user-owned). The skill **may** run `chunk config set` for `orgID` (Step 1 bootstrap) and `validation.sidecarImage` (Step 3 snapshot).
- Install or change pre-commit hooks (that is `chunk init`).
- Run `chunk init`.
- Edit files on the sidecar over SSH — they will be wiped by the next `sync`.
