#!/usr/bin/env python3
"""Evaluate `chunk factory` on pinned tasks with hidden tests.

    check-tasks   run the oracle and nop agents: every task's hidden checks must
                  pass with its reference solution and fail without it
    run           run a job: every task x agent x attempt, one trial directory each
    report        aggregate a job's trials into report.md and report.json
    rejudge       judge a job's finished trials again (after a judge or policy change), reusing their results

A job directory is the unit of resumption: re-running `run` with the same
--job-dir skips completed trials and retries the rest. See harness/README.md.
"""

import argparse
import datetime
import hashlib
import json
import os
import shutil
import signal
import subprocess
import sys
import traceback
from concurrent.futures import ThreadPoolExecutor, as_completed
from pathlib import Path

import factory_report
from factory_agents import EVAL_REVIEWS, NotApplicable, load_agents, run_agent, summarize_factory
from factory_judge import JUDGE_MODEL, judge, judge_rewards
from factory_tasks import HARNESS_ROOT, Task, checkout, git, load_tasks, repo_clone, verify

REPO_ROOT = HARNESS_ROOT.parent
RESULTS_ROOT = HARNESS_ROOT / "results"


def now() -> str:
    return datetime.datetime.now(datetime.UTC).isoformat(timespec="seconds")


def write_json(path: Path, value) -> None:
    path.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")


def build_binary(binary: Path, source: Path = REPO_ROOT) -> None:
    result = subprocess.run(["go", "build", "-o", str(binary), "."], cwd=source,
                            capture_output=True, text=True, check=False)
    if result.returncode != 0:
        raise RuntimeError(f"go build failed:\n{result.stdout}{result.stderr}")


def job_env(job_dir: Path, echo: bool) -> dict:
    """The environment trials run in. The job gets a watch daemon of its own,
    started from its own binary: factory runs on the daemon, and a daemon from
    another build would otherwise be replaced, or be what is evaluated."""
    digest = hashlib.sha256(str(job_dir).encode()).hexdigest()[:10]
    env = {**os.environ, "CHUNK_WATCHD_DIR": f"/tmp/chunk-eval-{digest}",
           "CHUNK_EVAL_BINARY": str(job_dir / "chunk")}
    if echo:
        env["CHUNK_EVAL_ECHO"] = "1"
    return env


def stop_daemon(env: dict) -> None:
    pid_file = Path(env["CHUNK_WATCHD_DIR"]) / "watchd.pid"
    try:
        os.kill(int(pid_file.read_text(encoding="utf-8").strip()), signal.SIGTERM)
    except (OSError, ValueError):
        pass


def trial_dir(job_dir: Path, task: Task, agent: str, attempt: int) -> Path:
    return job_dir / "trials" / task.name / agent / f"{attempt:02d}"


def rewards(checks: list[dict]) -> dict:
    hidden = [c for c in checks if c["kind"] == "hidden"]
    suite = [c for c in checks if c["kind"] == "suite"]
    reward = {f"check_{c['name']}": int(c["passed"]) for c in checks}
    reward["hidden"] = round(sum(c["passed"] for c in hidden) / len(hidden), 3) if hidden else 1
    reward["suite"] = int(all(c["passed"] for c in suite))
    reward["solved"] = int(all(c["passed"] for c in checks))
    return reward


def run_trial(job_dir: Path, task: Task, agent_name: str, agent: dict, attempt: int, env: dict,
              use_judge: bool) -> dict:
    directory = trial_dir(job_dir, task, agent_name, attempt)
    if directory.exists():
        shutil.rmtree(directory)
    directory.mkdir(parents=True)
    record = {"task": task.name, "agent": agent_name, "attempt": attempt, "kind": agent["kind"],
              "revision": task.revision, "started_utc": now(), "status": "completed", "error": "",
              "metrics": {}, "reward": {}, "checks": []}
    write_json(directory / "config.json", {"agent": agent, "task": task.name, "revision": task.revision})
    clone = repo_clone(task)
    try:
        with checkout(clone, task.revision, "chunk-eval-") as project:
            result = run_agent(agent, task, project, directory, env)
            # Keep the work reachable after the checkout is removed.
            ref = f"refs/chunk-eval/{job_dir.name}/{task.name}/{agent_name}/{attempt:02d}"
            git(clone, "update-ref", ref, result.final)
    except NotApplicable as error:
        record.update(status="not_applicable", error=str(error), ended_utc=now())
        write_json(directory / "trial.json", record)
        return record
    except Exception as error:
        (directory / "harness-error.log").write_text(traceback.format_exc(), encoding="utf-8")
        record.update(status="errored", error=f"harness: {error}", ended_utc=now())
        write_json(directory / "trial.json", record)
        return record

    record.update(baseline=result.baseline, final=result.final, ref=ref, metrics=result.metrics)
    diff = git(clone, "diff", result.baseline, result.final, "--", ".", f":(exclude){EVAL_REVIEWS}", check=False)
    (directory / "diff.patch").write_text(diff + "\n" if diff else "", encoding="utf-8")
    if result.error:
        # The agent could not run; its score would say nothing about it.
        record.update(status="errored", error=result.error, ended_utc=now())
        write_json(directory / "trial.json", record)
        return record

    record["checks"] = verify(task, clone, result.final, directory / "verifier")
    record["reward"] = rewards(record["checks"])
    if use_judge and agent["kind"] not in ("oracle", "nop"):
        verdict = judge(task, record["checks"], diff, result.evidence)
        write_json(directory / "judge.json", verdict)
        record["reward"].update(judge_rewards(verdict))
        if verdict.get("judge_error"):
            record["judge_error"] = verdict["judge_error"]
    record["ended_utc"] = now()
    write_json(directory / "reward.json", record["reward"])
    write_json(directory / "trial.json", record)
    return record


def finished(job_dir: Path, task: Task, agent: str, attempt: int) -> bool:
    path = trial_dir(job_dir, task, agent, attempt) / "trial.json"
    if not path.exists():
        return False
    return json.loads(path.read_text(encoding="utf-8"))["status"] in ("completed", "not_applicable")


def describe(record: dict) -> str:
    if record["status"] != "completed":
        return f"{record['status']}: {record['error'][:200]}"
    failed = [c["name"] for c in record["checks"] if not c["passed"]]
    return "solved" if record["reward"].get("solved") else f"failed {', '.join(failed)}"


def run_job(job_dir: Path, tasks: list[Task], agent_names: list[str], attempts: int, concurrency: int,
            use_judge: bool, echo: bool, chunk_src: Path = REPO_ROOT) -> list[dict]:
    agents = load_agents()["agents"]
    unknown = [a for a in agent_names if a not in agents]
    if unknown:
        raise ValueError(f"unknown agent(s) {unknown}; available: {sorted(agents)}")
    job_dir.mkdir(parents=True, exist_ok=True)
    meta_path = job_dir / "job.json"
    if not meta_path.exists():
        dirty = bool(git(REPO_ROOT, "status", "--porcelain", check=False))
        write_json(meta_path, {
            "started_utc": now(), "harness_revision": git(REPO_ROOT, "rev-parse", "HEAD"), "harness_dirty": dirty,
            "tasks": {t.name: t.revision for t in tasks}, "agents": {a: agents[a] for a in agent_names},
            "attempts": attempts, "judge_model": JUDGE_MODEL if use_judge else None,
            "chunk_src": str(chunk_src), "chunk_revision": git(chunk_src, "rev-parse", "HEAD"),
            "chunk_dirty": bool(git(chunk_src, "status", "--porcelain", check=False)),
        })
    # The binary is built once per job, so a resumed job evaluates the same code.
    if any(agents[a]["kind"] == "factory" for a in agent_names) and not (job_dir / "chunk").exists():
        print("Building chunk...", file=sys.stderr)
        build_binary(job_dir / "chunk", chunk_src)
    env = job_env(job_dir, echo)
    for task in tasks:
        repo_clone(task)  # clone once, before trials race to do it

    pending = [(t, a, n) for t in tasks for a in agent_names for n in range(1, attempts + 1)
               if not finished(job_dir, t, a, n)]
    print(f"{len(pending)} trial(s) to run in {job_dir}", file=sys.stderr)
    records = []
    try:
        with ThreadPoolExecutor(max_workers=concurrency) as pool:
            futures = {pool.submit(run_trial, job_dir, t, a, agents[a], n, env, use_judge): (t, a, n)
                       for t, a, n in pending}
            for future in as_completed(futures):
                task, agent, attempt = futures[future]
                record = future.result()
                records.append(record)
                print(f"  {task.name} / {agent} #{attempt}: {describe(record)}", file=sys.stderr)
    finally:
        stop_daemon(env)
    factory_report.write(job_dir)
    return records


def rejudge_trial(job_dir: Path, record: dict, task: Task) -> str:
    """Judges a finished trial again from what it saved, without running it again."""
    directory = trial_dir(job_dir, task, record["agent"], record["attempt"])
    diff = (directory / "diff.patch").read_text(encoding="utf-8") if (directory / "diff.patch").exists() else ""
    if record["kind"] == "factory":
        try:
            result = json.loads((directory / "result.json").read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            result = {}
        evidence = {"run": summarize_factory(result)}
    else:
        log = directory / "agent.log"
        evidence = {"final_message": log.read_text(encoding="utf-8")[-4000:] if log.exists() else ""}
    verdict = judge(task, record["checks"], diff, evidence)
    previous = directory / "judge.json"
    if previous.exists() and not (directory / "judge-previous.json").exists():
        previous.rename(directory / "judge-previous.json")
    write_json(directory / "judge.json", verdict)
    record["reward"] = {k: v for k, v in record["reward"].items() if not k.startswith("judge_")}
    record["reward"].update(judge_rewards(verdict))
    record.pop("judge_error", None)
    if verdict.get("judge_error"):
        record["judge_error"] = verdict["judge_error"]
    write_json(directory / "reward.json", record["reward"])
    write_json(directory / "trial.json", record)
    return verdict.get("judge_error") or f"implementation {record['reward'].get('judge_implementation')}"


def rejudge(job_dir: Path, tasks: list[Task], concurrency: int) -> None:
    by_name = {t.name: t for t in tasks}
    work = [t for t in factory_report.load_trials(job_dir) if t["status"] == "completed"
            and t["kind"] not in ("oracle", "nop") and t["task"] in by_name]
    print(f"{len(work)} trial(s) to judge again in {job_dir}", file=sys.stderr)
    with ThreadPoolExecutor(max_workers=concurrency) as pool:
        futures = {pool.submit(rejudge_trial, job_dir, t, by_name[t["task"]]): t for t in work}
        for future in as_completed(futures):
            t = futures[future]
            try:
                print(f"  {t['task']} / {t['agent']} #{t['attempt']}: {future.result()}", file=sys.stderr)
            except Exception as error:  # one broken trial must not stop the rest
                print(f"  {t['task']} / {t['agent']} #{t['attempt']}: error {error}", file=sys.stderr)
    factory_report.write(job_dir)


def check_tasks(job_dir: Path, tasks: list[Task]) -> int:
    run_job(job_dir, tasks, ["oracle", "nop"], 1, 2, use_judge=False, echo=False)
    problems = []
    for record in factory_report.load_trials(job_dir):
        task, agent = record["task"], record["agent"]
        if record["status"] != "completed":
            problems.append(f"{task}: {agent} {record['status']}: {record['error']}")
            continue
        for check in record["checks"]:
            if agent == "oracle" and not check["passed"]:
                problems.append(f"{task}: {check['name']} fails with the reference solution")
            if agent == "nop" and check["kind"] == "hidden" and check["passed"]:
                problems.append(f"{task}: hidden check {check['name']} passes with no change")
            if agent == "nop" and check["kind"] == "suite" and not check["passed"]:
                problems.append(f"{task}: suite check {check['name']} fails at the baseline")
    for problem in problems:
        print(f"PROBLEM {problem}")
    print(f"{len(tasks)} task(s) checked, {len(problems)} problem(s). Trials: {job_dir}")
    return 1 if problems else 0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = parser.add_subparsers(dest="command", required=True)
    stamp = datetime.datetime.now(datetime.UTC).strftime("%Y%m%d-%H%M%S")

    check = sub.add_parser("check-tasks", help="verify every task with the oracle and nop agents")
    check.add_argument("--tasks", help="comma-separated task names (default: all)")
    check.add_argument("--job-dir", type=Path, default=RESULTS_ROOT / f"check-{stamp}")

    run = sub.add_parser("run", help="run tasks x agents x attempts")
    run.add_argument("--tasks", help="comma-separated task names (default: all)")
    run.add_argument("--agents", help="comma-separated agent names from agents.toml (default: its `default`)")
    run.add_argument("-k", "--attempts", type=int, default=3, help="trials per task and agent (default: 3)")
    run.add_argument("-n", "--concurrency", type=int, default=1,
                     help="trials at once (default: 1; each factory trial uses its own sidecars)")
    run.add_argument("--job-dir", type=Path, help="job directory; reuse one to resume (default: a new one)")
    run.add_argument("--no-judge", action="store_true", help="skip the LLM judge; rewards come from checks only")
    run.add_argument("--echo", action="store_true", help="stream factory progress to stderr")
    run.add_argument("--chunk-src", type=Path, default=REPO_ROOT,
                     help="checkout to build the chunk under test from (default: this repository)")

    report = sub.add_parser("report", help="aggregate a job's trials")
    report.add_argument("job_dir", type=Path)

    again = sub.add_parser("rejudge", help="judge a job's finished trials again under the current judge")
    again.add_argument("job_dir", type=Path)
    again.add_argument("-n", "--concurrency", type=int, default=6)

    args = parser.parse_args()
    if args.command == "report":
        factory_report.write(args.job_dir.resolve())
        print((args.job_dir / "report.md").read_text(encoding="utf-8"))
        return 0
    if args.command == "rejudge":
        rejudge(args.job_dir.resolve(), load_tasks(), args.concurrency)
        print((args.job_dir / "report.md").read_text(encoding="utf-8"))
        return 0
    tasks = load_tasks(args.tasks.split(",") if args.tasks else None)
    if args.command == "check-tasks":
        return check_tasks(args.job_dir.resolve(), tasks)
    if args.attempts < 1 or args.concurrency < 1:
        parser.error("--attempts and --concurrency must be at least 1")
    agents = args.agents.split(",") if args.agents else load_agents()["default"]
    job_dir = (args.job_dir or RESULTS_ROOT / stamp).resolve()
    run_job(job_dir, tasks, agents, args.attempts, args.concurrency, not args.no_judge, args.echo,
            args.chunk_src.resolve())
    print((job_dir / "report.md").read_text(encoding="utf-8"))
    return 0


if __name__ == "__main__":
    sys.exit(main())
