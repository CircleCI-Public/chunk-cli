# What we tried and what we learned

## What we tried
- **A proper eval setup.** Pinned tasks with hidden acceptance tests and a reference solution, checked automatically. Repeated trials, infrastructure failures excluded, an LLM judge only for what tests can't measure, and plain Claude Code (no review loop) as the baseline. 5 tasks in total; the comparisons used the 3 chunk-cli ones.
- **Three review sets:**
  - **the repo's prompts:** `adversarial.md`, `correctness.md` and `testing.md`, three long checklists with one reviewer each;
  - **calibrated:** the same three, with a `testing.md` that only rates a missing test high or medium when requested behaviour has no test, or a plausible one-line bug would slip past every test;
  - **consolidated:** one short generic prompt on one reviewer: "report only merge-blocking problems with a concrete failure scenario and evidence; omit nits, optional improvements, pre-existing issues, and speculative gaps."
- **End-to-end runs:** factory with the repo's and the calibrated prompts, against Claude Code, 2–3 runs per task.
- **A reviewer-only eval:** each review set on correct changes, to measure noise, and on 13 planted bugs (10 of which the hidden tests miss), to measure recall. Each finding was classified as block, report (real but outside the request) or noise.
- **A convergence breakdown** of why each factory round failed, and an **offline replay** of the recorded rounds with a triage step.

## What we learned

### Why factory doesn't converge
- **Reviews cause the failures, not validation.** 17 of 18 rounds failed, every time on a review.
- **`testing.md` is the main noise source.** It failed in 17 of 18 rounds, and about 70% of its findings on correct code were optional extra tests.
- **Reviewers follow consequences outward, and the implementer chases them.** One run grew to 17 files and about 900 lines, with new defects, and scored 0.42 against Claude Code's 0.88.
- **Duplicates and no memory make it worse.** The same issue came 4–6 times a round, and declined findings came back in later rounds.

### Review quality
- **The review signal is real.** Every review set caught every planted bug, and reviewers found real issues in Claude Code's "solved" code.
- **Gating on reviewer silence is the costly part.** Rounds on correct code would pass 8% of the time today, but 76% if only findings that should block counted.
- **The calibrated `testing.md` cut noise from 1.29 to 0.08 per review,** with no lost recall, and helped factory converge.
- **Consolidated had almost no noise and full recall** with one reviewer instead of three. But the planted bugs were too easy to show what it might miss.

### Factory versus Claude Code
- **Claude Code solved 9 of 9** in one pass, at about a tenth of the cost and a fifth of the time, with equal or better judged quality.
- **That reflects the loop's design and the tasks** (small and fully specified) more than review itself.
- **The replay estimates that triage would converge all 10 runs** (actually 4) in 1.8 rounds (actually 2.8).

### Evaluating agents
- **The automatic task checks paid off immediately.** They caught three harness issues that would have scored agents unfairly.
- **Planted bugs also test the tests.**
- **LLM judges need explicit written rules.** Without them, verdicts on the same finding varied from run to run.
- **"Worth a round" is the right question,** not just "is it real".

### Factory bugs found
- The acceptance-test gate always passes from cache.
- The workspace pull has no timeout, which hung two runs for about 1.5 hours.
- Run IDs collide when two runs start in the same second.
- Display truncation breaks UTF-8.
- `{{CHANGED_PACKAGES}}` breaks on nested Go modules.

## Limits
- **Small samples:** 3 tasks, 2–3 runs each.
- **The tasks favour a single pass.**
- **The judges are only partly checked against a human.**
- **The planted bugs were too easy.**
- **Claude Code ran locally, factory on sidecars.**
- **The replay is an estimate.**

## Open questions
- Does triage deliver the convergence the replay predicts?
- Is review once, as advice, better than gating?
- Does review earn its cost on larger, vaguer tasks?
- Can consolidated replace the three prompts?
