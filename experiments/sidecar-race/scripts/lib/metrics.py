"""CircleCI credits and cost helpers for sidecar-race experiment."""
from __future__ import annotations

import json
import os
import subprocess
import urllib.parse
from datetime import datetime


def credit_usd_rate() -> float:
    return float(os.environ.get("CIRCLECI_CREDIT_USD", "0.0006"))


def sidecar_credits_per_min() -> float:
    return float(os.environ.get("SIDECAR_CREDITS_PER_MIN", "0"))


def credits_to_usd(credits: float) -> float:
    return round(credits * credit_usd_rate(), 6)


def _parse_ci_timestamp(value: str) -> datetime:
    return datetime.fromisoformat(value.replace("Z", "+00:00"))


def job_duration_seconds(job: dict) -> float:
    """Wall-clock job duration from CircleCI v2 job object (started_at / stopped_at)."""
    if job.get("duration") is not None:
        return max(float(job.get("duration") or 0), 0.0)
    started = job.get("started_at")
    stopped = job.get("stopped_at")
    if started and stopped:
        delta = _parse_ci_timestamp(stopped) - _parse_ci_timestamp(started)
        return max(delta.total_seconds(), 0.0)
    return 0.0


def format_duration_s(seconds: float | int | None) -> str:
    """Human-readable duration for PR tables."""
    if seconds is None or seconds == "":
        return "—"
    value = float(seconds)
    if value <= 0:
        return "0"
    if abs(value - round(value)) < 0.05:
        return str(int(round(value)))
    return f"{value:.1f}"


def format_credits(credits: float | int | None) -> str:
    if credits is None or credits == "":
        return "—"
    value = float(credits)
    if value <= 0:
        return "0"
    if abs(value - round(value)) < 0.05:
        return str(int(round(value)))
    return f"{value:.1f}"


def api_get(path: str) -> dict:
    token = os.environ["CIRCLE_TOKEN"]
    url = f"https://circleci.com/api/v2{path}"
    raw = subprocess.check_output(
        ["curl", "-fsSL", "-H", f"Circle-Token: {token}", url],
    )
    return json.loads(raw)


def workflow_credits(project_slug: str, workflow_id: str) -> int:
    """Insights credits for a workflow run (may lag up to ~24h on some plans)."""
    slug = project_slug.removeprefix("github/").removeprefix("gh/")
    slug_path = urllib.parse.quote(f"gh/{slug}", safe="/")
    for branch in ("", "?all-branches=true"):
        try:
            data = api_get(f"/insights/{slug_path}/workflows/ci{branch}")
        except subprocess.CalledProcessError:
            continue
        for item in data.get("items") or []:
            if item.get("id") == workflow_id:
                return int(item.get("credits_used") or 0)
    return 0


def gate_job_credits(
    project_slug: str,
    workflow_id: str,
    lint_duration_s: float,
    test_duration_s: float,
) -> tuple[int, int, int]:
    """Return (workflow_credits, lint_credits_est, test_credits_est)."""
    total = workflow_credits(project_slug, workflow_id)
    gate_dur = lint_duration_s + test_duration_s
    if total <= 0 or gate_dur <= 0:
        return total, 0, 0
    lint_c = int(round(total * lint_duration_s / gate_dur))
    test_c = total - lint_c
    return total, lint_c, test_c


def workflow_job_durations(workflow_id: str) -> dict[str, float]:
    jobs: dict[str, float] = {}
    for j in api_get(f"/workflow/{workflow_id}/job").get("items") or []:
        name = j.get("name")
        if name:
            jobs[name] = round(job_duration_seconds(j), 1)
    return jobs


def workflow_total_credits_from_jobs(
    project_slug: str, workflow_id: str
) -> tuple[int, dict[str, float]]:
    """Allocate workflow insights credits across jobs by duration."""
    total = workflow_credits(project_slug, workflow_id)
    durations = workflow_job_durations(workflow_id)
    dur_sum = sum(durations.values()) or 1.0
    per_job = {n: round(total * d / dur_sum, 1) for n, d in durations.items()}
    return total, per_job


def enrich_workflow_jobs(
    project_slug: str,
    workflow_id: str,
    jobs: dict[str, dict],
) -> tuple[int, dict[str, dict]]:
    """Fill duration_s, credits_est, cost_usd_est on job dicts from the API."""
    wf_credits, job_credits = workflow_total_credits_from_jobs(project_slug, workflow_id)
    durations = workflow_job_durations(workflow_id)
    for name, info in jobs.items():
        info["duration_s"] = durations.get(name, job_duration_seconds(info))
        cred = job_credits.get(name, 0)
        info["credits_est"] = cred
        info["cost_usd_est"] = credits_to_usd(cred)
    return wf_credits, jobs
