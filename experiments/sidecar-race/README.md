# Sidecar vs CI race experiment

Measure **time to signal** and **compute per iteration** when an AI coding agent validates changes via Chunk sidecar microbuilds (snapshot-backed) versus pushing to CircleCI for the same gate checks.

This directory is scaffolding only. **Do not treat results under `results/` as published data until a recorded run completes on a child branch.**

## When to merge (read this first)

**Do not merge `experiment/sidecar-race` into `main` until every planned run is finished** (sidecar arm, CI arm, and any reruns you care about). The open PR can stay a draft while you work.

1. Run the experiment on **child branches** (below) branched from `experiment/sidecar-race`.
2. Collect and review results.
3. **Then** merge the harness to `main` (or close/reopen the PR) if you want the tooling public.

`main` should stay free of experiment runs and `internal/racefixture/` until you are ready.

## Branching strategy

| Branch | Purpose |
|--------|---------|
| `experiment/sidecar-race` | Harness, docs, task bank — **base for all runs; merge to `main` only after runs** |
| `experiment/sidecar-race/run-<id>-sidecar` | Execute sidecar arm only (branch **from** `experiment/sidecar-race`, not `main`) |
| `experiment/sidecar-race/run-<id>-ci` | Execute CI arm only (same base; fresh branch, not from the sidecar run branch) |
| `experiment/sidecar-race/run-<id>-combined` | Optional: same machine, both arms interleaved (control order) |

Always create run branches from `experiment/sidecar-race`:

```bash
git fetch origin
git checkout experiment/sidecar-race

# Sidecar arm
git checkout -b experiment/sidecar-race/run-001-sidecar

# CI arm (start again from experiment/sidecar-race, not from the sidecar run branch)
git checkout experiment/sidecar-race
git checkout -b experiment/sidecar-race/run-001-ci
```

Push run branches to `origin` when you need CI (CI arm) or remote backup. That does **not** require merging to `main`.

Commit **results** on the run branch (they are gitignored here by default; see `results/README.md` to opt in).

## What counts as “same signal”

Compare sidecar microbuild gates to CircleCI **`lint`** and **`test`** jobs only — the checks a developer would want before sharing code. The full `ci` workflow also runs shellcheck, acceptance-test, and build-smoke-test; record full-workflow timing separately if you want an “outer loop tax” sidebar.

| Arm | Command / trigger |
|-----|-------------------|
| Sidecar | `chunk sidecar sync` then `chunk validate lint test-changed` |
| CI | `git push` then poll until `lint` + `test` reach terminal state |

## Prerequisites

- `chunk` CLI installed and on `PATH`
- `chunk auth status` — CircleCI token configured (`chunk auth set circleci`)
- `.chunk/config.json` with `validation.sidecarImage` set (snapshot from one-time `chunk sidecar setup`)
- `CIRCLE_TOKEN` for CI polling scripts
- Run branches pushed to `origin` so CI arm can trigger pipelines

## Quick start (when ready to run)

```bash
cd experiments/sidecar-race

# 1. Initialize a run directory (creates results/<run-id>/)
./scripts/new-run.sh --arm sidecar --notes "pilot"

# 2. Apply a task-bank patch (once patches exist)
./scripts/apply-task.sh 1

# 3. Record one iteration
./scripts/sidecar-iter.sh 1
# or, on a CI run branch:
./scripts/ci-iter.sh 1

# 4. Summarize (after all iterations)
./scripts/summarize-run.sh
```

## Task bank

See `task-bank/manifest.json` for the planned iteration sequence. Add `task-bank/NN-slug.patch` files and list them in the manifest before executing a run.

## Metrics

Primary outputs land in `results/<run-id>/results.csv`. Columns are documented in `results/schema.csv`.

After a run, `summarize-run.sh` prints median/p95 time-to-signal and pass/fail agreement vs CI job outcomes (when job IDs were recorded).

## Article alignment

This experiment supports the Chunk sidecars narrative ([blog](https://circleci.com/blog/chunk-sidecars/)):

- **Time** — feedback in seconds (snapshot sidecar) vs minutes (CI gate jobs)
- **Cost** — shorter jobs × smaller effective cost per agent iteration; extrapolate with `scripts/extrapolate.sh`

## Related docs

- [Getting started — sidecars](../../docs/GETTING_STARTED.md#sidecars)
- [chunk-sidecar skill](../../skills/chunk-sidecar/SKILL.md)
