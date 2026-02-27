---
name: start-legibility
description: Scaffold agent legibility files (AGENTS.md, ARCHITECTURE.md, .agents/docs/) in the current repository
---

Run `.claude/skills/start-legibility/setup.sh` from the repository root using Bash.

The script creates:
- `AGENTS.md` — entry point doc pointing to key documents
- `ARCHITECTURE.md` — pointer to per-subsystem architectural docs
- `.agents/docs/` — directory with subdirectories: `design-docs/`, `plans/`, `generated/`, `specs/`, `references/`
- `.agents/docs/PLANS.md`, `DESIGN.md`, `QUALITY_SCORE.md`, `RELIABILITY.md`, `SECURITY.md` — placeholder docs

All files are created only if they don't already exist. Run the script, then show the user what was created.
