#!/usr/bin/env bash
# Creates the agent legibility scaffold in the current repository root.
set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"

# ── Directory structure ──────────────────────────────────────────────────────
mkdir -p "$REPO_ROOT/.agents/docs/design-docs"
mkdir -p "$REPO_ROOT/.agents/docs/plans"
mkdir -p "$REPO_ROOT/.agents/docs/generated"
mkdir -p "$REPO_ROOT/.agents/docs/specs"
mkdir -p "$REPO_ROOT/.agents/docs/references"

# ── Root files (skip if already exist) ──────────────────────────────────────
create_if_missing() {
  local path="$1"
  local content="$2"
  if [[ -f "$path" ]]; then
    echo "  skip  $path (already exists)"
  else
    echo "$content" > "$path"
    echo "  create $path"
  fi
}

create_if_missing "$REPO_ROOT/AGENTS.md" "# AGENTS.md

> Entry point for AI assistants working in this codebase.
> Keep this short — point to documents, don't duplicate them.

## Key Documents

- Architecture overview: [ARCHITECTURE.md](./ARCHITECTURE.md)
- Plans and roadmap: [.agents/docs/PLANS.md](.agents/docs/PLANS.md)
- Design decisions: [.agents/docs/DESIGN.md](.agents/docs/DESIGN.md)
- Quality standards: [.agents/docs/QUALITY_SCORE.md](.agents/docs/QUALITY_SCORE.md)
- Reliability: [.agents/docs/RELIABILITY.md](.agents/docs/RELIABILITY.md)
- Security: [.agents/docs/SECURITY.md](.agents/docs/SECURITY.md)

## Quick Start

<!-- Add the 2-3 commands an agent needs to build, test, and lint. -->
"

create_if_missing "$REPO_ROOT/ARCHITECTURE.md" "# ARCHITECTURE.md

> Pointers to architectural documentation for each core subsystem.
> Do not put architecture here — link to the canonical doc.

## Subsystems

<!-- For each subsystem, add a row:
| Subsystem | Doc | Description |
|-----------|-----|-------------|
| Example   | [design-docs/example.md](.agents/docs/design-docs/example.md) | What it does |
-->

| Subsystem | Doc | Description |
|-----------|-----|-------------|
|           |     |             |
"

# ── .agents/docs files ───────────────────────────────────────────────────────
create_if_missing "$REPO_ROOT/.agents/docs/PLANS.md" "# Plans

Active and upcoming work. Link to detailed plan files in \`plans/\`.

## Active

<!-- - [Plan name](plans/plan-name.md) — brief description -->

## Upcoming

<!-- - [Plan name](plans/plan-name.md) — brief description -->

## Completed

<!-- - [Plan name](plans/plan-name.md) -->
"

create_if_missing "$REPO_ROOT/.agents/docs/DESIGN.md" "# Design Decisions

Key design decisions and their rationale. Link to detailed docs in \`design-docs/\`.

## Decisions

<!-- | Decision | Doc | Status |
|----------|-----|--------|
| Example  | [design-docs/example.md](design-docs/example.md) | Accepted | -->
"

create_if_missing "$REPO_ROOT/.agents/docs/QUALITY_SCORE.md" "# Quality Score

Quality criteria and current status for this codebase.

## Criteria

<!-- Fill in scores and notes as the project matures. -->

| Dimension | Score | Notes |
|-----------|-------|-------|
| Test coverage | — | |
| Type safety | — | |
| Linting | — | |
| Documentation | — | |
"

create_if_missing "$REPO_ROOT/.agents/docs/RELIABILITY.md" "# Reliability

Reliability expectations, SLOs, and known failure modes.

## SLOs

<!-- Define objectives here when relevant. -->

## Known Failure Modes

<!-- Document known issues and mitigations. -->
"

create_if_missing "$REPO_ROOT/.agents/docs/SECURITY.md" "# Security

Security model, threat surface, and policies for this codebase.

## Threat Model

<!-- Describe the trust boundary and what we protect against. -->

## Policies

<!-- Auth requirements, secret handling, dependency policy, etc. -->
"

echo ""
echo "Done. Scaffold created at $REPO_ROOT"
