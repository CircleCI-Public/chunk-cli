---
name: chunk-testing-gaps
description: >-
  Use when asked to "find testing gaps", "chunk testing-gaps", "mutation test",
  "mutate this code", "test mutation coverage", or "find surviving mutants".
  Runs a 4-stage mutation testing process: discovery, validation via git
  worktrees and CI, production cross-reference, and risk assessment.
version: 1.0.0
---

# Chunk Mutate Skill

Perform mutation testing on the current codebase to find gaps in test coverage.

## Goal

Perform mutation testing on this codebase. A **mutation** is a small, deliberate change to production code that mimics a realistic bug a human might introduce. A **mutant** is the resulting modified codebase. If the test suite catches the bug (tests fail), the mutant is **killed**. If tests still pass, the mutant **survives** — exposing a gap in test coverage.

The objective is to find as many surviving mutants as possible, prioritised by risk.

## Prerequisites

This prompt expects to be run in the context of a local clone of a VCS project. If that is not the case, stop and explain why you cannot proceed.

Before any testing, ensure local dependencies are running (`docker compose up -d` or equivalent). On compose failure, check whether containers from another project are occupying ports and kill them.

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

For each candidate mutation:

1. Create a git worktree on a new branch named `chunk/mut-<NUMBER>-<short-kebab-description>` (e.g. `chunk/mut-042-remove-auth-check`).
2. Apply the mutation.
3. Run static analysis: linter, type checks, compilation — use the project's standard tooling (`./do`, `Taskfile`, or whatever is configured). If the project has no local tooling, check CI config for what tools are used and attempt to run them locally. **Do not install missing tools without prompting.**
4. Run targeted local tests: tests in the mutated package/module that reference the mutated function or type. For Go, use the race detector and a 1-minute timeout (`-race -timeout 1m`). If tests take longer than 1 minute, consider the mutation viable and move on.
5. **If static analysis or tests fail** → mark the mutant as **killed locally**, delete the worktree, and move to the next candidate.
6. **If the mutant survives local validation** → push the branch to the remote and trigger or wait for the CI pipeline.

**Batching**: push surviving branches in batches (e.g. 10–20 at a time) rather than sequentially. Poll for CI results across the batch before pushing the next.

7. Monitor CI pipelines for each pushed branch. If CI fails → mark as **killed in CI**. If CI passes → mark as **survivor**.

**Cleanup**: after a mutant is killed (locally or in CI), remove the worktree promptly to avoid disk/git overhead.

#### Stage 2 Summary

Once all CI pipelines have completed, produce a summary table:

| # | Mutation | File | Line | Branch Link | Status |
|---|----------|------|------|-------------|--------|
| MUT-001 | Inverted auth check | `pkg/auth/verify.go` | 42 | `chunk/mut-001-invert-auth` | Survivor |
| MUT-002 | Removed nil guard | `pkg/api/handler.go` | 118 | `chunk/mut-002-rm-nil-guard` | Killed (CI) |

Include links to the branch in the VCS for each mutation.

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

| # | Mutation | File | Line | Branch | Production Traffic | Risk | Rationale |
|---|----------|------|------|--------|--------------------|------|-----------|
| MUT-001 | Inverted auth check | `pkg/auth/verify.go` | 42 | [link] | High | Critical | Auth bypass on a hot path, no test coverage |
| MUT-017 | Hardcoded timeout to 0 | `pkg/worker/poll.go` | 89 | [link] | Low | Medium | Would cause tight loop but only in batch worker |
