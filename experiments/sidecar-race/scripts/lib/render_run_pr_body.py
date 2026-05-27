"""Render GitHub PR description markdown for a sidecar-race run."""
from __future__ import annotations

import csv
import json
import sys
from pathlib import Path


def _status(run_dir: Path) -> str:
    if (run_dir / "costs_summary.json").exists() and (run_dir / "results.csv").exists():
        rows = list(csv.DictReader((run_dir / "results.csv").open()))
        iters = [r for r in rows if r.get("iter", "").isdigit()]
        if iters:
            return "completed — ready for review"
    if (run_dir / "results.csv").exists():
        return "in progress"
    return "pending"


def render(run_dir: Path, meta: dict, *, harness_pr: str = "") -> str:
    run_id = meta.get("run_id", run_dir.name)
    arm = meta.get("arm", "?")
    branch = meta.get("branch", "")
    notes = meta.get("notes", "")
    status = _status(run_dir)

    lines = [
        f"## Sidecar race — run `{run_id}` ({arm} arm)",
        "",
        f"| Field | Value |",
        f"|-------|-------|",
        f"| Status | **{status}** |",
        f"| Arm | `{arm}` |",
        f"| Branch | `{branch}` |",
        f"| Run directory | `experiments/sidecar-race/results/{run_id}/` |",
    ]
    if notes:
        lines.append(f"| Notes | {notes} |")
    if harness_pr:
        lines.append(f"| Harness PR | {harness_pr} |")
    lines.extend(["", "---", ""])

    summary_path = run_dir / "summary.txt"
    costs_path = run_dir / "costs_summary.json"
    csv_path = run_dir / "results.csv"

    if status == "pending":
        lines.extend(
            [
                "### Metrics summary",
                "",
                "_Run not started yet. After `./scripts/run-arm.sh --arm "
                f"{arm}` completes, results will be committed and this PR will be marked **ready for review**._",
                "",
                "Tracked per iteration and in totals:",
                "- Wall time (TTS, lint/test job durations, sync)",
                "- CircleCI credits and USD (gate + full workflow on epilogue)",
                "- Sidecar credits estimate and USD (`SIDECAR_CREDITS_PER_MIN`)",
                "- LLM tokens and USD (zero for this harness; no Claude calls)",
                "",
            ]
        )
        return "\n".join(lines)

    if summary_path.exists():
        lines.extend(["### Summary", "", "```", summary_path.read_text().rstrip(), "```", ""])

    if costs_path.exists():
        costs = json.loads(costs_path.read_text())
        t = costs.get("totals") or {}
        lines.extend(
            [
                "### Cost totals",
                "",
                "| Metric | Value |",
                "|--------|------:|",
                f"| CI workflow credits (sum) | {t.get('ci_workflow_credits_sum', 0)} |",
                f"| CI cost USD (sum) | {t.get('ci_cost_usd_sum', 0)} |",
                f"| Sidecar credits (est. sum) | {t.get('sidecar_credits_est_sum', 0)} |",
                f"| Sidecar cost USD (est. sum) | {t.get('sidecar_cost_usd_sum', 0)} |",
                f"| LLM tokens | {t.get('llm_tokens_sum', 0)} |",
                f"| LLM cost USD | {t.get('llm_cost_usd_sum', 0)} |",
                "",
            ]
        )
        note = costs.get("llm_note")
        if note:
            lines.append(f"_{note}_\n")

    if csv_path.exists():
        rows = list(csv.DictReader(csv_path.open()))
        iter_rows = [r for r in rows if r.get("iter", "").isdigit()]
        epilogue_rows = [r for r in rows if r.get("iter") == "epilogue"]
        if iter_rows:
            lines.extend(
                [
                    "### Per-iteration timings & costs",
                    "",
                    "| iter | tts (s) | lint | test | lint dur | test dur | sync | CI credits | CI USD | sidecar cr. est | sidecar USD |",
                    "|-----:|--------:|:----:|:----:|---------:|---------:|-----:|-----------:|-------:|----------------:|------------:|",
                ]
            )
            for r in iter_rows:
                lines.append(
                    f"| {r.get('iter')} | {r.get('tts_seconds', '')} | {r.get('lint_ok', '')} | "
                    f"{r.get('test_ok', '')} | {r.get('lint_duration_s', '')} | "
                    f"{r.get('test_duration_s', '')} | {r.get('sync_duration_s', '') or '—'} | "
                    f"{r.get('ci_workflow_credits', '') or '—'} | {r.get('ci_cost_usd', '') or '—'} | "
                    f"{r.get('sidecar_credits_est', '') or '—'} | {r.get('sidecar_cost_usd', '') or '—'} |"
                )
            lines.append("")
        if epilogue_rows:
            e = epilogue_rows[0]
            lines.extend(
                [
                    "### Epilogue (final push → CI)",
                    "",
                    f"- TTS: {e.get('tts_seconds')}s — gate lint={e.get('lint_ok')} test={e.get('test_ok')}",
                    f"- CI credits: {e.get('ci_workflow_credits', '—')} — USD: {e.get('ci_cost_usd', '—')}",
                    "",
                ]
            )

    ep_path = run_dir / "epilogue.json"
    if ep_path.exists():
        ep = json.loads(ep_path.read_text())
        wf = ep.get("workflow") or {}
        jobs = wf.get("jobs") or {}
        if jobs:
            lines.extend(
                [
                    "### Full `ci` workflow jobs (epilogue)",
                    "",
                    "| job | status | duration (s) | credits est | cost USD est |",
                    "|-----|--------|-------------:|------------:|-------------:|",
                ]
            )
            for name, info in sorted(jobs.items()):
                lines.append(
                    f"| {name} | {info.get('status', '')} | {info.get('duration_s', '')} | "
                    f"{info.get('credits_est', '')} | {info.get('cost_usd_est', '')} |"
                )
            lines.append("")

    metrics_path = run_dir / "metrics.jsonl"
    if metrics_path.exists():
        lines.append(f"_Event log: `{metrics_path.relative_to(run_dir.parent.parent)}` ({metrics_path.name})_\n")

    lines.extend(
        [
            "---",
            "",
            "Reproduce: see [experiments/sidecar-race/README.md](https://github.com/CircleCI-Public/chunk-cli/blob/experiment/sidecar-race/experiments/sidecar-race/README.md).",
        ]
    )
    return "\n".join(lines)


def main() -> None:
    run_dir = Path(sys.argv[1])
    meta_path = run_dir / "run.json"
    meta = json.loads(meta_path.read_text()) if meta_path.exists() else {"run_id": run_dir.name}
    harness_pr = sys.argv[2] if len(sys.argv) > 2 else ""
    print(render(run_dir, meta, harness_pr=harness_pr))


if __name__ == "__main__":
    main()
