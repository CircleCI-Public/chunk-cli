#!/usr/bin/env python3
"""
environment: harness for `chunk sandbox env` + the sandbox build acceptance test.
Runs against target repos and uses the Claude Code SDK to improve
internal/envbuilder when tests fail.

Usage:
    task env-harness [-- --repo flask --repo serde --timeout SECONDS]
    uv run --project harness harness/environment.py [--repo NAME_OR_URL ...] [--timeout SECONDS] [--cache-dir DIR]

Requirements (managed by harness/pyproject.toml):
    claude-agent-sdk, anyio
"""

import argparse
import datetime
import difflib
import json
import os
import shutil
import subprocess
import sys
import tempfile
import threading
from concurrent.futures import ThreadPoolExecutor, as_completed
from dataclasses import dataclass, field
from pathlib import Path

import anyio
from claude_agent_sdk import query, ClaudeAgentOptions, ResultMessage, AssistantMessage, TextBlock


class _Tee:
    """Write to two streams simultaneously (terminal + log file)."""

    def __init__(self, primary, secondary):
        self._primary = primary
        self._secondary = secondary

    def write(self, data):
        self._primary.write(data)
        self._secondary.write(data)
        return len(data)

    def flush(self):
        self._primary.flush()
        self._secondary.flush()

    def __getattr__(self, name):
        return getattr(self._primary, name)


REPO_ROOT = Path(__file__).parent.parent
BINARY = REPO_ROOT / "dist" / "chunk"

# Primary source Claude should edit when env detection or Dockerfile generation is wrong.
ENVBUILDER_SOURCE = REPO_ROOT / "internal" / "envbuilder" / "envbuilder.go"

MAX_ITERATIONS = 5
MAX_OUTPUT_CHARS = 40_000

# Only one Claude agent may edit envbuilder.go at a time.
_envbuilder_lock = threading.Lock()

# Serialise reads and writes to acceptance/repos.json across parallel repo workers.
_repos_json_lock = threading.Lock()

REPOS_JSON_PATH = REPO_ROOT / "acceptance" / "repos.json"
KNOWN_REPOS: list[dict] = json.loads(REPOS_JSON_PATH.read_text())


# ---------------------------------------------------------------------------
# Data structures
# ---------------------------------------------------------------------------

@dataclass
class TargetRepo:
    name: str
    url: str
    ref: str = ""           # pinned SHA/tag; empty means HEAD
    env: dict | None = None  # last known passing env spec

    @property
    def sandbox_repos_value(self) -> str:
        """Value to pass as CHUNK_SANDBOX_REPOS to the acceptance test."""
        known = {r["name"] for r in KNOWN_REPOS}
        return self.name if self.name in known else self.url


@dataclass
class EnvBuilderChange:
    triggered_by_repo: str
    iteration: int
    diff: str
    rebuild_ok: bool
    rebuild_error: str = ""


@dataclass
class IterationRecord:
    iteration: int
    dockerfile: str
    tests_ok: bool
    test_output: str
    envbuilder_updated: bool


@dataclass
class RepoRecord:
    repo: TargetRepo
    passed: bool
    iterations: list[IterationRecord] = field(default_factory=list)


_envbuilder_changes: list[EnvBuilderChange] = []


# ---------------------------------------------------------------------------
# Repo resolution
# ---------------------------------------------------------------------------

def resolve_repo(repo_arg: str) -> TargetRepo:
    if repo_arg.startswith(("https://", "http://", "git@")):
        name = repo_arg.rstrip("/").rsplit("/", 1)[-1].removesuffix(".git") or repo_arg
        return TargetRepo(name=name, url=repo_arg)
    known = {r["name"]: r for r in KNOWN_REPOS}
    if repo_arg not in known:
        print(
            f"Unknown repo nickname '{repo_arg}'. "
            f"Known: {', '.join(known)}",
            file=sys.stderr,
        )
        sys.exit(1)
    entry = known[repo_arg]
    return TargetRepo(name=entry["name"], url=entry["url"], ref=entry.get("ref", ""), env=entry.get("env"))


def resolve_repos(repo_args: list[str]) -> list[TargetRepo]:
    if repo_args:
        return [resolve_repo(r) for r in repo_args]
    return [TargetRepo(name=r["name"], url=r["url"], ref=r.get("ref", ""), env=r.get("env")) for r in KNOWN_REPOS]


# ---------------------------------------------------------------------------
# Core helpers
# ---------------------------------------------------------------------------

def truncate_output(text: str) -> str:
    if len(text) <= MAX_OUTPUT_CHARS:
        return text
    half = MAX_OUTPUT_CHARS // 2
    omitted = len(text) - MAX_OUTPUT_CHARS
    return text[:half] + f"\n\n... ({omitted} chars omitted) ...\n\n" + text[-half:]


def build_binary() -> None:
    result = subprocess.run(
        ["go", "build", "-o", str(BINARY), "."],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        check=False,
    )
    if result.returncode != 0:
        raise RuntimeError(f"go build failed:\n{result.stderr}")


def _run_test(repo: TargetRepo, cache_dir: Path, timeout: int, extra_env: dict | None = None) -> tuple[bool, str]:
    env = {
        **os.environ,
        "CHUNK_ENV_BUILDER_ACCEPTANCE": "1",
        "CHUNK_SANDBOX_REPOS": repo.sandbox_repos_value,
        "CHUNK_SANDBOX_CACHE_DIR": str(cache_dir),
        "NO_COLOR": "1",
        **(extra_env or {}),
    }
    result = subprocess.run(
        ["go", "test", "-v", "-count=1", f"-timeout={timeout}s",
         "-run", "TestSandboxesBuildEndToEnd", "./acceptance/"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        env=env,
        check=False,
    )
    return result.returncode == 0, truncate_output(result.stdout + result.stderr)


def run_acceptance_test(repo: TargetRepo, cache_dir: Path, timeout: int) -> tuple[bool, str]:
    """Clone (if needed), run env, build image, run tests. Returns (passed, output)."""
    return _run_test(repo, cache_dir, timeout)


def run_env_only_test(repo: TargetRepo, cache_dir: Path, timeout: int) -> tuple[dict | None, str]:
    """Clone (if needed) and run env detection only. Returns (env_dict, output)."""
    ok, output = _run_test(repo, cache_dir, timeout, extra_env={"CHUNK_SANDBOX_ENV_ONLY": "1"})
    if not ok:
        return None, output
    env_json_path = cache_dir / f"chunk-sandbox-{repo.name}" / "env.json"
    try:
        return json.loads(env_json_path.read_text()), output
    except (OSError, json.JSONDecodeError) as e:
        return None, output + f"\n(failed to read env.json: {e})"


def read_dockerfile(repo: TargetRepo, cache_dir: Path) -> str:
    # Acceptance test clones to cache_dir/chunk-sandbox-{name}
    path = cache_dir / f"chunk-sandbox-{repo.name}" / "Dockerfile.test"
    if path.exists():
        return path.read_text()
    return "(Dockerfile.test not found)"


# ---------------------------------------------------------------------------
# Claude improvement loop
# ---------------------------------------------------------------------------

def _format_previous_iterations(previous_iterations: list[IterationRecord]) -> str:
    if not previous_iterations:
        return ""
    lines = ["\n**Previous attempts (all failed) — do not repeat these approaches:**\n"]
    for it in previous_iterations:
        lines.append(f"Iteration {it.iteration}:")
        lines.append("  Dockerfile.test that was generated:")
        lines.append("  ```dockerfile")
        for line in it.dockerfile.strip().splitlines():
            lines.append(f"  {line}")
        lines.append("  ```")
        snippet = it.test_output.strip().splitlines()[-20:]
        lines.append("  Failure output (last 20 lines):")
        lines.append("  ```")
        for line in snippet:
            lines.append(f"  {line}")
        lines.append("  ```\n")
    return "\n".join(lines)


def update_envbuilder(
    repo: TargetRepo,
    dockerfile: str,
    test_output: str,
    iteration: int,
    max_iterations: int,
    previous_iterations: list[IterationRecord],
    session_id: str | None = None,
) -> tuple[bool, str | None]:
    """Ask Claude Code to fix internal/envbuilder and rebuild.

    Returns (source_changed, session_id) so the caller can resume the same
    conversation on subsequent iterations, preserving full context.

    Acquires _envbuilder_lock for the duration so parallel repo fix loops
    don't race on envbuilder.go or the binary.
    """
    source_rel = ENVBUILDER_SOURCE.relative_to(REPO_ROOT)

    print(f"  [{repo.name}] Waiting for envbuilder lock (iteration {iteration})...")
    with _envbuilder_lock:
        print(f"  [{repo.name}] Acquired envbuilder lock.")
        previous = ENVBUILDER_SOURCE.read_text()

        if session_id:
            prompt = f"""The fix did not work. Here is the updated failure (iteration {iteration}/{max_iterations}).

**Dockerfile.test that was generated:**
```dockerfile
{dockerfile}
```

**Test failure output:**
```
{test_output}
```

Please analyse what went wrong, revise your approach, and edit {source_rel} again.
After editing, run `go build -o {BINARY.relative_to(REPO_ROOT)} .` to verify it compiles."""
        else:
            prompt = f"""You are debugging an environment detection tool inside the chunk CLI.

`chunk sandbox env` analyses a repository, detects its tech stack, and writes a
Dockerfile.test for running that repo's tests. It was run on the
{repo.name} repo ({repo.url}).

The core logic lives in: {source_rel}  (relative to your working directory)
{_format_previous_iterations(previous_iterations)}
**Current Dockerfile.test (iteration {iteration}):**
```dockerfile
{dockerfile}
```

**Current failure output:**
```
{test_output}
```

The tests did not pass (iteration {iteration}/{max_iterations}). Analyse the failure,
identify the root cause, and edit {source_rel} to fix it.
After editing, run `go build -o {BINARY.relative_to(REPO_ROOT)} .` to verify it compiles.

Common issues to consider:
- Missing test dependencies (e.g. needing `pip install -e ".[dev]"` or a requirements/tests.txt)
- Rust workspaces needing `cargo test --workspace`
- Wrong working directory assumptions
- Missing system packages
- Incorrect base image or package manager detection"""

        print(f"  [{repo.name}] Asking Claude to analyse failure and update {source_rel}...")

        new_session_id: str | None = None

        async def run():
            nonlocal new_session_id
            options = ClaudeAgentOptions(
                cwd=str(REPO_ROOT),
                allowed_tools=["Read", "Edit", "Bash"],
                permission_mode="acceptEdits",
            )
            if session_id:
                options.resume = session_id
            async for message in query(prompt=prompt, options=options):
                if isinstance(message, AssistantMessage):
                    for block in message.content:
                        if isinstance(block, TextBlock) and block.text.strip():
                            print(f"  [{repo.name}] [agent] {block.text.strip()}")
                elif isinstance(message, ResultMessage):
                    new_session_id = message.session_id
                    print(f"  [{repo.name}] [agent done] {message.result[:200]}")

        try:
            anyio.run(run)
        except Exception as e:
            print(f"  [{repo.name}] ERROR: Agent SDK failed: {e}")
            return False, None

        updated = ENVBUILDER_SOURCE.read_text()
        diff = "".join(difflib.unified_diff(
            previous.splitlines(keepends=True),
            updated.splitlines(keepends=True),
            fromfile=f"{source_rel} (before)",
            tofile=f"{source_rel} (after)",
        ))

        if not diff:
            print(f"  [{repo.name}] No changes made to envbuilder.")
            return False, new_session_id

        print(f"  [{repo.name}] Source updated (diff below):")
        for line in diff.splitlines():
            print(f"    {line}")

        print(f"  [{repo.name}] Verifying build...")
        try:
            build_binary()
            print(f"  [{repo.name}] Build verified.")
            _envbuilder_changes.append(EnvBuilderChange(
                triggered_by_repo=repo.name,
                iteration=iteration,
                diff=diff,
                rebuild_ok=True,
            ))
            return True, new_session_id
        except RuntimeError as e:
            error_msg = str(e)
            print(f"  [{repo.name}] Build FAILED:\n{error_msg}")
            _envbuilder_changes.append(EnvBuilderChange(
                triggered_by_repo=repo.name,
                iteration=iteration,
                diff=diff,
                rebuild_ok=False,
                rebuild_error=error_msg,
            ))
            return False, new_session_id


# ---------------------------------------------------------------------------
# Env detection helpers
# ---------------------------------------------------------------------------

def normalize_env(env: dict) -> dict:
    """Normalize an env spec so argument order doesn't affect equality checks."""
    def sort_args(cmd: str) -> str:
        """Sort arguments within each &&-separated segment independently.
        Also sort comma-separated lists inside [...] within each token."""
        def normalize_token(token: str) -> str:
            # Sort comma-separated items inside [...] e.g. ".[b,a,c]" → ".[a,b,c]"
            if "[" in token and "]" in token:
                pre, rest = token.split("[", 1)
                items, post = rest.split("]", 1)
                return pre + "[" + ",".join(sorted(items.split(","))) + "]" + post
            return token

        def sort_segment(segment: str) -> str:
            tokens = segment.split()
            if len(tokens) <= 1:
                return segment
            normalized = [normalize_token(t) for t in tokens[1:]]
            return tokens[0] + " " + " ".join(sorted(normalized))

        segments = [s.strip() for s in cmd.split("&&")]
        return " && ".join(sort_segment(s) for s in segments)

    return {
        **env,
        "install": sort_args(env.get("install", "")),
        "test": sort_args(env.get("test", "")),
        "system_deps": sorted(env.get("system_deps", [])),
    }


def update_repos_json(repo_name: str, env: dict) -> None:
    """Persist a passing env spec for repo_name back into acceptance/repos.json."""
    with _repos_json_lock:
        repos = json.loads(REPOS_JSON_PATH.read_text())
        for entry in repos:
            if entry["name"] == repo_name:
                entry["env"] = env
                break
        REPOS_JSON_PATH.write_text(json.dumps(repos, indent=2) + "\n")
    print(f"  Stored env spec for {repo_name} in repos.json")


# ---------------------------------------------------------------------------
# Per-repo loop
# ---------------------------------------------------------------------------

def run_repo(repo: TargetRepo, timeout: int, max_iterations: int, persistent_cache_dir: Path | None) -> RepoRecord:
    record = RepoRecord(repo=repo, passed=False)

    own_cache = None
    if persistent_cache_dir:
        cache_dir = persistent_cache_dir
    else:
        own_cache = Path(tempfile.mkdtemp(prefix=f"chunk-env-harness-{repo.name}-"))
        cache_dir = own_cache
    clone_dir = cache_dir / f"chunk-sandbox-{repo.name}"

    try:
        # Phase 1: env-only — clone + detect env (no docker).
        print("Running env detection (clone + sandbox env)...")
        current_env, env_output = run_env_only_test(repo, cache_dir, timeout)
        if current_env is None:
            print(f"  Env detection failed:\n{env_output}")
            return record

        if normalize_env(current_env) == normalize_env(repo.env or {}):
            print(f"\n✓ [{repo.name}] Env unchanged — skipping build.")
            record.passed = True
            return record

        if repo.env is not None:
            print("  Env changed — running full build+test.")

        # Phase 2: full build+test loop. Clone already exists so no re-cloning.
        session_id: str | None = None
        for iteration in range(1, max_iterations + 1):
            print(f"\n{'=' * 52}")
            print(f"  [{repo.name}] Iteration {iteration}/{max_iterations}")
            print(f"{'=' * 52}")

            print("Running acceptance test (build + run)...")
            tests_ok, test_output = run_acceptance_test(repo, cache_dir, timeout)
            dockerfile = read_dockerfile(repo, cache_dir)

            if tests_ok:
                print(f"\n✓ [{repo.name}] Tests passed on iteration {iteration}!")
                record.passed = True
                record.iterations.append(IterationRecord(
                    iteration=iteration,
                    dockerfile=dockerfile,
                    tests_ok=True,
                    test_output=test_output,
                    envbuilder_updated=False,
                ))
                # Read the env.json the acceptance test just wrote — this
                # reflects any envbuilder fixes applied during the loop.
                env_json_path = clone_dir / "env.json"
                try:
                    current_env = json.loads(env_json_path.read_text())
                except (OSError, json.JSONDecodeError):
                    pass
                if normalize_env(current_env) != normalize_env(repo.env or {}):
                    update_repos_json(repo.name, current_env)
                    repo.env = current_env
                return record

            print(f"Tests FAILED on iteration {iteration}.")
            updated, session_id = update_envbuilder(
                repo, dockerfile, test_output, iteration, max_iterations,
                record.iterations, session_id,
            )
            record.iterations.append(IterationRecord(
                iteration=iteration,
                dockerfile=dockerfile,
                tests_ok=False,
                test_output=test_output,
                envbuilder_updated=updated,
            ))
    finally:
        if own_cache:
            shutil.rmtree(own_cache, ignore_errors=True)

    print(f"\n✗ [{repo.name}] Tests did not pass after {max_iterations} iterations.")
    return record


# ---------------------------------------------------------------------------
# Report
# ---------------------------------------------------------------------------

def print_report(records: list[RepoRecord]) -> None:
    width = 60
    print("\n")
    print("=" * width)
    print("  ENVIRONMENT HARNESS RUN REPORT")
    print("=" * width)

    for record in records:
        repo = record.repo
        status = "PASSED" if record.passed else "FAILED"
        print(f"\n{'─' * width}")
        ref_str = f" @ {repo.ref[:12]}" if repo.ref else " @ HEAD"
        print(f"  {repo.name.upper()}  [{status}]  {repo.url}{ref_str}")
        print(f"{'─' * width}")

        for it in record.iterations:
            outcome = "✓ tests passed" if it.tests_ok else "✗ tests failed"
            print(f"\n  Iteration {it.iteration}: {outcome}")

            print("\n    Dockerfile.test:")
            for line in it.dockerfile.strip().splitlines():
                print(f"      {line}")

            if not it.tests_ok and it.test_output:
                snippet = it.test_output.strip().splitlines()[-15:]
                print("\n    Test failure (last 15 lines):")
                for line in snippet:
                    print(f"      {line}")

            if it.envbuilder_updated:
                print("\n    → envbuilder was updated after this iteration")

    print(f"\n{'─' * width}")
    print(f"  ENVBUILDER CHANGES  ({len(_envbuilder_changes)} total)")
    print(f"{'─' * width}")

    if not _envbuilder_changes:
        print("\n  No changes were made to envbuilder.")
    else:
        for i, change in enumerate(_envbuilder_changes, 1):
            rebuild_status = (
                "rebuild OK" if change.rebuild_ok
                else f"rebuild FAILED: {change.rebuild_error.splitlines()[0]}"
            )
            print(f"\n  Change {i}: triggered by {change.triggered_by_repo} "
                  f"iteration {change.iteration} — {rebuild_status}")
            print()
            for line in change.diff.splitlines():
                print(f"    {line}")

    print(f"\n{'─' * width}")
    print("  SUMMARY")
    print(f"{'─' * width}")
    for record in records:
        iters_taken = len(record.iterations)
        status = "✓ passed" if record.passed else "✗ failed"
        print(f"  {record.repo.name:<12} {status}  ({iters_taken} iteration{'s' if iters_taken != 1 else ''})")
    print(f"\n  envbuilder changes: {len(_envbuilder_changes)}")
    print("=" * width)


# ---------------------------------------------------------------------------
# Entry point
# ---------------------------------------------------------------------------

def main():
    known_names = [r["name"] for r in KNOWN_REPOS]

    parser = argparse.ArgumentParser(
        description="environment: harness for chunk sandbox env/build. "
                    "Uses Claude to improve envbuilder on failure.",
    )
    parser.add_argument(
        "--repo",
        metavar="NAME_OR_URL",
        action="append",
        default=[],
        help=f"repo to run by nickname or Git URL; may be repeated "
             f"(known nicknames: {', '.join(known_names)})",
    )
    parser.add_argument(
        "--timeout",
        metavar="SECONDS",
        type=int,
        default=900,
        help="acceptance test timeout in seconds per iteration (default: 900)",
    )
    parser.add_argument(
        "--cache-dir",
        metavar="DIR",
        default=None,
        help="persist repo clones in DIR across runs (default: per-run temp dir)",
    )
    parser.add_argument(
        "--iterations",
        metavar="N",
        type=int,
        default=MAX_ITERATIONS,
        help=f"max improvement iterations per repo (default: {MAX_ITERATIONS})",
    )
    parser.add_argument(
        "--parallelism",
        metavar="N",
        type=int,
        default=4,
        help="number of repos to process in parallel (default: 4)",
    )
    parser.add_argument(
        "--log-file",
        metavar="PATH",
        default=None,
        help="write all output to PATH in addition to stdout "
             "(default: harness/logs/YYYYMMDD-HHMMSS.log)",
    )
    args = parser.parse_args()

    log_path = Path(
        args.log_file
        if args.log_file
        else Path(__file__).parent / "logs" / f"{datetime.datetime.now().strftime('%Y%m%d-%H%M%S')}.log"
    )
    log_path.parent.mkdir(parents=True, exist_ok=True)
    log_file = open(log_path, "w", buffering=1, encoding="utf-8")  # line-buffered  # pylint: disable=consider-using-with
    sys.stdout = _Tee(sys.__stdout__, log_file)
    sys.stderr = _Tee(sys.__stderr__, log_file)
    print(f"Logging to {log_path}")

    persistent_cache_dir = Path(args.cache_dir) if args.cache_dir else None
    if persistent_cache_dir:
        persistent_cache_dir.mkdir(parents=True, exist_ok=True)

    repos = resolve_repos(args.repo)

    print("Building chunk binary...")
    build_binary()

    parallelism = min(args.parallelism, len(repos))
    print(f"Running {len(repos)} repo(s) with parallelism={parallelism}...")

    def run_one(repo: TargetRepo) -> RepoRecord:
        print(f"\n{'#' * 52}")
        print(f"  Target: {repo.name} ({repo.url})")
        print(f"{'#' * 52}")
        return run_repo(repo, timeout=args.timeout, max_iterations=args.iterations,
                        persistent_cache_dir=persistent_cache_dir)

    records_by_name: dict[str, RepoRecord] = {}
    with ThreadPoolExecutor(max_workers=parallelism) as executor:
        futures = {executor.submit(run_one, repo): repo for repo in repos}
        for future in as_completed(futures):
            record = future.result()
            records_by_name[record.repo.name] = record

    # Restore original order for the report.
    records = [records_by_name[repo.name] for repo in repos]

    print_report(records)

    sys.exit(0 if all(r.passed for r in records) else 1)


if __name__ == "__main__":
    main()
