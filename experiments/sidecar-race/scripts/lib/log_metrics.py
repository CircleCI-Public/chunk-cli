"""Append structured timing/cost events to results/<run-id>/metrics.jsonl."""
from __future__ import annotations

import json
import sys
from pathlib import Path


def append_event(run_dir: Path, event: dict) -> None:
    path = run_dir / "metrics.jsonl"
    with path.open("a") as f:
        f.write(json.dumps(event, separators=(",", ":")) + "\n")


def main() -> None:
    run_dir = Path(sys.argv[1])
    event = json.loads(sys.argv[2])
    append_event(run_dir, event)


if __name__ == "__main__":
    main()
