# Sidecar race — cross-run comparison

Per-replicate **median TTS** (seconds) for gate `lint` + `test` / remote `test-changed`.
Aggregate row uses median of per-run medians.

| label | sidecar median TTS | CI median TTS | Δ (CI − sidecar) | sidecar LLM | CI LLM |
|------:|-------------------:|--------------:|-----------------:|------------:|-------:|
| 001 | 21.5 | 69 | +48 | $0.9299 | $1.0198 |
| 002 | 22 | 69 | +47 | $1.0271 | $0.9777 |
| 003 | 20 | 69 | +49 | $0.9319 | $0.8108 |
| 004 | 22 | 69 | +47 | $0.8676 | $0.8659 |
| 005 | 22 | 69 | +47 | $0.8875 | $1.0538 |
| **median** | **22** | **69** | **+47** | **$4.644** | **$4.7279** |

## Per-run detail

| label | arm | run_id | median TTS | p95 TTS | iters | lint fail | test fail | CI cost | sidecar est. | LLM tokens | epilogue TTS |
|------:|-----|--------|----------:|--------:|------:|----------:|----------:|--------:|-------------:|-----------:|-------------:|
| 001 | ci | 20260527-214223 | 69 | 99 | 10 | 2 | 2 | $0.1356 | $0 | 23031 | — |
| 001 | sidecar | 20260527-190102 | 21.5 | 23 | 10 | 2 | 0 | $0.0144 | $0.1374 | 19695 | 108 |
| 002 | ci | 20260527-220610 | 69 | 72 | 10 | 2 | 2 | $0.1182 | $0 | 21604 | — |
| 002 | sidecar | 20260527-200851 | 22 | 25 | 10 | 2 | 0 | $0.0156 | $0.174 | 22720 | 4 |
| 003 | ci | 20260527-222903 | 69 | 86 | 10 | 2 | 2 | $0.1242 | $0 | 17858 | — |
| 003 | sidecar | 20260527-203710 | 20 | 23 | 10 | 2 | 1 | $0.012 | $0.132 | 20865 | 61 |
| 004 | ci | 20260527-224812 | 69 | 86 | 10 | 2 | 2 | $0.1374 | $0 | 19311 | — |
| 004 | sidecar | 20260527-205229 | 22 | 23 | 10 | 2 | 0 | $0.015 | $0.1284 | 20004 | 93 |
| 005 | ci | 20260527-231212 | 69 | 84 | 10 | 2 | 2 | $0.1248 | $0 | 20783 | — |
| 005 | sidecar | 20260527-210725 | 22 | 25 | 10 | 2 | 0 | $0.0144 | $0.126 | 19475 | 93 |

## Cost totals (sum across replicates)

- Sidecar arm: n=5 — LLM $4.644, sidecar est. $0.6978, epilogue CI $0.0714
- CI arm: n=5 — LLM $4.7279, CI gates $0.6402

## Extrapolation hint

Measured medians: sidecar **22s**, CI **69s** (3.1× slower on CI, 47s saved per iteration).

```bash
./scripts/extrapolate.sh --sidecar-avg-sec 22 --ci-avg-sec 69
```
