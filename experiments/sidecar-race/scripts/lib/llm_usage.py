"""Optional LLM usage totals for a sidecar-race run directory."""
from __future__ import annotations

import json
from pathlib import Path


def format_usd(amount: float | int | None) -> str:
    """Format a dollar amount for PR tables (e.g. $0.0124)."""
    if amount is None or amount == "":
        return "n/a"
    try:
        value = float(amount)
    except (TypeError, ValueError):
        return "n/a"
    if value == 0:
        return "$0"
    text = f"${value:.4f}"
    return text.rstrip("0").rstrip(".")


def format_tokens(count: int | None) -> str:
    if count is None:
        return "n/a"
    return str(int(count))


def load_llm_totals(run_dir: Path) -> dict:
    """Load LLM usage from results/<run-id>/llm_usage.json if present."""
    path = run_dir / "llm_usage.json"
    if not path.exists():
        return {
            "measured": False,
            "input_tokens": None,
            "output_tokens": None,
            "total_tokens": None,
            "cost_usd": None,
            "source": None,
            "note": (
                "Not measured: no agent_usage.jsonl / llm_usage.json for this run. "
                "Normal runs invoke Claude Agent SDK via run-agent-task.sh."
            ),
        }
    data = json.loads(path.read_text())
    inp = int(data.get("input_tokens") or 0)
    out = int(data.get("output_tokens") or 0)
    cost = data.get("cost_usd")
    if cost is not None:
        cost = float(cost)
    return {
        "measured": True,
        "input_tokens": inp,
        "output_tokens": out,
        "total_tokens": inp + out,
        "cost_usd": cost,
        "source": data.get("source"),
        "note": data.get("note"),
    }
