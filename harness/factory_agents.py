"""Agents an eval trial can run: factory profiles, a plain Claude Code baseline,
and the oracle and nop agents that check a task's verifier.

Every agent starts from the same baseline commit (the task's revision, plus its
setup/ files and the agent's review prompts committed on top) and leaves its
work as a commit the verifier checks out. Agents are configured in agents.toml.
"""

import json
import os
import signal
import subprocess
import sys
import threading
import time
import tomllib
from dataclasses import dataclass, field
from pathlib import Path

import anyio
from claude_agent_sdk import AssistantMessage, ClaudeAgentOptions, ResultMessage, TextBlock, query

from factory_tasks import HARNESS_ROOT, Task, copy_tree, git

AGENTS_FILE = HARNESS_ROOT / "agents.toml"
EVAL_REVIEWS = ".chunk/factory-eval-reviews"
COMMIT_ENV = {"GIT_AUTHOR_NAME": "chunk-eval", "GIT_AUTHOR_EMAIL": "chunk-eval@circleci.com",
              "GIT_COMMITTER_NAME": "chunk-eval", "GIT_COMMITTER_EMAIL": "chunk-eval@circleci.com"}


# A factory run's ID, and so its branch, is the second it started on the daemon
# (internal/factory/run.go), so runs of one repository that start in the same
# second collide. The daemon assigns it after the client registers the project
# and starts the daemon, so spacing launches out is not enough: the next factory
# trial waits until the last one's branch exists.
_START_LOCK = threading.Lock()
DEFAULT_FACTORY_TIMEOUT_S = 4500
_BRANCH_WAIT_S = 180


def factory_branches(project: Path) -> set[str]:
    return set(git(project, "branch", "--list", "chunk/factory/*", "--format=%(refname:short)").split())


def wait_for_new_branch(project: Path, before: set[str], process: subprocess.Popen) -> None:
    deadline = time.monotonic() + _BRANCH_WAIT_S
    while time.monotonic() < deadline and process.poll() is None:
        if factory_branches(project) - before:
            break
        time.sleep(0.5)
    # A second boundary between this run's ID and the next one's.
    time.sleep(1.1)


class NotApplicable(Exception):
    """The agent cannot run on this task, such as a review prompt it names not existing there."""


@dataclass
class AgentResult:
    baseline: str
    final: str = ""
    # error is why the agent could not run at all. It says nothing about the
    # agent's ability, so the trial is left out of the averages.
    error: str = ""
    metrics: dict = field(default_factory=dict)
    # evidence is what the judge is shown about how the agent worked.
    evidence: dict = field(default_factory=dict)


def load_agents() -> dict:
    return tomllib.loads(AGENTS_FILE.read_text(encoding="utf-8"))


def commit_all(path: Path, message: str) -> str:
    """Commits everything in the checkout and returns HEAD, committed or not."""
    env = {**os.environ, **COMMIT_ENV}
    subprocess.run(["git", "add", "-A"], cwd=path, check=True, env=env)
    if subprocess.run(["git", "diff", "--cached", "--quiet"], cwd=path, check=False).returncode != 0:
        subprocess.run(["git", "-c", "commit.gpgsign=false", "commit", "--quiet", "--no-verify", "-m", message],
                       cwd=path, check=True, env=env)
    return git(path, "rev-parse", "HEAD")


def resolve_reviews(entries: list[str], task: Task, project: Path) -> list[Path]:
    """Expands an agent's review entries, in order; a later file with the same name
    replaces an earlier one, so a task can specialise a generic lens.

        "repo"          every prompt in the checkout's .chunk/reviews
        "repo/<file>"   one prompt from the checkout's .chunk/reviews
        "task"          every prompt in the task's reviews/ directory
        "<path>"        a prompt file relative to harness/
    """
    chosen: dict[str, Path] = {}

    def add(path: Path) -> None:
        chosen.pop(path.name, None)
        chosen[path.name] = path

    for entry in entries:
        if entry == "repo":
            for path in sorted((project / ".chunk/reviews").glob("*.md")):
                add(path)
        elif entry.startswith("repo/"):
            path = project / ".chunk/reviews" / entry.removeprefix("repo/")
            if not path.is_file():
                raise NotApplicable(f"{task.name} has no review prompt {entry}")
            add(path)
        elif entry == "task":
            for path in sorted(task.reviews_dir.glob("*.md")):
                add(path)
        else:
            path = HARNESS_ROOT / entry
            if not path.is_file():
                raise RuntimeError(f"review prompt not found: {path}")
            add(path)
    if not chosen:
        raise NotApplicable(f"no review prompts for {task.name}")
    return list(chosen.values())


def prepare(agent: dict, task: Task, project: Path, trial_dir: Path) -> str:
    """Installs the task's setup files and the agent's review prompts, and commits
    them as the baseline every agent starts from."""
    copy_tree(task.path / "setup", project)
    if agent["kind"] == "factory":
        reviews = resolve_reviews(agent.get("reviews", ["repo"]), task, project)
        target, captured = project / EVAL_REVIEWS, trial_dir / "reviews"
        target.mkdir(parents=True, exist_ok=True)
        captured.mkdir(parents=True, exist_ok=True)
        for position, source in enumerate(reviews, 1):
            name = f"{position:02d}-{source.name}"
            text = source.read_text(encoding="utf-8")
            (target / name).write_text(text, encoding="utf-8")
            (captured / name).write_text(text, encoding="utf-8")
    return commit_all(project, "chunk eval baseline: task setup and review prompts")


def instructions(agent: dict, task: Task, key: str) -> str:
    parts = [agent.get(key, "").strip(), task.implementer_context.strip()]
    return "\n\n".join(p for p in parts if p)


def run_agent(agent: dict, task: Task, project: Path, trial_dir: Path, env: dict) -> AgentResult:
    baseline = prepare(agent, task, project, trial_dir)
    kind = agent["kind"]
    if kind == "nop":
        return AgentResult(baseline=baseline, final=baseline)
    if kind == "oracle":
        subprocess.run(["git", "apply", "--whitespace=nowarn", str(task.solution)], cwd=project, check=True)
        return AgentResult(baseline=baseline, final=commit_all(project, "oracle solution"))
    if kind == "claude-code":
        return run_claude_code(agent, task, project, trial_dir, baseline)
    if kind == "factory":
        return run_factory(agent, task, project, trial_dir, baseline, env)
    raise ValueError(f"unknown agent kind {kind!r}")


BASE_CLAUDE_PROMPT = """You are working in a disposable copy of the repository.
Make the requested change by editing files. When you finish, reply with a short summary of what you changed."""


def run_claude_code(agent: dict, task: Task, project: Path, trial_dir: Path, baseline: str) -> AgentResult:
    """One Claude Code session on this machine, with no review loop: the baseline
    the factory loop has to beat."""
    extra = instructions(agent, task, "instructions")
    system = BASE_CLAUDE_PROMPT + (f"\n\nRun-specific instructions:\n{extra}" if extra else "")
    transcript, result_message = [], {}

    async def run() -> None:
        options = ClaudeAgentOptions(cwd=str(project), permission_mode="bypassPermissions",
                                     system_prompt={"type": "preset", "preset": "claude_code", "append": system},
                                     model=agent.get("model") or None)
        async for message in query(prompt=task.instruction, options=options):
            if isinstance(message, AssistantMessage):
                transcript.extend(block.text for block in message.content if isinstance(block, TextBlock))
            elif isinstance(message, ResultMessage):
                result_message.update(cost=message.total_cost_usd or 0.0, duration_ms=message.duration_ms,
                                      turns=message.num_turns, is_error=message.is_error, result=message.result)

    started = time.monotonic()
    error = ""
    try:
        anyio.run(_with_timeout, run, agent.get("timeout", 1800))
    except TimeoutError:
        error = "claude-code timed out"
    except Exception as exc:
        error = f"claude-code failed: {exc}"
    (trial_dir / "agent.log").write_text("\n\n".join(transcript), encoding="utf-8")
    final = commit_all(project, "claude-code result")
    metrics = {"duration_s": round(time.monotonic() - started, 1),
               "cost_usd": round(result_message.get("cost", 0.0), 4), "turns": result_message.get("turns")}
    if result_message.get("is_error") and not error:
        error = f"claude-code ended with an error: {result_message.get('result', '')[:500]}"
    return AgentResult(baseline=baseline, final=final, error=error, metrics=metrics,
                       evidence={"final_message": (result_message.get("result") or "")[-4000:]})


async def _with_timeout(run, seconds: float) -> None:
    with anyio.fail_after(seconds):
        await run()


def factory_command(binary: Path, agent: dict, task: Task, trial_dir: Path) -> list[str]:
    command = [str(binary), "factory", "--attempts", str(agent.get("attempts", 3)), "--json", "--verbose",
               f"--log={trial_dir / 'factory.log'}", "--reviews", EVAL_REVIEWS,
               "--implementer-instructions", instructions(agent, task, "implementer_instructions")]
    for key, flag in (("reviewers", "--reviewers"), ("model", "--model"),
                      ("implement_timeout", "--implement-timeout"), ("review_timeout", "--review-timeout")):
        if agent.get(key):
            command.extend([flag, str(agent[key])])
    # Flags a build under test has that the others do not, such as --no-triage.
    command.extend(agent.get("args", []))
    return command


def run_factory(agent: dict, task: Task, project: Path, trial_dir: Path, baseline: str, env: dict) -> AgentResult:
    # Popen is kept alive while stderr is streamed to two destinations.
    # pylint: disable=consider-using-with
    started = time.monotonic()
    command = factory_command(Path(env["CHUNK_EVAL_BINARY"]), agent, task, trial_dir)
    with (trial_dir / "result.json").open("w", encoding="utf-8") as result_file, \
            (trial_dir / "progress.log").open("w", encoding="utf-8") as progress_file:
        with _START_LOCK:
            before = factory_branches(project)
            process = subprocess.Popen(command, cwd=project, stdin=subprocess.PIPE, stdout=result_file,
                                       stderr=subprocess.PIPE, text=True, bufsize=1, env=env,
                                       # chunk truncates display lines by bytes, which can split a character.
                                       errors="replace")
            assert process.stdin is not None and process.stderr is not None
            process.stdin.write(task.instruction)
            process.stdin.close()
            wait_for_new_branch(project, before, process)
        # A stuck run (such as a workspace pull that never returns) must not hold
        # the job: past the limit it is stopped the way Ctrl-C stops it.
        limit = agent.get("timeout", DEFAULT_FACTORY_TIMEOUT_S)
        watchdog = threading.Timer(limit, process.send_signal, [signal.SIGINT])
        watchdog.daemon = True
        watchdog.start()
        for line in process.stderr:
            if env.get("CHUNK_EVAL_ECHO"):
                sys.stderr.write(f"[{trial_dir.name}] {line}")
            progress_file.write(line)
            progress_file.flush()
        exit_code = process.wait()
        timed_out = not watchdog.is_alive() and watchdog.finished.is_set() and exit_code != 0
        watchdog.cancel()
    try:
        result = json.loads((trial_dir / "result.json").read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        result = {}
    summary = summarize_factory(result)
    summary.update(exit_code=exit_code, duration_s=round(time.monotonic() - started, 1))
    run = result.get("factory") or {}
    outcome = run.get("result")
    final, error = baseline, ""
    if run.get("branch") and git(project, "rev-parse", "--verify", "--quiet", run["branch"], check=False):
        final = git(project, "rev-parse", run["branch"])
    if run.get("worktree"):
        # The run leaves its worktree for a developer to carry on in; a trial does not.
        git(project, "worktree", "remove", "--force", run["worktree"], check=False)
    if timed_out:
        error = f"factory run exceeded the trial limit of {limit}s and was stopped"
    elif outcome not in ("passed", "exhausted", "stuck", "no_change"):
        error = result.get("error") or result.get("message") or f"factory ended without a result (exit {exit_code})"
    metrics = {key: summary[key] for key in ("duration_s", "implementer_cost_usd", "implementer_seconds",
                                             "reviewer_seconds", "reviewer_critical_path_seconds", "round_count")}
    metrics.update(cost_usd=summary["implementer_cost_usd"], factory_result=outcome)
    return AgentResult(baseline=baseline, final=final, error=str(error), metrics=metrics, evidence={"run": summary})


def summarize_factory(result: dict) -> dict:
    rounds = result.get("rounds") or []
    details = result.get("details") or []
    round_summaries = []
    reviewer_seconds = reviewer_critical_path = implementer_seconds = implementer_cost = 0.0
    for index, current in enumerate(rounds):
        review_timing = current.get("reviews") or []
        implementation = current.get("implement") or {}
        durations = [item.get("duration_ms", 0) for item in review_timing]
        reviewer_seconds += sum(durations) / 1000
        reviewer_critical_path += max(durations, default=0) / 1000
        implementer_seconds += implementation.get("duration_ms", 0) / 1000
        implementer_cost += implementation.get("cost_usd", 0)
        round_summaries.append({
            "round": index + 1,
            "implementation": implementation,
            "checks": [{key: check.get(key) for key in ("name", "status", "duration_ms", "error")}
                       for check in (current.get("checks") or [])],
            "reviews": (details[index].get("results") or []) if index < len(details) else [],
        })
    return {
        "state": result.get("state"), "factory": result.get("factory"), "rounds": round_summaries,
        "round_count": len(round_summaries),
        "implementer_seconds": round(implementer_seconds, 3), "implementer_cost_usd": round(implementer_cost, 6),
        "reviewer_seconds": round(reviewer_seconds, 3),
        "reviewer_critical_path_seconds": round(reviewer_critical_path, 3),
    }
