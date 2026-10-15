"""Eval tasks for `chunk factory`: loading, checkouts and the verifier.

A task is a directory under harness/tasks/:

    instruction.md          the prompt the agent is given, verbatim
    task.toml               pinned repository, tags, good_looks_like, verifier checks
    setup/                  optional files copied into the checkout before the agent runs
    reviews/                optional task-specific review prompts (see factory_agents)
    tests/                  hidden files copied over the agent's result before verifying; a
                            trailing .hidden is dropped (foo_test.go.hidden -> foo_test.go)
    solution/solution.patch reference change; the oracle agent applies it

The agent never sees tests/ or solution/. The verifier runs each check in a fresh
checkout of the agent's final commit with tests/ copied over it; a check passes
iff its command exits 0.
"""

import contextlib
import shutil
import subprocess
import tempfile
import threading
import time
import tomllib
from dataclasses import dataclass, field
from pathlib import Path
from typing import Iterator

HARNESS_ROOT = Path(__file__).parent
TASKS_ROOT = HARNESS_ROOT / "tasks"
CACHE_ROOT = HARNESS_ROOT / ".cache"
OUTPUT_TAIL = 4000
# Trials run in threads; git's worktree bookkeeping is not safe to change concurrently.
WORKTREE_LOCK = threading.Lock()


@dataclass
class Check:
    name: str
    kind: str  # "hidden" (the task's acceptance tests) or "suite" (existing validation, for regressions)
    command: str
    timeout: int = 600


@dataclass
class Task:  # pylint: disable=too-many-instance-attributes
    name: str
    path: Path
    instruction: str
    url: str
    ref: str
    revision: str
    description: str = ""
    tags: list[str] = field(default_factory=list)
    implementer_context: str = ""
    good_looks_like: dict = field(default_factory=dict)
    checks: list[Check] = field(default_factory=list)

    @property
    def solution(self) -> Path:
        return self.path / "solution" / "solution.patch"

    @property
    def reviews_dir(self) -> Path:
        return self.path / "reviews"


def load_task(path: Path) -> Task:
    meta = tomllib.loads((path / "task.toml").read_text(encoding="utf-8"))
    repo = meta["repo"]
    checks = [Check(**item) for item in meta.get("verifier", {}).get("checks", [])]
    if not checks:
        raise ValueError(f"task {path.name} has no verifier checks")
    for check in checks:
        if check.kind not in ("hidden", "suite"):
            raise ValueError(f"task {path.name}: check {check.name} has unknown kind {check.kind!r}")
    return Task(
        name=path.name, path=path,
        instruction=(path / "instruction.md").read_text(encoding="utf-8").strip(),
        url=repo["url"], ref=repo.get("ref", ""), revision=repo["revision"],
        description=meta.get("description", ""), tags=meta.get("tags", []),
        implementer_context=meta.get("implementer_context", ""),
        good_looks_like=meta.get("good_looks_like", {}), checks=checks,
    )


def load_tasks(names: list[str] | None = None) -> list[Task]:
    available = sorted(p for p in TASKS_ROOT.iterdir() if (p / "task.toml").is_file())
    tasks = [load_task(p) for p in available]
    if not names:
        return tasks
    by_name = {t.name: t for t in tasks}
    missing = [n for n in names if n not in by_name]
    if missing:
        raise ValueError(f"unknown task(s) {missing}; available: {sorted(by_name)}")
    return [by_name[n] for n in names]


def git(repo: Path, *args: str, check: bool = True) -> str:
    result = subprocess.run(["git", *args], cwd=repo, capture_output=True, text=True, check=False)
    if check and result.returncode != 0:
        raise RuntimeError(f"git {' '.join(args)} failed in {repo}:\n{result.stderr}")
    return result.stdout.strip()


def repo_clone(task: Task) -> Path:
    """A local clone of the task's repository with its pinned revision, shared by
    every trial. Factory branches are made in it, never in the developer's repo."""
    slug = task.url.rstrip("/").removesuffix(".git").split("github.com/")[-1].replace("/", "__")
    clone = CACHE_ROOT / "repos" / slug
    if not (clone / ".git").exists():
        clone.parent.mkdir(parents=True, exist_ok=True)
        subprocess.run(["git", "clone", "--quiet", task.url, str(clone)], check=True)
    if not has_commit(clone, task.revision):
        git(clone, "fetch", "--quiet", "origin", *([task.ref] if task.ref else []))
        if not has_commit(clone, task.revision):
            git(clone, "fetch", "--quiet", "origin", task.revision)
    return clone


def has_commit(repo: Path, revision: str) -> bool:
    return subprocess.run(["git", "cat-file", "-e", f"{revision}^{{commit}}"], cwd=repo,
                          capture_output=True, check=False).returncode == 0


@contextlib.contextmanager
def checkout(clone: Path, commit: str, prefix: str) -> Iterator[Path]:
    """A detached worktree of clone at commit, removed afterwards."""
    with tempfile.TemporaryDirectory(prefix=prefix) as temp_dir:
        # git names a worktree's bookkeeping after its directory, so it must be unique.
        path = Path(temp_dir) / Path(temp_dir).name
        with WORKTREE_LOCK:
            git(clone, "worktree", "add", "--quiet", "--detach", str(path), commit)
        try:
            yield path
        finally:
            with WORKTREE_LOCK:
                git(clone, "worktree", "remove", "--force", str(path), check=False)


HIDDEN_SUFFIX = ".hidden"


def copy_tree(source: Path, destination: Path) -> list[str]:
    """Copies every file under source into destination at the same relative path,
    dropping a trailing .hidden from file names.

    Hidden test files are kept in this repository as e.g. foo_test.go.hidden: as
    .go files they would be packages of chunk-cli's own module to every tool that
    lists changed Go files, though they only build inside a task's checkout."""
    copied = []
    if not source.is_dir():
        return copied
    for item in sorted(source.rglob("*")):
        if item.is_file():
            relative = item.relative_to(source)
            relative = relative.with_name(relative.name.removesuffix(HIDDEN_SUFFIX))
            target = destination / relative
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(item, target)
            copied.append(str(relative))
    return copied


def run_check(check: Check, cwd: Path, log: Path) -> dict:
    started = time.monotonic()
    try:
        result = subprocess.run(["bash", "-c", check.command], cwd=cwd, capture_output=True, text=True,
                                timeout=check.timeout, check=False)
        output, code, timed_out = result.stdout + result.stderr, result.returncode, False
    except subprocess.TimeoutExpired as error:
        partial = error.stdout or ""
        output = partial.decode(errors="replace") if isinstance(partial, bytes) else partial
        code, timed_out = None, True
    log.write_text(output, encoding="utf-8")
    return {"name": check.name, "kind": check.kind, "passed": code == 0, "exit_code": code,
            "timed_out": timed_out, "duration_s": round(time.monotonic() - started, 1),
            "output_tail": output[-OUTPUT_TAIL:]}


def verify(task: Task, clone: Path, commit: str, out_dir: Path) -> list[dict]:
    """Runs the task's checks against commit with the hidden tests copied in."""
    out_dir.mkdir(parents=True, exist_ok=True)
    with checkout(clone, commit, "chunk-eval-verify-") as path:
        copy_tree(task.path / "tests", path)
        return [run_check(check, path, out_dir / f"{check.name}.log") for check in task.checks]
