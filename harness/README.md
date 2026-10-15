# Factory eval harness

Two evals of `chunk factory`, both run with `uv run --project harness …` (or the
`task` wrappers):

- **`factory.py`**: end-to-end. Each agent implements a pinned task. Hidden tests
  it never saw then decide whether the work is right.
- **`review_eval.py`**: reviewers only. Factory's review prompts review a known-good
  change with one planted defect, and the eval measures whether they catch it.

## Tasks (`tasks/<name>/`)

| File | Purpose |
|---|---|
| `instruction.md` | The prompt, verbatim. Every behaviour the hidden tests check must be stated in it. |
| `task.toml` | Pinned `[repo]` revision, tags, `implementer_context` (environment facts appended to every agent's instructions), `good_looks_like` (for the judge), `[[verifier.checks]]`. |
| `setup/` | Optional. Copied into the checkout and committed before the agent starts. |
| `reviews/` | Optional. Task-specific review prompts; see `task` in `agents.toml`. |
| `tests/` | Hidden files. Copied over the agent's final commit before verifying, never shown to the agent. A trailing `.hidden` is dropped. |
| `solution/solution.patch` | Reference change, applied by the `oracle` agent. |

A check passes if its command exits 0 when run with `bash -c` in a fresh checkout
of the agent's final commit with `tests/` copied in.
- `hidden` checks are the acceptance tests.
- `suite` checks are existing validation, to catch regressions. They must pass at
  the baseline, so they skip the hidden tests (`-skip '^TestHiddenEval'`, `--exclude '**/*hidden-eval*'`).
  Go suites also skip `TestDelegateDestination_Close_DeliversToSegment`: at the pinned
  revisions it waits 5s for a background subprocess and times out on a loaded machine, so it
  measures machine speed, not the agent.

Rules for adding a task:
- Assert only what the instruction literally asks for, through a user-facing
  boundary, so any reasonable implementation passes.
- Never re-pin a task. Scores are only comparable at a fixed revision. Add new
  tasks against new revisions instead.
- Run `factory.py check-tasks`. It must report 0 problems: hidden checks pass with
  the solution and fail without it, and suite checks pass at the baseline.

Hidden Go tests are stored as `*_test.go.hidden`, and copying drops the `.hidden`. As
`.go` files they would look like packages of chunk-cli itself to anything that lists changed
Go files, such as `{{CHANGED_PACKAGES}}`.

## Agents (`agents.toml`)

| Kind | Description |
|---|---|
| `factory` | `chunk factory` with a profile: attempts, review prompts and implementer instructions. |
| `claude-code` | One local Claude Code session with no review loop. The baseline factory has to beat. |
| `oracle` | Applies the reference solution. |
| `nop` | Changes nothing. |

Each agent starts from the same baseline commit and leaves its work as a commit,
kept under `refs/chunk-eval/<job>/…` in the cached clone (`.cache/repos/`).

## Running

```bash
task factory-harness -- check-tasks
task factory-harness -- run -k 3                       # default agents x all tasks x 3
task factory-harness -- run --tasks react-search --agents factory-control,claude-code -k 1 --echo
task factory-harness -- report harness/results/<job>
```

A job is `results/<job>/`:
- `job.json`: harness revision, tasks, agent configs and judge model.
- `chunk`: the binary under test, built once per job.
- `trials/<task>/<agent>/<attempt>/`: `trial.json`, `reward.json`, `diff.patch`,
  `verifier/*.log`, the factory log and result, and `judge.json`.

Re-run `run` with the same `--job-dir` to resume: completed trials are skipped and
errored ones retried. A job has its own watch daemon (`CHUNK_DAEMON_DIR`) started
from its binary, so it never replaces your daemon or evaluates a different build.

`reward.json` holds:
- one entry per check;
- `hidden`: the fraction of hidden checks passed;
- `suite`;
- `solved`: every check passed;
- the judge's `judge_*` fields.

The judge (`factory_judge.py`, pinned model) only scores what tests can't: scope,
maintainability and review quality. Errored trials (infrastructure, not the agent)
are left out of every mean. Start with k ≥ 3: the run-to-run spread in `report.md`
tells you how big a difference has to be before it means anything.

## Running on CI

`.circleci/config.yml` has two workflows that stay off until a pipeline sets the `eval`
parameter. Ordinary pipelines (`eval=none`) run only the usual `ci` workflow.

| `eval` | Runs | Needs |
|---|---|---|
| `check` | `eval-check`: `check-tasks` on all tasks. Catches a rotted task, a changed CLI flag or a broken pinned repo. | Nothing secret. |
| `score` | `eval-check`, and `eval-score`: real trials, a report, and a failed job if too few factory trials are solved. | The `chunk-cli-eval` context. |

Set it up once in the project's **Project Settings → Triggers**: add a scheduled pipeline
and pass `eval` as a pipeline parameter. Weekly `check` and a less frequent `score` is a
reasonable start. A run can also be started by hand with the same parameter.

The `chunk-cli-eval` context must provide `CIRCLE_TOKEN`, `ANTHROPIC_API_KEY` and
`CIRCLECI_ORG_ID`: factory runs on sidecars, and the judge and Claude Code use the API key.

`eval-score` runs `factory.py run --min-solved`, which exits 1 when fewer than that share of
factory trials are solved, and also when none completed (all infrastructure errors, so
nothing was measured). Its defaults (2 tasks, 2 attempts, threshold 0.5) are a starting
point, not a tuned value: with this few trials one unlucky run moves the rate a lot, so read
`report.md` (stored as a job artifact) before trusting a single failure.

`harness/results/` holds every trial and the report; the built `chunk` binary is left out of
the CI artifact.

`chunk factory eval` (a hidden command, `harness/suites/`) is a separate, smaller check: it
scores review prompts against past changes whose problems are known, with no implementer. It
needs no tasks, so it is the cheaper of the two for tuning prompts. `review_eval.py` measures
noise on correct changes as well, and `factory.py` measures whether the whole loop produces
correct code.

## Adaptive experiment (`factory.py adaptive`)

The earlier harness, kept as a mode for exploring profiles on a single prompt. It has no
hidden tests: each pass runs factory once from a pinned baseline, Claude grades the pass
against `good_looks_like`, and an advisor picks the profile for the next pass.

| File | Purpose |
|---|---|
| `factory-experiment.json` | Pass count, attempts, profiles (reviews, reviewers, implementer instructions) and `good_looks_like`. |
| `factory-baseline.json` | The pinned chunk-cli revision each pass starts from. |
| `targets/<repo>/` | The same two files for another repository, passed with `--config`, `--baseline-file` and `--target-repo`. |

Prompts and reviews are shared with the tasks above: the default prompt is
`tasks/session-list-failed/instruction.md`.

```bash
task factory-harness -- adaptive                                   # configured passes
task factory-harness -- adaptive --passes 1 --profile targeted --no-advisor
task factory-harness -- adaptive --results-dir harness/results/<stamp> --resume
```

Results go to `results/<stamp>/`: one `pass-NN/` per pass with the factory log, diff and
`grade.json`, plus `leaderboard.md` and `best.json`.

## Review eval: noise (`review_eval.py`)

The main question is how often reviews flag things that aren't real issues, or
aren't worth sending the implementer round again. On a change that is already
correct, every high/medium finding starts another factory round.

Each factory agent's review set reviews correct changes, left uncommitted the way
factory's reviewers see them:
- `clean`: each task's reference solution.
- `trial-<agent>-N`: changes from a `factory.py` job that passed every hidden and
  suite check (`--from-job`). These are realistic implementations.
- `mutant-<name>`, optional (`--mutants`): the reference solution with a planted
  defect, from `review-cases/<task>/case.toml`. Use these to check that cutting
  noise doesn't cut real findings.

A judge with read-only access to the checkout classifies every high/medium finding:
- **Worth a round:** `defect`, `requirement-miss`, `test-gap`, `safety`.
- **Noise:** `optional-test`, `scope`, `speculative`, `incorrect`, `nit`,
  `pre-existing`.

The report gives each profile's noise per review, how often a correct change
passes clean, precision, and the share of noise from each review prompt.

The judge's line between worth and noise is a judgement call. Calibrate it
against your own:

```bash
task review-harness -- run --from-job harness/results/<factory job> -k 3
task review-harness -- export-labels harness/results/<review job>   # writes labels.csv
# fill in human_worth (yes/no) for a sample of rows
task review-harness -- report harness/results/<review job>          # adds judge-vs-human agreement
```
