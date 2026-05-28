# Sidecar vs CI race — findings

Five replicates per arm (labels `001`–`005`). Each replicate runs ten agent-driven tasks (Claude Agent SDK locally), then measures **time to signal (TTS)** for the same gate checks: `lint` + `test` (CI arm: push per task; sidecar arm: `chunk sidecar sync` + remote validate). Sidecar runs add a **post-run epilogue** (commit all tasks → push → poll gate jobs + full `ci` workflow).

Full tables and per-run detail: [`results/comparison.md`](results/comparison.md). Raw artifacts: [`results/published/`](results/published/).

## Headline (median of per-run medians)

| Metric | Sidecar arm | CI arm |
|--------|------------:|-------:|
| **Median TTS per iteration** | **22s** | **69s** |
| Speedup | — | **3.1× slower** (~47s saved per iter on sidecar) |
| LLM cost (5 runs) | $4.64 | $4.73 |
| Infra cost (5 runs) | ~$0.70 sidecar est. + $0.07 epilogue CI | ~$0.64 CI gates |

LLM spend is effectively the same across arms (same agent, same tasks). The win is **feedback latency**: remote microbuild validation on a warmed sidecar snapshot vs. a full git push + CircleCI queue per iteration.

## Method notes

- **Agent path**: `run-agent-task.sh` + task prompts in `task-bank/manifest.json` (not patch replay).
- **Comparable signal**: gate jobs `lint` and `test` on both arms; sidecar epilogue additionally records full `ci` workflow pass.
- **Run branches** (`experiment/sidecar-race--run-*`) were archival during execution and deleted after consolidation; published metrics live on `experiment/sidecar-race` (PR #370).

## Reproduce rollup

```bash
cd experiments/sidecar-race
./scripts/collect-published-results.sh   # refresh from origin run branches (if still present)
./scripts/compare-runs.sh --labels 001,002,003,004,005 --output results/comparison.md
```
