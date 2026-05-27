# Task bank

Each **iteration** is a small, realistic edit an agent would make. **`agent_prompt`** in `manifest.json` drives real runs via Claude Agent SDK; **`patch`** files are the oracle for `verify-task-bank.sh` and optional `--replay-patches` debugging.

## Adding a task

1. Implement the change (agent or branch) and export a patch for verification:
   ```bash
   git format-patch -1 HEAD --stdout > experiments/sidecar-race/task-bank/11-my-task.patch
   ```
2. Register in `manifest.json`: `agent_prompt`, `patch`, optional `seed_patch`, `expect` (lint/test pass/fail).
3. Run on a run branch:
   ```bash
   ./scripts/run-agent-task.sh 11
   ```

## Patch rules

- One logical agent step per task.
- Cumulative state across tasks 1–10 (task 1 reset clears `internal/racefixture` only).
- Include tasks that should **fail** lint or test, then a follow-up that fixes (signal detection).

## Planned tasks

See `manifest.json`.
