"""Compare sidecar-race runs across labels and arms (sidecar vs CI)."""
from __future__ import annotations

import csv
import json
import re
import statistics
import subprocess
import sys
from dataclasses import dataclass, replace
from pathlib import Path

from llm_usage import format_usd


@dataclass
class RunSnapshot:
    label: str
    arm: str
    run_id: str
    branch: str
    source: str
    iterations: int
    median_tts_s: float | None
    p95_tts_s: float | None
    lint_fail_iters: int
    test_fail_iters: int
    ci_cost_usd: float | None
    sidecar_cost_usd: float | None
    llm_tokens: int | None
    llm_cost_usd: float | None
    epilogue_tts_s: float | None
    epilogue_workflow_ok: bool | None


def _median(vals: list[float]) -> float | None:
    if not vals:
        return None
    return float(statistics.median(vals))


def _p95(vals: list[float]) -> float | None:
    if not vals:
        return None
    ordered = sorted(vals)
    idx = max(0, int(len(ordered) * 0.95) - 1)
    return float(ordered[idx])


def _tts_from_csv(csv_path: Path) -> tuple[list[float], int, int]:
    if not csv_path.exists():
        return [], 0, 0
    rows = list(csv.DictReader(csv_path.open()))
    iter_rows = [r for r in rows if str(r.get("iter", "")).isdigit()]
    tts = []
    lint_fails = test_fails = 0
    for r in iter_rows:
        raw = r.get("tts_seconds") or ""
        try:
            tts.append(float(raw))
        except (TypeError, ValueError):
            pass
        if r.get("lint_ok") == "fail":
            lint_fails += 1
        if r.get("test_ok") == "fail":
            test_fails += 1
    return tts, lint_fails, test_fails


def _snapshot_from_dir(run_dir: Path, source: str) -> RunSnapshot | None:
    meta_path = run_dir / "run.json"
    if not meta_path.exists():
        return None
    meta = json.loads(meta_path.read_text())
    branch = meta.get("branch") or ""
    arm = meta.get("arm") or ""
    label = _label_from_branch(branch, arm)
    if not label:
        return None

    tts_list, lint_fails, test_fails = _tts_from_csv(run_dir / "results.csv")
    costs = {}
    costs_path = run_dir / "costs_summary.json"
    if costs_path.exists():
        costs = json.loads(costs_path.read_text()).get("totals") or {}

    epilogue_tts = None
    epilogue_ok = None
    epilogue_path = run_dir / "epilogue.json"
    if epilogue_path.exists():
        ep = json.loads(epilogue_path.read_text())
        gate = ep.get("gate") or {}
        wf = ep.get("workflow") or {}
        epilogue_tts = float(gate.get("tts_seconds") or wf.get("tts_seconds") or 0) or None
        epilogue_ok = bool(wf.get("workflow_ok"))

    llm_tokens = costs.get("llm_tokens_sum")
    if llm_tokens is not None:
        llm_tokens = int(llm_tokens)

    return RunSnapshot(
        label=label,
        arm=arm,
        run_id=meta.get("run_id") or run_dir.name,
        branch=branch,
        source=source,
        iterations=len(tts_list),
        median_tts_s=_median(tts_list),
        p95_tts_s=_p95(tts_list),
        lint_fail_iters=lint_fails,
        test_fail_iters=test_fails,
        ci_cost_usd=_float_or_none(costs.get("ci_cost_usd_sum")),
        sidecar_cost_usd=_float_or_none(costs.get("sidecar_cost_usd_sum")),
        llm_tokens=llm_tokens,
        llm_cost_usd=_float_or_none(costs.get("llm_cost_usd_sum")),
        epilogue_tts_s=epilogue_tts,
        epilogue_workflow_ok=epilogue_ok,
    )


def _float_or_none(value) -> float | None:
    if value is None or value == "":
        return None
    return float(value)


def _label_from_branch(branch: str, arm: str) -> str:
    prefix = "experiment/sidecar-race--run-"
    suffix = f"-{arm}"
    if branch.startswith(prefix) and branch.endswith(suffix):
        return branch[len(prefix) : -len(suffix)]
    return ""


def discover_local(results_root: Path) -> list[RunSnapshot]:
    out: list[RunSnapshot] = []
    if not results_root.is_dir():
        return out
    for run_dir in sorted(results_root.iterdir()):
        if not run_dir.is_dir() or run_dir.name.startswith("."):
            continue
        if run_dir.name == "published":
            continue
        snap = _snapshot_from_dir(run_dir, source=str(run_dir))
        if snap:
            out.append(snap)
    return out


def _label_arm_from_published_name(name: str) -> tuple[str, str] | None:
    match = re.match(r"^(\d{3})-(sidecar|ci)$", name)
    if not match:
        return None
    return match.group(1), match.group(2)


def discover_published(results_root: Path) -> list[RunSnapshot]:
    published = results_root / "published"
    if not published.is_dir():
        return []
    out: list[RunSnapshot] = []
    for run_dir in sorted(published.iterdir()):
        if not run_dir.is_dir():
            continue
        parsed = _label_arm_from_published_name(run_dir.name)
        if not parsed:
            continue
        snap = _snapshot_from_dir(run_dir, source=f"published/{run_dir.name}")
        if not snap:
            continue
        label, arm = parsed
        out.append(replace(snap, label=label, arm=arm))
    return out


def discover_git(repo_root: Path, labels: list[str], arms: list[str]) -> list[RunSnapshot]:
    out: list[RunSnapshot] = []
    for label in labels:
        for arm in arms:
            branch = f"experiment/sidecar-race--run-{label}-{arm}"
            ref = f"origin/{branch}"
            try:
                subprocess.run(
                    ["git", "-C", str(repo_root), "rev-parse", "--verify", ref],
                    check=True,
                    capture_output=True,
                )
            except subprocess.CalledProcessError:
                continue
            listing = subprocess.check_output(
                [
                    "git",
                    "-C",
                    str(repo_root),
                    "ls-tree",
                    "-r",
                    "--name-only",
                    ref,
                    "experiments/sidecar-race/results",
                ],
                text=True,
            )
            run_jsons = [
                line
                for line in listing.splitlines()
                if line.endswith("/run.json")
            ]
            if not run_jsons:
                continue
            # Use the single results subdir on the branch.
            rel = run_jsons[0]
            raw = subprocess.check_output(
                ["git", "-C", str(repo_root), "show", f"{ref}:{rel}"],
                text=True,
            )
            meta = json.loads(raw)
            run_id = meta.get("run_id") or Path(rel).parent.name
            prefix = f"experiments/sidecar-race/results/{run_id}"
            snap = _snapshot_from_git(repo_root, ref, prefix, run_id)
            if snap:
                out.append(snap)
    return out


def _snapshot_from_git(repo_root: Path, ref: str, prefix: str, run_id: str) -> RunSnapshot | None:
    import tempfile

    files = ["run.json", "results.csv", "costs_summary.json", "epilogue.json", "llm_usage.json"]
    with tempfile.TemporaryDirectory(prefix="compare-runs-") as tmp:
        base = Path(tmp) / run_id
        base.mkdir(parents=True, exist_ok=True)
        for name in files:
            rel = f"{prefix}/{name}"
            try:
                content = subprocess.check_output(
                    ["git", "-C", str(repo_root), "show", f"{ref}:{rel}"],
                )
            except subprocess.CalledProcessError:
                continue
            (base / name).write_bytes(content)
        return _snapshot_from_dir(base, source=ref)


def merge_snapshots(*groups: list[RunSnapshot]) -> list[RunSnapshot]:
    by_key: dict[tuple[str, str], RunSnapshot] = {}
    for group in groups:
        for snap in group:
            by_key[(snap.label, snap.arm)] = snap
    return sorted(by_key.values(), key=lambda s: (s.label, s.arm))


@dataclass
class ArmAggregate:
    arm: str
    n: int
    median_of_medians_s: float | None
    mean_median_s: float | None
    p95_of_medians_s: float | None
    total_llm_tokens: int | None
    total_llm_cost_usd: float | None
    total_ci_cost_usd: float | None
    total_sidecar_cost_usd: float | None


def aggregate_arm(snaps: list[RunSnapshot], arm: str) -> ArmAggregate:
    rows = [s for s in snaps if s.arm == arm and s.median_tts_s is not None]
    medians = [s.median_tts_s for s in rows if s.median_tts_s is not None]
    llm_tokens = [s.llm_tokens for s in rows if s.llm_tokens is not None]
    llm_cost = [s.llm_cost_usd for s in rows if s.llm_cost_usd is not None]
    ci_cost = [s.ci_cost_usd for s in rows if s.ci_cost_usd is not None]
    sidecar_cost = [s.sidecar_cost_usd for s in rows if s.sidecar_cost_usd is not None]
    return ArmAggregate(
        arm=arm,
        n=len(rows),
        median_of_medians_s=_median(medians),
        mean_median_s=float(statistics.mean(medians)) if medians else None,
        p95_of_medians_s=_p95(medians),
        total_llm_tokens=sum(llm_tokens) if llm_tokens else None,
        total_llm_cost_usd=sum(llm_cost) if llm_cost else None,
        total_ci_cost_usd=sum(ci_cost) if ci_cost else None,
        total_sidecar_cost_usd=sum(sidecar_cost) if sidecar_cost else None,
    )


def _fmt_secs(value: float | None) -> str:
    if value is None:
        return "—"
    if abs(value - round(value)) < 0.05:
        return str(int(round(value)))
    return f"{value:.1f}"


def render_report(snaps: list[RunSnapshot], labels: list[str]) -> str:
    lines = [
        "# Sidecar race — cross-run comparison",
        "",
        "Per-replicate **median TTS** (seconds) for gate `lint` + `test` / remote `test-changed`.",
        "Aggregate row uses median of per-run medians.",
        "",
        "| label | sidecar median TTS | CI median TTS | Δ (CI − sidecar) | sidecar LLM | CI LLM |",
        "|------:|-------------------:|--------------:|-----------------:|------------:|-------:|",
    ]

    sidecar_snaps = {s.label: s for s in snaps if s.arm == "sidecar"}
    ci_snaps = {s.label: s for s in snaps if s.arm == "ci"}

    for label in labels:
        sc = sidecar_snaps.get(label)
        ci = ci_snaps.get(label)
        sc_tts = sc.median_tts_s if sc else None
        ci_tts = ci.median_tts_s if ci else None
        delta = "—"
        if sc_tts is not None and ci_tts is not None:
            delta = f"{ci_tts - sc_tts:+.0f}"
        sc_llm = format_usd(sc.llm_cost_usd) if sc and sc.llm_cost_usd is not None else "—"
        ci_llm = format_usd(ci.llm_cost_usd) if ci and ci.llm_cost_usd is not None else "—"
        lines.append(
            f"| {label} | {_fmt_secs(sc_tts)} | {_fmt_secs(ci_tts)} | {delta} | {sc_llm} | {ci_llm} |"
        )

    sc_agg = aggregate_arm(snaps, "sidecar")
    ci_agg = aggregate_arm(snaps, "ci")
    delta_agg = "—"
    if sc_agg.median_of_medians_s is not None and ci_agg.median_of_medians_s is not None:
        delta_agg = f"{ci_agg.median_of_medians_s - sc_agg.median_of_medians_s:+.0f}"

    lines.extend(
        [
            f"| **median** | **{_fmt_secs(sc_agg.median_of_medians_s)}** | "
            f"**{_fmt_secs(ci_agg.median_of_medians_s)}** | **{delta_agg}** | "
            f"**{format_usd(sc_agg.total_llm_cost_usd) if sc_agg.total_llm_cost_usd else '—'}** | "
            f"**{format_usd(ci_agg.total_llm_cost_usd) if ci_agg.total_llm_cost_usd else '—'}** |",
            "",
            "## Per-run detail",
            "",
            "| label | arm | run_id | median TTS | p95 TTS | iters | lint fail | test fail | "
            "CI cost | sidecar est. | LLM tokens | epilogue TTS |",
            "|------:|-----|--------|----------:|--------:|------:|----------:|----------:|"
            "--------:|-------------:|-----------:|-------------:|",
        ]
    )

    for snap in sorted(snaps, key=lambda s: (s.label, s.arm)):
        if snap.label not in labels:
            continue
        lines.append(
            f"| {snap.label} | {snap.arm} | {snap.run_id} | {_fmt_secs(snap.median_tts_s)} | "
            f"{_fmt_secs(snap.p95_tts_s)} | {snap.iterations} | {snap.lint_fail_iters} | "
            f"{snap.test_fail_iters} | {format_usd(snap.ci_cost_usd)} | "
            f"{format_usd(snap.sidecar_cost_usd)} | "
            f"{snap.llm_tokens if snap.llm_tokens is not None else '—'} | "
            f"{_fmt_secs(snap.epilogue_tts_s) if snap.arm == 'sidecar' else '—'} |"
        )

    lines.extend(["", "## Cost totals (sum across replicates)", ""])
    lines.append(f"- Sidecar arm: n={sc_agg.n} — LLM {format_usd(sc_agg.total_llm_cost_usd)}, "
                 f"sidecar est. {format_usd(sc_agg.total_sidecar_cost_usd)}, "
                 f"epilogue CI {format_usd(sc_agg.total_ci_cost_usd)}")
    lines.append(f"- CI arm: n={ci_agg.n} — LLM {format_usd(ci_agg.total_llm_cost_usd)}, "
                 f"CI gates {format_usd(ci_agg.total_ci_cost_usd)}")

    if sc_agg.median_of_medians_s and ci_agg.median_of_medians_s:
        ratio = ci_agg.median_of_medians_s / sc_agg.median_of_medians_s
        saved = ci_agg.median_of_medians_s - sc_agg.median_of_medians_s
        lines.extend(
            [
                "",
                "## Extrapolation hint",
                "",
                f"Measured medians: sidecar **{_fmt_secs(sc_agg.median_of_medians_s)}s**, "
                f"CI **{_fmt_secs(ci_agg.median_of_medians_s)}s** "
                f"({ratio:.1f}× slower on CI, {saved:.0f}s saved per iteration).",
                "",
                "```bash",
                "./scripts/extrapolate.sh "
                f"--sidecar-avg-sec {int(round(sc_agg.median_of_medians_s))} "
                f"--ci-avg-sec {int(round(ci_agg.median_of_medians_s))}",
                "```",
            ]
        )
    elif sc_agg.median_of_medians_s:
        lines.extend(
            [
                "",
                "## Extrapolation hint",
                "",
                f"Sidecar median of medians: **{_fmt_secs(sc_agg.median_of_medians_s)}s**. "
                "Re-run with CI arm data to fill the comparison.",
            ]
        )

    return "\n".join(lines) + "\n"


def parse_labels(text: str) -> list[str]:
    return [p.strip() for p in text.split(",") if p.strip()]


def default_labels(snaps: list[RunSnapshot]) -> list[str]:
    labels = sorted({s.label for s in snaps}, key=lambda x: (len(x), x))
    return labels


def main() -> None:
    import argparse

    parser = argparse.ArgumentParser(description="Compare sidecar-race runs by label and arm.")
    parser.add_argument(
        "--labels",
        default="",
        help="Comma-separated run labels (e.g. 001,002,003). Default: all discovered.",
    )
    parser.add_argument(
        "--results-dir",
        default="",
        help="Path to experiments/sidecar-race/results (default: experiment results/)",
    )
    parser.add_argument(
        "--from-git",
        action="store_true",
        help="Load results from origin run branches (for gitignored local results/)",
    )
    parser.add_argument(
        "--repo-root",
        default="",
        help="Repo root for --from-git (default: auto)",
    )
    parser.add_argument("-o", "--output", default="", help="Write markdown report to this path")
    args = parser.parse_args()

    script_dir = Path(__file__).resolve().parent
    experiment_root = script_dir.parent.parent
    results_root = Path(args.results_dir) if args.results_dir else experiment_root / "results"
    repo_root = Path(args.repo_root) if args.repo_root else experiment_root.parent.parent

    local = discover_local(results_root)
    published = discover_published(results_root)
    git_snaps: list[RunSnapshot] = []
    if args.from_git:
        labels_guess = parse_labels(args.labels) if args.labels else [
            f"{i:03d}" for i in range(1, 20)
        ]
        git_snaps = discover_git(repo_root, labels_guess, ["sidecar", "ci"])

    # Published copies on the harness branch win over git run branches.
    snaps = merge_snapshots(git_snaps, local, published)
    if not snaps:
        print("No runs found. Use --from-git or commit results under results/<run-id>/", file=sys.stderr)
        sys.exit(1)

    labels = parse_labels(args.labels) if args.labels else default_labels(snaps)
    report = render_report(snaps, labels)
    print(report, end="")
    if args.output:
        Path(args.output).write_text(report)


if __name__ == "__main__":
    main()
