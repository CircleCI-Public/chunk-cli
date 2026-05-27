# Task bank

Each **iteration** is a small, realistic edit an agent would make while implementing a feature or fixing review feedback. Patches keep runs reproducible across arms and sub-branches.

## Adding a task

1. Create the change on a throwaway branch and export a patch:
   ```bash
   git format-patch -1 HEAD --stdout > experiments/sidecar-race/task-bank/01-fix-test.patch
   ```
2. Register it in `manifest.json` (`patch` filename, `expect` pass/fail for lint and test).
3. On a run branch, apply before each iteration:
   ```bash
   ./scripts/apply-task.sh 1
   ```

## Patch rules

- One logical agent step per patch (single concern).
- Patches apply cleanly on top of the previous iteration **or** reset the tree to a known base between tasks (document which in `run.json` `notes`).
- Include at least one task that should **fail** lint or test first, then a follow-up task that fixes it (validates signal detection).

## Planned tasks

See `manifest.json`. Patches are not committed until authors add them; the manifest lists intent only.
