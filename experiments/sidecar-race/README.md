# Sidecar vs CI race experiment

Measure **time to signal** and **compute per iteration** when an AI coding agent validates changes via Chunk sidecar microbuilds (snapshot-backed) versus pushing to CircleCI for the same gate checks.

This directory is scaffolding only. **Do not treat results under `results/` as published data until a recorded run completes on a child branch.**

## When to merge (read this first)

**Do not merge `experiment/sidecar-race` into `main` until every planned run is finished** (sidecar arm, CI arm, and any reruns you care about). The open PR can stay a draft while you work.

1. Run the experiment on **run branches** (below) branched from `experiment/sidecar-race`.
2. Collect and review results (including sidecar **epilogue** CI validation).
3. **Then** merge `experiment/sidecar-race` → `main` if you want the tooling public.

`main` should stay free of experiment runs and `internal/racefixture/` until you are ready.

## Branching strategy

| Branch | Purpose |
|--------|---------|
| `experiment/sidecar-race` | Harness, docs, task bank — **base for all runs** |
| `experiment/sidecar-race--run-<id>-sidecar` | Sidecar arm + final CI epilogue |
| `experiment/sidecar-race--run-<id>-ci` | CI arm (fresh from harness; per-task push) |
| `experiment/sidecar-race--run-<id>-combined` | Optional: both arms interleaved |

Run branches use a **double hyphen** (`--run-`) so they never collide with the harness ref `experiment/sidecar-race` and old polluted `experiment/sidecar-race-run-*` names are easy to spot and delete.

```bash
git fetch origin
git checkout experiment/sidecar-race

# Sidecar arm
git checkout -b experiment/sidecar-race--run-001-sidecar
git push -u origin HEAD

# CI arm (from harness again, not from the sidecar run branch)
git checkout experiment/sidecar-race
git checkout -b experiment/sidecar-race--run-001-ci
git push -u origin HEAD
```

## What counts as “same signal”

| Arm | Per-iteration | After sidecar run (epilogue) |
|-----|----------------|------------------------------|
| Sidecar | `chunk sidecar sync` + `chunk validate --remote lint` + `test-changed` | **Commit tasks 1–10 → push → poll `lint` + `test` + full `ci` workflow** |
| CI | Commit → push → poll `lint` + `test` each task | (N/A — CI *is* the inner loop) |

Gate jobs (`lint`, `test`) are the primary comparison. The epilogue also records the **full `ci` workflow** (shellcheck, acceptance-test, build-smoke-test, etc.) to confirm pipeline-level confidence.

## Agent edits and LLM tokens

Each iteration runs **Claude Agent SDK locally** (`run-agent-task.sh` → `scripts/lib/agent_task.py`):

1. Optional **seed patch** (task 1 only) sets up broken state
2. Agent applies the task **`agent_prompt`** from `task-bank/manifest.json`
3. Tokens and cost are appended to `results/<run-id>/agent_usage.jsonl` and rolled up to `llm_usage.json`

Use **`--replay-patches`** on `run-arm.sh` only to debug validation timing without spending tokens (applies `task-bank/*.patch` instead of the agent).

Patches remain the **oracle** for `verify-task-bank.sh`, not the default run path.

Override model: `SIDECAR_RACE_AGENT_MODEL=...` or `agent_model` in `manifest.json`.

## Prerequisites

- `chunk` CLI, `task`, `uv` on PATH locally
- **`ANTHROPIC_API_KEY`** (or `chunk auth set anthropic`) for Agent SDK runs
- `uv sync --project experiments/sidecar-race` (installs `claude-agent-sdk`)
- **`uv` (and Go toolchain) on the sidecar snapshot** — install before `chunk sidecar snapshot create`, then set `validation.sidecarImage`
- `chunk auth status` + `CIRCLE_TOKEN` (sidecar epilogue and CI arm)
- `.chunk/config.json` with `lint` and `test-changed` commands
- Run branch checked out locally (see below); first **push** creates the remote branch

## Pull requests (one per run arm)

Do **not** open a PR before the run. A **draft** PR is created automatically on the first commit pushed to the run branch:

| Arm | First commit | Draft PR opens |
|-----|----------------|----------------|
| CI | Task 1 (`git push` in `ci-iter.sh`) | After first push |
| Sidecar | Epilogue (`tasks 1–10` commit + push) | After epilogue push |

When `run-arm.sh` finishes, it commits results, refreshes the PR body, and marks it **ready for review**.

| Phase | PR state | Body |
|-------|----------|------|
| First push on run branch | Draft | Run in progress (`run.json` only) |
| Post-run (`run-arm.sh` end) | Ready for review | `summary.txt`, `costs_summary.json`, per-iter table |

Manual update after a partial run:

```bash
export RUN_ID=<timestamp-from-new-run>
./scripts/open-run-pr.sh --run-id 001 --arm sidecar --update --commit-results
```

Set `SIDECAR_CREDITS_PER_MIN` and `CIRCLECI_CREDIT_USD` before runs for cost columns (see `finalize-metrics.sh`).

## Running the sidecar arm

```bash
git checkout experiment/sidecar-race--run-001-sidecar
cd experiments/sidecar-race
./scripts/prep-check.sh --arm sidecar
./scripts/run-arm.sh --arm sidecar --notes "run 001 sidecar"
```

`run-arm.sh` will:

1. Reset to a clean tree (task 1)
2. Warm the sidecar (`sync` + remote `lint`)
3. Run tasks 1–10 (agent edit → sync → remote gates)
4. **Epilogue:** verify tree passes shellcheck + `task lint` + tests locally → commit cumulative state → push → poll gate jobs + full `ci` workflow (must pass) → `epilogue.json`

Skip epilogue: `./scripts/run-arm.sh --arm sidecar --no-epilogue`  
Epilogue only: `./scripts/sidecar-epilogue.sh` (after a partial run, set `RUN_ID`)

## Running the CI arm

```bash
git checkout experiment/sidecar-race--run-001-ci
cd experiments/sidecar-race
./scripts/prep-check.sh --arm ci
./scripts/run-arm.sh --arm ci --notes "run 001 ci"
```

## Results layout

| File | Contents |
|------|----------|
| `results/<run-id>/results.csv` | Per-iteration rows + `iter=epilogue` row |
| `results/<run-id>/epilogue.json` | Gate + full workflow job outcomes (sidecar only) |
| `results/<run-id>/run.json` | Metadata; includes `epilogue` when present |
| `results/<run-id>/summary.txt` | From `summarize-run.sh` |

## Related docs

- [Getting started — sidecars](../../docs/GETTING_STARTED.md#sidecars)
- [chunk-sidecar skill](../../skills/chunk-sidecar/SKILL.md)
