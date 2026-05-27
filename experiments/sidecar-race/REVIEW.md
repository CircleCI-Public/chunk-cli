# Review checklist (experiment/sidecar-race)

Use this before merging the scaffolding branch or cutting run sub-branches.

## Harness

- [ ] Scripts are executable (`chmod +x experiments/sidecar-race/scripts/*.sh`)
- [ ] `new-run.sh --arm sidecar` and `--arm ci` create expected layout
- [ ] `apply-task.sh` fails clearly when `patch` is null (expected until task bank is filled)
- [ ] `poll-ci-gate.sh` documented env vars match your CircleCI project slug
- [ ] Gate comparison is **lint + test** only (documented in README)

## Task bank

- [ ] Ten tasks in `manifest.json` cover lint fail, test fail, multi-package, and happy path
- [ ] Patches added under `task-bank/*.patch` and `patch` fields set in manifest
- [ ] Patches apply cleanly on `base_ref` from a clean tree

## Run branches (later)

- [ ] `experiment/sidecar-race/run-001-sidecar` branched from this branch
- [ ] `experiment/sidecar-race/run-001-ci` branched from this branch
- [ ] Sidecar snapshot ID recorded in `run.json` matches org snapshot
- [ ] CI run branch pushed to `origin` before `ci-iter.sh`
- [ ] Results committed with `git add -f` only when publishing (see `results/README.md`)

## Not in scope for scaffolding PR

- Running iterations or committing `results/*/`
- Changing `.chunk/config.json` or `.circleci/config.yml` unless required for patches
