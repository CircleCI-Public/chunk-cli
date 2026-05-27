# Review checklist (experiment/sidecar-race-harness)

Use this before cutting run sub-branches. **Merge to `main` only after all experiment runs are done** (see README “When to merge”).

## Harness

- [ ] Scripts are executable (`chmod +x experiments/sidecar-race/scripts/*.sh`)
- [ ] `new-run.sh --arm sidecar` and `--arm ci` create expected layout
- [ ] `apply-task.sh` fails clearly when `patch` is null (expected until task bank is filled)
- [ ] `poll-ci-gate.sh` documented env vars match your CircleCI project slug
- [ ] Gate comparison is **lint + test** only (documented in README)

## Task bank

- [x] Ten tasks in `manifest.json` cover lint fail, test fail, multi-package, and happy path
- [x] Patches added under `task-bank/*.patch` and `patch` fields set in manifest
- [x] `./scripts/verify-task-bank.sh` passes (cumulative apply; matches manifest `expect`)

## Run branches (before merge to main)

- [ ] `experiment/sidecar-race/run-001-sidecar` branched from `experiment/sidecar-race-harness` (not from `main`)
- [ ] `experiment/sidecar-race/run-001-ci` branched from `experiment/sidecar-race-harness` (fresh, not from the sidecar run branch)
- [ ] All planned iterations recorded on both arms
- [ ] Happy with results — only then merge `experiment/sidecar-race-harness` → `main`
- [ ] Sidecar snapshot ID recorded in `run.json` matches org snapshot
- [ ] CI run branch pushed to `origin` before `ci-iter.sh`
- [ ] Results committed with `git add -f` only when publishing (see `results/README.md`)

## Not in scope for scaffolding PR

- Running iterations or committing `results/*/`
- Changing `.chunk/config.json` or `.circleci/config.yml` unless required for patches
