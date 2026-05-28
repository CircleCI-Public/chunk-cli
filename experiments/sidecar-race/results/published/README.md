# Published experiment results

Consolidated artifacts from completed run branches (`experiment/sidecar-race--run-<label>-<arm>`).

| Directory | Arm | Label |
|-----------|-----|-------|
| `001-sidecar` … `005-sidecar` | Sidecar + epilogue | 001–005 |
| `001-ci` … `005-ci` | CI (push per task) | 001–005 |

Each directory contains the same files that were committed on its run branch under `results/<run-id>/`.

- **`index.json`** — label, arm, `run_id`, source branch, originating PR number
- Regenerate from origin: `../scripts/collect-published-results.sh`

Rollup: [`../comparison.md`](../comparison.md) · Executive summary: [`../FINDINGS.md`](../FINDINGS.md)
