"""Aggregates a job's trials into per-agent results. Reads only the trial
directories, so a report can be re-run, or a weighting changed, without
re-running anything."""

import json
import statistics
from collections import defaultdict
from pathlib import Path

# Shown in this order when present; any other reward dimension follows.
PRIMARY = ["solved", "hidden", "suite", "judge_implementation", "judge_review_precision",
           "judge_review_coverage", "judge_remaining_defects", "judge_scope_violations"]
METRICS = ["round_count", "cost_usd", "duration_s", "reviewer_seconds"]


def load_trials(job_dir: Path) -> list[dict]:
    return [json.loads(p.read_text(encoding="utf-8")) for p in sorted(job_dir.glob("trials/*/*/*/trial.json"))]


def stats(values: list[float]) -> dict:
    if not values:
        return {}
    return {"mean": round(statistics.fmean(values), 3),
            "stdev": round(statistics.stdev(values), 3) if len(values) > 1 else 0.0, "n": len(values)}


def summarize(trials: list[dict]) -> dict:
    completed = [t for t in trials if t["status"] == "completed"]
    dims = sorted({k for t in completed for k in t["reward"]},
                  key=lambda k: (PRIMARY.index(k) if k in PRIMARY else len(PRIMARY), k))
    results = defaultdict(list)
    for t in completed:
        for k in METRICS:
            if isinstance(t["metrics"].get(k), (int, float)):
                results[k].append(t["metrics"][k])
    outcomes = defaultdict(int)
    for t in completed:
        if t["metrics"].get("factory_result"):
            outcomes[t["metrics"]["factory_result"]] += 1
    return {
        "trials": len(trials), "completed": len(completed),
        "errored": sum(t["status"] == "errored" for t in trials),
        "not_applicable": sum(t["status"] == "not_applicable" for t in trials),
        "reward": {k: stats([t["reward"][k] for t in completed if k in t["reward"]]) for k in dims},
        "metrics": {k: stats(v) for k, v in results.items()},
        "factory_results": dict(outcomes),
        # pass@k over a task's attempts: did any of them solve it.
        "pass_at_k": int(any(t["reward"].get("solved") for t in completed)) if completed else None,
    }


def build(job_dir: Path) -> dict:
    trials = load_trials(job_dir)
    by_cell = defaultdict(list)
    by_agent = defaultdict(list)
    for t in trials:
        by_cell[(t["task"], t["agent"])].append(t)
        by_agent[t["agent"]].append(t)
    cells = {f"{task}/{agent}": summarize(ts) for (task, agent), ts in sorted(by_cell.items())}
    agents = {}
    for agent, ts in sorted(by_agent.items()):
        overall = summarize(ts)
        # Task-balanced means: each task counts once however many attempts it had.
        tasks = sorted({t["task"] for t in ts})
        task_solved = [cells[f"{task}/{agent}"]["reward"].get("solved", {}).get("mean") for task in tasks]
        task_solved = [v for v in task_solved if v is not None]
        overall["task_mean_solved"] = round(statistics.fmean(task_solved), 3) if task_solved else None
        overall["tasks"] = tasks
        agents[agent] = overall
    return {"job": job_dir.name, "agents": agents, "cells": cells}


def fmt(cell: dict) -> str:
    if not cell:
        return "–"
    return f"{cell['mean']:.2f} ± {cell['stdev']:.2f}" if cell["n"] > 1 else f"{cell['mean']:.2f}"


def markdown(report: dict) -> str:
    lines = [f"# Factory eval report: {report['job']}", ""]
    columns = ["solved", "hidden", "judge_implementation", "judge_review_precision", "judge_review_coverage"]
    metrics = ["round_count", "cost_usd", "duration_s"]
    header = ["Agent", "Trials (ok/err/n.a.)", "Task-mean solved", *columns, *metrics]
    lines += ["## By agent", "", "| " + " | ".join(header) + " |", "|" + "---|" * len(header)]
    for agent, s in report["agents"].items():
        row = [agent, f"{s['completed']}/{s['errored']}/{s['not_applicable']}",
               "–" if s["task_mean_solved"] is None else f"{s['task_mean_solved']:.2f}",
               *[fmt(s["reward"].get(c, {})) for c in columns], *[fmt(s["metrics"].get(m, {})) for m in metrics]]
        lines.append("| " + " | ".join(row) + " |")
    header = ["Task / agent", "Trials", "pass@k", *columns, "factory results"]
    lines += ["", "## By task", "", "| " + " | ".join(header) + " |", "|" + "---|" * len(header)]
    for name, s in report["cells"].items():
        results = ", ".join(f"{k} {v}" for k, v in sorted(s["factory_results"].items())) or "–"
        row = [name, f"{s['completed']}/{s['trials']}", "–" if s["pass_at_k"] is None else str(s["pass_at_k"]),
               *[fmt(s["reward"].get(c, {})) for c in columns], results]
        lines.append("| " + " | ".join(row) + " |")
    lines += convergence_markdown(report.get("convergence", {}))
    lines += ["", "Means are over completed trials; errored trials (infrastructure, not the agent) are excluded. "
              "`solved` means every hidden and suite check passed."]
    return "\n".join(lines) + "\n"


def factory_rounds(trial_dir: Path) -> list[dict]:
    """Each round of a factory trial: what failed, errored or passed only from
    a cache, and how many high/medium findings each review gave."""
    try:
        result = json.loads((trial_dir / "result.json").read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return []
    reviews = {d.get("number"): d.get("results") or [] for d in result.get("details") or []}
    rounds = []
    for current in result.get("rounds") or []:
        checks = [("validate", c.get("name"), c.get("status"), c.get("output") or "")
                  for c in current.get("checks") or []]
        worth = {}
        for r in reviews.get(current.get("number"), []):
            checks.append(("review", r.get("prompt"), r.get("status"), ""))
            worth[r.get("prompt")] = sum(f.get("severity") in ("high", "medium") for f in r.get("findings") or [])
        rounds.append({
            "failed": [f"{kind}:{name}" for kind, name, status, _ in checks if status == "failed"],
            "errored": [f"{kind}:{name}" for kind, name, status, _ in checks if status == "errored"],
            "cached": [f"{kind}:{name}" for kind, name, status, output in checks
                       if status == "passed" and "(cached)" in output],
            "worth_changing": sum(worth.values()),
        })
    return rounds


def tally_rounds(rounds: list[dict], tally: dict) -> None:
    """Adds one factory trial's rounds to an agent's tally."""
    for index, current in enumerate(rounds):
        tally["rounds"] += 1
        tally["findings_by_round"][index + 1].append(current["worth_changing"])
        for name in current["cached"]:
            tally["cached_passes"][name] += 1
        if not current["failed"] and not current["errored"]:
            continue
        tally["failed_rounds"] += 1
        # An errored-only round feeds nothing back, so the next re-checks the same code.
        tally["errored_only_rounds"] += not current["failed"]
        for name in current["failed"]:
            tally["causes"][name] += 1
        for name in current["errored"]:
            tally["causes"][f"errored {name}"] += 1
        later = rounds[index + 1]["failed"] if index + 1 < len(rounds) else None
        for name in current["failed"] if later is not None else []:
            if name in later:
                tally["failed_again_next_round"][name] += 1
            else:
                tally["fixed_next_round"] += 1


def convergence(job_dir: Path, trials: list[dict]) -> dict:
    """Why factory rounds did not pass, per agent: the evidence for why runs do
    or do not converge."""
    out = {}
    for agent in sorted({t["agent"] for t in trials if t.get("kind") == "factory"}):
        tally = {"errored_trials": 0, "rounds": 0, "failed_rounds": 0, "errored_only_rounds": 0,
                 "fixed_next_round": 0, "judge_false_positives": 0, "causes": defaultdict(int),
                 "failed_again_next_round": defaultdict(int), "cached_passes": defaultdict(int),
                 "findings_by_round": defaultdict(list)}
        # A run that ended without a result is left out of the scores, but its
        # rounds say just as much about convergence.
        for t in (t for t in trials if t["agent"] == agent and t["status"] in ("completed", "errored")):
            tally["errored_trials"] += t["status"] == "errored"
            directory = job_dir / "trials" / t["task"] / agent / f"{t['attempt']:02d}"
            tally_rounds(factory_rounds(directory), tally)
            try:
                verdict = json.loads((directory / "judge.json").read_text(encoding="utf-8"))
                tally["judge_false_positives"] += len(verdict.get("false_positives") or [])
            except (OSError, json.JSONDecodeError):
                pass
        by_round = tally.pop("findings_by_round")
        for key in ("causes", "failed_again_next_round", "cached_passes"):
            tally[key] = dict(sorted(tally[key].items(), key=lambda kv: -kv[1]))
        tally["mean_worth_changing_by_round"] = {r: round(statistics.fmean(v), 2) for r, v in sorted(by_round.items())}
        out[agent] = tally
    return out


def convergence_markdown(conv: dict) -> list[str]:
    if not conv:
        return []
    lines = ["", "## Convergence (factory agents)", "",
             "Why rounds did not pass. A check that fails again in the next round is one the loop is not fixing; "
             "an errored-only round feeds nothing back and re-checks the same code, spending an attempt; a cached "
             "pass checked nothing.", "",
             "| Agent | Errored runs | Rounds | Failed rounds | Errored-only | Fixed next round | Top causes | "
             "Failed again next round | High/medium findings by round | Judge false positives | Cached passes |",
             "|---|---|---|---|---|---|---|---|---|---|---|"]
    for agent, c in conv.items():
        def top(d):
            return ", ".join(f"{k} ×{v}" for k, v in list(d.items())[:4]) or "–"
        by_round = " → ".join(f"{v}" for v in c["mean_worth_changing_by_round"].values()) or "–"
        lines.append(f"| {agent} | {c['errored_trials']} | {c['rounds']} | {c['failed_rounds']} | "
                     f"{c['errored_only_rounds']} | {c['fixed_next_round']} | {top(c['causes'])} | "
                     f"{top(c['failed_again_next_round'])} | "
                     f"{by_round} | {c['judge_false_positives']} | {top(c['cached_passes'])} |")
    return lines


def write(job_dir: Path) -> dict:
    report = build(job_dir)
    report["convergence"] = convergence(job_dir, load_trials(job_dir))
    (job_dir / "report.json").write_text(json.dumps(report, indent=2) + "\n", encoding="utf-8")
    (job_dir / "report.md").write_text(markdown(report), encoding="utf-8")
    return report
