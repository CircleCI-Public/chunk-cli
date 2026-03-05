---
name: chunk-review-v3
description: This skill should be used when the user asks to "review recent changes", "chunk review", "review my diff", "review this PR", "review my changes", or asks for a code review using the team's review prompt. Applies team-specific review standards from the .chunk/review-prompt.md file. Focuses on high-signal findings only.
version: 3.0.0
---

# Chunk Review Skill

Review recent code changes using the team's review prompt. The goal is to surface only the findings that genuinely matter — the kind of thing that would make an experienced engineer block a PR. Everything else is noise.

## Why most AI reviews are noisy

AI reviewers tend to flag everything they can articulate — style nitpicks, naming preferences, theoretical edge cases. This makes real issues harder to spot. A review with 2 important findings is more useful than one with 8 findings where 6 are filler. Your job is to be the reviewer who only speaks up when it matters.

## Steps

1. **Load the review prompt**: Read `.chunk/review-prompt.md` from the project root. If missing, tell the user and suggest they run `chunk build-prompt` to generate one.

2. **Determine the diff scope** (priority order):
   - User-specified commit range, branch, or file list
   - Staged changes (`git diff --cached`)
   - Uncommitted changes against main (`git diff main...HEAD` combined with `git diff`)

3. **Get the diff**: Run `git diff` with the appropriate scope. If empty, tell the user and stop.

4. **Read full file context**: For every file touched in the diff, read the full file (or at minimum the surrounding ~100 lines around each changed section). You cannot judge whether something is a real problem from the diff alone — you need to see what the code is doing, what's imported, what the function signatures look like, how the surrounding code behaves. This step is not optional.

5. **Pass 1 — Identify candidates** (internal, do NOT show to the user):
   Apply the standards from `.chunk/review-prompt.md` to the diff. Do not invent criteria beyond what's in the prompt. For each potential issue, internally note:
   - File path and line number
   - Category (from the review prompt)
   - What the concern is
   - The relevant code

6. **Pass 2 — Challenge every finding** (internal, do NOT show to the user):
   For each candidate from Pass 1, actively try to **disprove it**:

   a. **Read the surrounding code carefully.** Does the context resolve the concern? Is there error handling upstream? Is the "missing" check actually done elsewhere? Does the type system already prevent this? Many apparent issues dissolve when you look at the full picture.

   b. **Construct a concrete failure scenario.** Not a theoretical "this could maybe cause issues" — an actual sequence of events: a specific input, a specific code path, a specific bad outcome. If you cannot construct one, drop the finding.

   c. **Ask: would a senior engineer flag this in a real review?** Someone who values their team's time, who knows that every comment costs attention. If the answer is "probably not, but technically...", drop it.

   d. **Classify what survives:**
      - **Critical**: Breaks correctness, security vulnerability, data loss, crash in production
      - **High**: Likely bug under realistic conditions, significant performance issue, clear violation of a team standard that exists for a good reason

      Drop everything that doesn't reach High. Style preferences, naming opinions, "consider using X instead of Y" — all dropped unless the review prompt specifically calls them out as team standards that matter.

   e. **Cap at 5 findings.** If more than 5 survive, keep the 5 most impactful. A focused review is a useful review.

7. **Produce the review**: Output only the survivors using the format below.

## Output Format

```
## Review Summary

**Scope**: <what was reviewed — branch, commit range, or "uncommitted changes">
**Files reviewed**: <count>
**Issues found**: <count>

## Findings

### 1. <short title>
**`<file>:<line>`** | **<Critical or High>**

<What's wrong, why it matters, and the concrete failure scenario. Be specific — name the function, the input, the consequence. Frame as a question if you're not 100% certain: "Could this throw if X?" rather than "This will throw.">

<Suggested fix if you have one. Keep it brief.>

---

(repeat for each finding)
```

If nothing survives filtering:

```
## Review Summary

**Scope**: <what was reviewed>
**Files reviewed**: <count>
**Issues found**: 0

No significant issues found. The changes look good.
```

## Ground Rules

- **Only comment on changed code.** Don't review code that wasn't modified in the diff.
- **The review prompt is your standard.** `.chunk/review-prompt.md` defines what this team cares about. Don't add your own opinions on top.
- **No filler.** No praise, no "looks great overall", no padding. If there's nothing to say, say nothing.
- **No low-confidence speculation.** If you're not reasonably sure something is a real issue, leave it out. Silence is better than noise.
- **Questions over assertions.** Unless you're certain something is a bug, frame it as a question. This respects the author's context — they may know something you don't.
- Pass 1 and Pass 2 are internal working steps. The user only sees the final output.
