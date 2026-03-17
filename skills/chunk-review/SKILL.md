---
name: chunk-review
description: Use when asked to "review recent changes", "chunk review", "review my diff", "review this PR", "review PR #123", "review PR <url>", "review my changes", or asks for a code review using the team's review prompt. Supports GitHub PRs via `gh pr diff` and local diffs. Applies team-specific review standards from the .chunk/review-prompt.md file.
version: 3.0.0
---

# Chunk Review Skill

Use the team's generated review prompt to review recent code changes. Runs the actual review in an isolated subagent so prior conversation context does not influence the findings.

## Steps

1. **Determine the diff scope**: Use the following priority order to decide what to review:
   - If the user provided a PR number, PR URL, or said "this PR" — use GitHub CLI:
     - Run `gh pr view <number-or-url> --json number,title,body,baseRefName,headRefName,headRepositoryOwner,headRepository` to get PR metadata
     - Run `gh pr diff <number-or-url>` to get the full diff
     - If no PR number/URL was given but you are on a branch with an open PR, run `gh pr view --json number,title,body,baseRefName,headRefName,headRepositoryOwner,headRepository` to auto-detect it, then fetch the diff the same way
   - If the user specified a commit range, branch, or file list — use that
   - If there are staged changes (`git diff --cached`) — review those
   - Otherwise, review uncommitted changes against the main branch (`git diff main...HEAD` or `git diff origin/main...HEAD`)

2. **Get the diff**: Obtain the changes using the method selected above. If the diff is empty, tell the user there is nothing to review. Store the full diff text — you will pass it inline to the subagent. For PR reviews also store the PR title and body.

3. **Load the review prompt**: The source depends on the review type:
   - **PR review**: fetch `.chunk/review-prompt.md` from the PR's head branch using:
     ```
     gh api repos/{headRepositoryOwner}/{headRepository}/contents/.chunk/review-prompt.md?ref={headRefName} --jq '.content' | base64 -d
     ```
     Substitute `{headRepositoryOwner}`, `{headRepository}`, and `{headRefName}` from the PR metadata fetched in step 1. If the file does not exist on that branch, fall back to the local `.chunk/review-prompt.md`.
   - **Local review**: read `.chunk/review-prompt.md` from the root of the current project.
   - If no review prompt is found in either location, tell the user and suggest they run `chunk build-prompt` to generate one. Do not proceed without it.
   - Store the full file contents — you will pass them inline to the subagent.

4. **Delegate to a subagent**: Launch a general-purpose subagent with a self-contained prompt built from the template below. Substitute `{{REVIEW_PROMPT}}`, `{{PR_CONTEXT}}`, and `{{DIFF}}` with the actual content collected in steps 1–3. The subagent has no access to your conversation history — everything it needs must be in the prompt.

   For local reviews, omit the PR context section entirely.

   ---
   **Subagent prompt template:**

   ```
   You are performing a code review. Apply the review standards below to the diff using a two-pass approach. Ignore anything outside this prompt — do not use any prior context.

   ## Review Standards

   {{REVIEW_PROMPT}}

   ## PR Context (if applicable)

   {{PR_CONTEXT}}

   ## Diff

   {{DIFF}}

   ## Instructions

   **Pass 1 — Comprehensive Review** (internal, do NOT include in output):
   Apply the standards above to the diff. Do not invent criteria beyond what is in the review standards. For each potential issue, internally record:
   - File path and line number
   - Category (from the review standards)
   - Description of the concern
   - Relevant code snippet

   **Pass 2 — Validate and Prioritize**:
   Re-examine every finding from Pass 1. For each finding:
   - Attempt to construct a **concrete failure scenario** (bug triggered, error thrown, security hole exploited, maintenance burden realized). If you cannot construct one, discard the finding.
   - Rate impact: **Critical** (breaks correctness, security vulnerability, data loss) / **High** (likely bug, significant performance issue, clear maintainability problem) / **Medium** (style or convention issue with real consequences) / **Low** (nitpick, subjective preference)
   - Estimate confidence (how certain you are this is a real issue). Discard anything below ~80% confidence.
   - **Keep**: all Critical and High findings. At most **1 Medium per changed file**. Discard all Low findings.
   - **Cap at 10 comments total**. If more than 10 survive filtering, keep the 10 highest-impact ones.

   **Output the final filtered findings using this format:**

   ## Review Summary

   **Files reviewed**: <count>
   **Issues identified**: <count after filtering>

   ## Findings

   ### 1. <short title>
   **`<file>:<line>`** | **<Impact>**

   <Explanation — framed as a question unless 100% certain the issue is critical. Reference the specific code and explain the concrete failure scenario.>

   <Optional: suggested fix or alternative approach>

   ---

   (repeat for each finding)

   If no findings survive filtering, output:

   ## Review Summary

   **Files reviewed**: <count>
   **Issues identified**: 0

   No significant issues identified. The changes look good against the team's review standards.
   ```

   ---

5. **Return the result**: Output the subagent's response directly to the user without modification.

## Framing Guidelines (for the subagent prompt)

- **Frame findings as questions** unless you are 100% certain the issue is critical.
- Always reference **file and line number** unless the issue is truly cross-cutting.
- Keep explanations concise. One short paragraph per finding, plus an optional suggestion.
- Do not pad the review with praise or filler. If there are no issues, say so clearly and stop.

## Notes

- The `.chunk/review-prompt.md` file is generated by `chunk build-prompt` and encodes the team's actual review patterns mined from real PR comments. Treat it as the authoritative source of review standards.
- If `.chunk/review-prompt.md` is missing, do not fall back to generic review criteria — prompt the user to generate it first.
- Keep review feedback grounded in the diff. Do not comment on code that was not changed.
- The subagent must receive all context inline — it has no access to the filesystem or conversation history.
