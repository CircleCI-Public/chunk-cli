#!/usr/bin/env python3
"""
cf-diff: detect structural control-flow changes in unified diffs.

Usage:
    gh pr diff 250 | python scripts/cf_diff.py
    git diff HEAD~1 | python scripts/cf_diff.py
"""

import sys
import re
from dataclasses import dataclass, field
from typing import Optional

try:
    import tree_sitter_go as tsgo
    from tree_sitter import Language, Parser
    _PARSER = Parser(Language(tsgo.language()))
    HAS_TS = True
except ImportError:
    HAS_TS = False
    print("warning: tree_sitter_go not found, falling back to keyword heuristic", file=sys.stderr)

# Node types that represent control flow in tree-sitter's Go grammar
CF_TYPES = {
    "if_statement", "for_statement", "switch_statement",
    "type_switch_statement", "select_statement",
    "return_statement", "break_statement", "continue_statement",
    "goto_statement", "defer_statement", "go_statement",
}


def cf_structure(src: str) -> list:
    """Return (node_type, depth) for each control-flow node, in source order."""
    if HAS_TS:
        tree = _PARSER.parse(src.encode())
        result = []
        _walk(tree.root_node, 0, result)
        return result
    # Fallback: keyword scan with indentation as proxy for depth
    out = []
    for line in src.splitlines():
        m = re.search(r'\b(if|for|switch|select|return|break|continue|goto|defer|go)\b', line)
        if m:
            depth = len(line) - len(line.lstrip('\t '))
            out.append((m.group(1), depth))
    return out


def _walk(node, depth: int, acc: list):
    if node.type in CF_TYPES:
        acc.append((node.type, depth))
    for child in node.children:
        _walk(child, depth + (1 if node.type in CF_TYPES else 0), acc)


@dataclass
class Hunk:
    file: str
    header: str
    removed: list = field(default_factory=list)
    added:   list = field(default_factory=list)
    context: list = field(default_factory=list)


def parse_diff(text: str) -> list:
    hunks = []
    current_file = ""
    current: Optional[Hunk] = None
    seen = set()  # deduplicate if gh outputs diff twice

    lines = text.splitlines()
    # gh sometimes emits the full diff twice; truncate at the second copy
    first_diff = next((i for i, l in enumerate(lines) if l.startswith("diff --git ")), None)
    if first_diff is not None:
        second = next((i for i, l in enumerate(lines) if i > first_diff and l == lines[first_diff]), None)
        if second is not None:
            lines = lines[:second]

    for line in lines:
        if line.startswith("diff --git "):
            m = re.search(r"b/(.+)$", line)
            current_file = m.group(1) if m else ""
            current = None
        elif line.startswith("@@"):
            key = (current_file, line)
            if key in seen:
                current = None
                continue
            seen.add(key)
            current = Hunk(file=current_file, header=line)
            hunks.append(current)
        elif current is None or line.startswith(("---", "+++")):
            continue
        elif line.startswith("-"):
            current.removed.append(line[1:])
        elif line.startswith("+"):
            current.added.append(line[1:])
        else:
            current.context.append(line[1:] if line.startswith(" ") else line)

    return hunks


def diff_cf(hunk: Hunk) -> Optional[str]:
    """Return a human-readable description of the CF change, or None."""
    before = "\n".join(hunk.context + hunk.removed)
    after  = "\n".join(hunk.context + hunk.added)
    cf_b = cf_structure(before)
    cf_a = cf_structure(after)
    if cf_b == cf_a:
        return None

    removed = [(t, d) for (t, d) in cf_b if (t, d) not in cf_a]
    added   = [(t, d) for (t, d) in cf_a  if (t, d) not in cf_b]
    depth_changes = [
        f"{t}: depth {d_b}→{d_a}"
        for (t, d_b), (t2, d_a) in zip(cf_b, cf_a)
        if t == t2 and d_b != d_a
    ]

    parts = []
    if removed:       parts.append(f"removed {[t for t, _ in removed]}")
    if added:         parts.append(f"added {[t for t, _ in added]}")
    if depth_changes: parts.append(f"re-nested: {depth_changes}")
    return "; ".join(parts) if parts else f"reordered: {cf_b} → {cf_a}"


def main():
    diff = sys.stdin.read()
    hunks = parse_diff(diff)
    found = False

    for hunk in hunks:
        if not hunk.removed and not hunk.added:
            continue
        desc = diff_cf(hunk)
        if desc:
            found = True
            print(f"\n⚠  {hunk.file}")
            print(f"   {hunk.header.strip()}")
            print(f"   {desc}")

    if not found:
        print("No control-flow changes detected.")

    sys.exit(1 if found else 0)


if __name__ == "__main__":
    main()
