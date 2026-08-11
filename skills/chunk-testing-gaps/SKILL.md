---
name: chunk-testing-gaps
description: >-
  Use when asked to "find testing gaps", "chunk testing-gaps", "mutation test",
  "mutate this code", "test mutation coverage", or "find surviving mutants".
  Runs a 4-stage mutation testing process: discovery, validation on parallel
  sidecars, production cross-reference, and risk assessment.
version: 2.0.0
---

# Chunk Mutate Skill

Perform mutation testing on the current codebase to find gaps in test coverage.

## Goal

Perform mutation testing on this codebase. A **mutation** is a small, deliberate change to production code that mimics a realistic bug a human might introduce. A **mutant** is the resulting modified codebase. If the test suite catches the bug (tests fail), the mutant is **killed**. If tests still pass, the mutant **survives** — exposing a gap in test coverage.

The objective is to find as many surviving mutants as possible, prioritised by risk.

## Prerequisites

This prompt expects to be run in the context of a local clone of a VCS project. If that is not the case, stop and explain why you cannot proceed.

Before any testing, ensure local dependencies are running (`docker compose up -d` or equivalent). On compose failure, check whether containers from another project are occupying ports and kill them.

Stage 2 runs mutants on `chunk` sidecars, which needs `validation.sidecarImage` set in `.chunk/config.json`. Check it with `cat .chunk/config.json`. If it is missing, stop and tell the user to run the one-time setup in the `chunk-sidecar` skill (Step 3) first — booting a fresh unconfigured sidecar per mutant is slow enough to make the whole run impractical.

## Never create VCS artifacts for a mutant

A mutant is a deliberately broken copy of the code. It must never reach the remote.

- **Do not** push branches, open pull requests (draft or otherwise), or create git worktrees for mutants.
- **Do not** try to get CI to run a mutant. CI is not the execution environment for this skill; sidecars are.
- Mutations are carried as patches in a variants file and applied on a throwaway sidecar. Nothing is committed and your working tree is left as you found it.

Earlier versions of this skill pushed a branch per mutant and opened a draft PR to force a CI run. That flooded repositories with stale draft PRs. If you find yourself reaching for `git push` or `gh pr create` in Stage 2, you have taken a wrong turn — re-read Stage 2.

## What to Mutate

### Priority targets (highest to lowest)

1. **Security and auth**: authentication checks, permission gates, encryption, token validation, input sanitisation
2. **Control flow**: condition inversions (`==` ↔ `!=`, `<` ↔ `<=`), removed or inverted `if` branches, swapped `switch`/`case` fall-throughs, early returns removed or added
3. **Validation**: removed or weakened input validation, boundary checks changed, nil/null guard removal
4. **State and values**: variables zeroed or hardcoded, default values changed, constants altered
5. **Error handling**: errors swallowed (replaced with nil/null), retry logic removed, fallback paths deleted

### What NOT to mutate

- Test code, test helpers, fakes, mocks, fixtures, or end-to-end test harnesses
- Logging, tracing, or metrics-only code blocks (spans, metric emissions, structured log calls) unless they also affect control flow
- Generated code or vendored dependencies

### Bias toward survivors

Focus mutations where test coverage is likely weakest. Look for these patterns:

- **Tests that test the mock**: the test and mock are tightly coupled — the mock returns a hardcoded value that happens to match a production default, so mutating that default is invisible to the test
- **Model coupling**: tests import production models/types and assert directly on their fields, meaning the test exercises serialisation rather than behaviour
- **Happy-path-only tests**: a function has multiple branches but only the success path is tested
- **Missing edge cases**: boundary values, empty collections, nil/null inputs, zero-length strings

## Method

### Stage 1 — Discovery

Investigate the codebase and identify candidate mutations. Use subagents to parallelise discovery across packages, modules, or directories — one subagent per logical area.

Each mutation must be assigned a unique sequential number (e.g. `MUT-001`, `MUT-002`, ...).

**Target density**: aim for roughly 1 mutation per 50 lines of production code (e.g. ~1000 mutations for a 50k LOC codebase). This is a guideline, not a hard rule — dense areas may yield more, sparse areas fewer.

**Time cap**: if no new viable mutation has been identified for 3 minutes, stop discovery and proceed with what you have.

For each candidate, record:
- Mutation number
- File path and line number(s)
- Description of the change
- Rationale (why this might survive)

### Stage 2 — Validation

Validation is two passes: a cheap local triage that discards the obvious kills, then a sidecar run for whatever is left. Triage first — it is what keeps the sidecar count, and therefore the cost, proportionate to the number of interesting mutants rather than to the number of candidates.

#### 2a — Local triage

For each candidate mutation, working in your normal checkout one at a time:

1. Apply the mutation with an in-place edit.
2. Run static analysis: linter, type checks, compilation — use the project's standard tooling (`./do`, `Taskfile`, or whatever is configured). If the project has no local tooling, check CI config for what tools are used and attempt to run them locally. **Do not install missing tools without prompting.**
3. Run targeted local tests: tests in the mutated package/module that reference the mutated function or type. For Go, use the race detector and a 1-minute timeout (`-race -timeout 1m`). If tests take longer than 1 minute, treat the mutation as viable and move on.
4. **Revert the edit** before moving to the next candidate. Never leave a mutation in the working tree.
5. **If static analysis or tests fail** → mark the mutant **killed locally** and drop it.
6. **If the mutant survives** → capture the change as a patch for pass 2b, then revert.

A patch is the output of `git diff` for that single mutation, captured before the revert.

#### 2b — Sidecar run

Collect every local survivor into a single variants file at `.chunk/variants.json` (gitignored) — a JSON array of objects with `id`, `description`, and `patch`:

```json
[
  {
    "id": "MUT-001",
    "description": "Inverted auth check in verifyToken",
    "patch": "diff --git a/pkg/auth/verify.go b/pkg/auth/verify.go\n@@ ... @@\n-\tif !ok {\n+\tif ok {\n"
  }
]
```

The `patch` value must be the literal `git diff` text with newlines escaped, and must apply cleanly against the current `HEAD`.

Then run the whole set in one command:

```
chunk validate variants .chunk/variants.json --parallel 5
```

This boots one throwaway sidecar per variant (5 at a time), applies that variant's patch there, runs the project's `remote: true` validate commands, and deletes the sidecar. Nothing is pushed and no PR is created.

It prints a JSON array, one entry per variant:

- `killed: true` → the test suite caught the bug. Mark as **killed**.
- `killed: false` with no `error` → the tests passed on broken code. Mark as **survivor** — this is a real coverage gap.
- `error` non-empty → the variant never ran (patch did not apply, sidecar failed to boot). Mark as **inconclusive** and either fix the patch and re-run just that variant, or report it as not assessed. Do not record an inconclusive variant as a survivor.

Raise `--parallel` for a faster run at higher concurrent cost; lower it if the org hits sidecar limits. Use `--name <command>` to run a single validate command instead of every remote one.

**Cleanup**: the command deletes its own sidecars, including on Ctrl-C, and sweeps any orphans from a previous crashed run before it starts. You do not need to clean up sidecars by hand. Do delete `.chunk/variants.json` when the run is done.

#### Stage 2 Summary

Once the variants run has completed, produce a summary table:

| # | Mutation | File | Line | Status |
|---|----------|------|------|--------|
| MUT-001 | Inverted auth check | `pkg/auth/verify.go` | 42 | Survivor |
| MUT-002 | Removed nil guard | `pkg/api/handler.go` | 118 | Killed (sidecar) |
| MUT-003 | Weakened length check | `pkg/api/parse.go` | 55 | Killed (local) |

Report any inconclusive variants separately, with the reason they did not run, so the user can see what was left unassessed rather than reading their absence as a pass.

### Stage 3 — Production Cross-Reference

Attempt to determine whether the surviving mutants' code paths are exercised in production. Use any available observability tooling — Honeycomb, Datadog, or other connected MCP servers or CLIs.

Concrete approaches:
- **Honeycomb**: query for traces spanning the service and function/handler containing the mutation over the last 7 days. Check span counts and error rates.
- **Datadog**: look for metrics on the relevant endpoint, service, or function. Check request volume and latency percentiles.
- **Other**: Kibana, Prometheus, New Relic, CloudWatch — whatever is available. Check logs, request counts, or dashboards referencing the mutated code path.

If no observability access is available, note this and skip to Stage 4.

Update the summary table with a **Production Traffic** column indicating: **High** (clear evidence of regular traffic), **Low** (occasional or indirect traffic), **None found** (no evidence), or **Unknown** (no observability access).

### Stage 4 — Risk Assessment

For each survivor, assess overall risk based on:

- **Severity of the mutation**: what could go wrong if this bug shipped? (auth bypass > cosmetic issue)
- **Production traffic**: is this code path actually hit?
- **Blast radius**: how many users/systems would be affected?
- **Detectability**: would monitoring/alerting catch this before users notice?

Assign a risk level: **Critical**, **High**, **Medium**, or **Low**.

Present the final summary table sorted by risk (highest first):

| # | Mutation | File | Line | Production Traffic | Risk | Rationale |
|---|----------|------|------|--------------------|------|-----------|
| MUT-001 | Inverted auth check | `pkg/auth/verify.go` | 42 | High | Critical | Auth bypass on a hot path, no test coverage |
| MUT-017 | Hardcoded timeout to 0 | `pkg/worker/poll.go` | 89 | Low | Medium | Would cause tight loop but only in batch worker |
