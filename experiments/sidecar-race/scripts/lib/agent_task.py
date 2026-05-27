"""Run one experiment iteration via Claude Agent SDK (local edits)."""
from __future__ import annotations

import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path

import anyio
from claude_agent_sdk import (
    AssistantMessage,
    ClaudeAgentOptions,
    ResultMessage,
    TextBlock,
    query,
)

EXPERIMENT_ROOT = Path(__file__).resolve().parents[2]
MANIFEST_PATH = EXPERIMENT_ROOT / "task-bank" / "manifest.json"

SYSTEM = """You are completing one step of a controlled Go experiment.

Rules:
- Only edit files under internal/racefixture/ unless the task explicitly requires internal/config/.
- Do not modify experiments/, .circleci/, docs/, or harness scripts.
- Prefer minimal, focused diffs.
- Run relevant go test / golangci-lint commands to verify before finishing."""


def _usage_from_result(result: ResultMessage) -> dict:
    usage = result.usage or {}
    inp = int(usage.get("input_tokens") or usage.get("inputTokens") or 0)
    out = int(usage.get("output_tokens") or usage.get("outputTokens") or 0)
    cost = result.total_cost_usd
    if cost is None and result.model_usage:
        cost = 0.0
        for model_data in result.model_usage.values():
            if isinstance(model_data, dict):
                cost += float(model_data.get("cost_usd") or model_data.get("costUSD") or 0)
    return {
        "input_tokens": inp,
        "output_tokens": out,
        "total_tokens": inp + out,
        "cost_usd": float(cost) if cost is not None else None,
        "duration_ms": result.duration_ms,
        "num_turns": result.num_turns,
        "is_error": result.is_error,
        "stop_reason": result.stop_reason,
    }


def load_task(iter_num: int) -> dict:
    manifest = json.loads(MANIFEST_PATH.read_text())
    for task in manifest.get("tasks", []):
        if task.get("id") == iter_num:
            return task, manifest
    raise SystemExit(f"no task with id {iter_num} in manifest.json")


def apply_seed(repo_root: Path, seed_patch: str) -> None:
    patch_path = EXPERIMENT_ROOT / "task-bank" / seed_patch
    if not patch_path.is_file():
        raise SystemExit(f"seed patch not found: {patch_path}")
    import subprocess

    subprocess.run(
        ["git", "-C", str(repo_root), "apply", "--whitespace=fix", str(patch_path)],
        check=True,
    )


def append_agent_usage(run_dir: Path, record: dict) -> None:
    path = run_dir / "agent_usage.jsonl"
    with path.open("a") as f:
        f.write(json.dumps(record, separators=(",", ":")) + "\n")


def aggregate_llm_usage(run_dir: Path) -> None:
    """Write llm_usage.json totals from agent_usage.jsonl."""
    usage_path = run_dir / "agent_usage.jsonl"
    if not usage_path.exists():
        return
    inp = out = 0
    cost = 0.0
    cost_known = False
    per_iter: list[dict] = []
    for line in usage_path.read_text().splitlines():
        if not line.strip():
            continue
        row = json.loads(line)
        per_iter.append(row)
        inp += int(row.get("input_tokens") or 0)
        out += int(row.get("output_tokens") or 0)
        c = row.get("cost_usd")
        if c is not None:
            cost += float(c)
            cost_known = True
    payload = {
        "input_tokens": inp,
        "output_tokens": out,
        "cost_usd": round(cost, 6) if cost_known else None,
        "source": "claude-agent-sdk",
        "note": f"{len(per_iter)} agent iteration(s) via experiments/sidecar-race/scripts/lib/agent_task.py",
        "per_iter": per_iter,
    }
    (run_dir / "llm_usage.json").write_text(json.dumps(payload, indent=2) + "\n")


async def run_agent(repo_root: Path, prompt: str, model: str) -> tuple[ResultMessage | None, dict]:
    result_msg: ResultMessage | None = None
    options = ClaudeAgentOptions(
        cwd=str(repo_root),
        model=model,
        system_prompt=SYSTEM,
        allowed_tools=["Read", "Edit", "Bash", "Glob", "Grep"],
        permission_mode="acceptEdits",
    )
    async for message in query(prompt=prompt, options=options):
        if isinstance(message, AssistantMessage):
            for block in message.content:
                if isinstance(block, TextBlock) and block.text.strip():
                    print(f"[agent] {block.text.strip()[:500]}")
        elif isinstance(message, ResultMessage):
            result_msg = message
            if message.result:
                print(f"[agent done] {message.result[:300]}")
    if result_msg is None:
        raise RuntimeError("agent finished without ResultMessage")
    return result_msg, _usage_from_result(result_msg)


def main() -> None:
    if len(sys.argv) < 2:
        raise SystemExit("usage: agent_task.py <iter>")
    iter_num = int(sys.argv[1])
    repo_root = Path(os.environ.get("REPO_ROOT", "")).resolve()
    run_dir = Path(os.environ.get("RUN_DIR", "")).resolve()
    if not repo_root.is_dir():
        raise SystemExit("REPO_ROOT must be set to the git repository root")
    if not run_dir.is_dir():
        raise SystemExit("RUN_DIR must be set to results/<run-id>/")

    task, manifest = load_task(iter_num)
    prompt = task.get("agent_prompt") or task.get("description")
    if not prompt:
        raise SystemExit(f"task {iter_num} has no agent_prompt")

    seed = task.get("seed_patch")
    if seed:
        print(f"Applying seed patch {seed}...")
        apply_seed(repo_root, seed)

    model = os.environ.get("SIDECAR_RACE_AGENT_MODEL") or manifest.get("agent_model") or "claude-sonnet-4-20250514"
    print(f"Running Claude Agent SDK (model={model}) for task {iter_num}...")
    started = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    result_msg, usage = anyio.run(run_agent, repo_root, prompt, model)
    ended = datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

    record = {
        "iter": iter_num,
        "slug": task.get("slug"),
        "started_at": started,
        "ended_at": ended,
        "model": model,
        **usage,
    }
    append_agent_usage(run_dir, record)
    aggregate_llm_usage(run_dir)

    sys.path.insert(0, str(Path(__file__).parent))
    from log_metrics import append_event  # noqa: E402

    append_event(
        run_dir,
        {
            "kind": "agent_iter",
            "iter": iter_num,
            "agent_duration_ms": usage.get("duration_ms"),
            "input_tokens": usage.get("input_tokens"),
            "output_tokens": usage.get("output_tokens"),
            "cost_usd": usage.get("cost_usd"),
        },
    )

    if usage.get("is_error"):
        raise SystemExit(f"agent reported error for task {iter_num}")
    print(
        f"Agent task {iter_num}: tokens in={usage['input_tokens']} out={usage['output_tokens']} "
        f"cost_usd={usage.get('cost_usd')}"
    )


if __name__ == "__main__":
    main()
