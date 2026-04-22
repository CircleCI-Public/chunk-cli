# Diff Analysis Tools

## cf_diff.py

`scripts/cf_diff.py` detects structural control-flow changes in unified diffs. Useful for quickly assessing whether a PR moves logic around vs. actually changes it.

```bash
gh pr diff 256 | python3 scripts/cf_diff.py
git diff HEAD~1 | python3 scripts/cf_diff.py
```

Exits 0 if no control-flow changes, 1 if any are found.

### What it reports

- **removed** — `if`, `for`, `switch`, `return`, `defer`, `go`, etc. that no longer exist in the hunk
- **added** — control-flow nodes that are new
- **re-nested** — same node type, different nesting depth (e.g. `return_statement: depth 1→0`)
- **reordered** — same set of nodes, different order

### Interpreting results

A balanced remove/add across files (logic deleted from one package, same logic added to another) indicates a structural move rather than a logic change — low risk. Unbalanced removals without corresponding additions warrant closer inspection.

Requires `tree_sitter_go` for accurate AST-based analysis; falls back to keyword + indentation heuristic if not installed.

---

## difftastic

`difft` is a syntax-aware diff tool. Unlike unified diff, it highlights changed *tokens* rather than changed lines, and understands language structure so moved code doesn't show as a wall of red/green.

Install: `brew install difftastic`

### Running against a PR branch

```bash
GIT_EXTERNAL_DIFF=difft git diff main...origin/<branch-name>
```

### Reading the output

- **Side-by-side layout** — old file on the left with old line numbers, new file on the right with new line numbers. The two sets of line numbers are independent.
- **`.` (dot)** — means nothing on that side. A dot on the right means the left line was deleted; a dot on the left means the right line was added.
- **`..` wrapped lines** — long lines wrap onto `..` continuation rows. It's the same logical line, not a new one.
- **Unchanged lines** — appear on both sides without highlighting (context only).
- **Highlighted tokens** — in a colour terminal, changed tokens are red (removed) or green (added) at the token level, not the whole line.
- **`--- N/M --- Go`** header — hunk N of M for this file; language name confirms syntactic parsing was used.
