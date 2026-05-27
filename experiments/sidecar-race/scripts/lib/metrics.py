"""CircleCI credits and cost helpers for sidecar-race experiment."""
from __future__ import annotations

import json
import os
import subprocess
import urllib.parse


def credit_usd_rate() -> float:
    return float(os.environ.get("CIRCLECI_CREDIT_USD", "0.0006"))


def sidecar_credits_per_min() -> float:
    return float(os.environ.get("SIDECAR_CREDITS_PER_MIN", "0"))


def credits_to_usd(credits: float) -> float:
    return round(credits * credit_usd_rate(), 6)


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
    project_slug: str, workflow_id: str, lint_duration_s: int, test_duration_s: int
) -> tuple[int, int, int]:
    """Return (workflow_credits, lint_credits_est, test_credits_est)."""
    total = workflow_credits(project_slug, workflow_id)
    gate_dur = lint_duration_s + test_duration_s
    if total <= 0 or gate_dur <= 0:
        return total, 0, 0
    lint_c = int(total * lint_duration_s / gate_dur)
    test_c = total - lint_c
    return total, lint_c, test_c


def workflow_job_durations(workflow_id: str) -> dict[str, int]:
    jobs: dict[str, int] = {}
    for j in api_get(f"/workflow/{workflow_id}/job").get("items") or []:
        name = j.get("name")
        if name:
            jobs[name] = int(j.get("duration") or 0)
    return jobs


def workflow_total_credits_from_jobs(
    project_slug: str, workflow_id: str
) -> tuple[int, dict[str, int]]:
    """Allocate workflow insights credits across jobs by duration."""
    total = workflow_credits(project_slug, workflow_id)
    durations = workflow_job_durations(workflow_id)
    dur_sum = sum(durations.values()) or 1
    per_job = {n: int(total * d / dur_sum) for n, d in durations.items()}
    return total, per_job
