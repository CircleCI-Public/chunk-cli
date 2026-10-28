---
name: chunk-review
description: Use when asked to "review recent changes", "chunk review", "review my diff", "review this PR", "review PR #123", "review PR <url>", "review my changes", or asks for a code review using the team's review prompts. Supports GitHub PRs via `gh pr diff` and local diffs. Applies the project's review prompts in .chunk/reviews/, one reviewer per prompt.
version: 4.0.0
---

# Chunk Review Skill

Use the project's review prompts in `.chunk/reviews/` to review recent code changes. Each prompt covers one area, such as testing or correctness, and gets its own isolated subagent, so prior conversation context does not influence the findings and one area does not crowd out another. These are the same prompts `chunk factory` and `chunk review` use.

## Steps

1. **Determine the diff scope**: Use the following priority order to decide what to review:
   - If the user provided a PR number, PR URL, or said "this PR" — use GitHub CLI:
     - Run `gh pr view <number-or-url> --json number,title,body,baseRefName,headRefName,headRepositoryOwner,headRepository` to get PR metadata
     - Run `gh pr diff <number-or-url>` to get the full diff
     - If no PR number/URL was given but you are on a branch with an open PR, run `gh pr view --json number,title,body,baseRefName,headRefName,headRepositoryOwner,headRepository` to auto-detect it, then fetch the diff the same way
   - If the user specified a commit range, branch, or file list — use that
   - If there are staged changes (`git diff --cached`) — review those
   - Otherwise, review uncommitted changes against the main branch (`git diff main...HEAD` or `git diff origin/main...HEAD`)

2. **Get the diff**: Obtain the changes using the method selected above. If the diff is empty, tell the user there is nothing to review. Store the full diff text — you will pass it inline to the subagents. For PR reviews also store the PR title and body.

3. **Load the review prompts**: Each `.md` or `.txt` file in `.chunk/reviews/` is one review prompt, named after its file without the extension. The source depends on the review type:
   - **PR review**: list the prompts on the PR's head branch, then fetch each one:
     ```
     gh api 'repos/{headRepositoryOwner}/{headRepository}/contents/.chunk/reviews?ref={headRefName}' --jq '.[] | select(.type == "file") | .name'
     gh api 'repos/{headRepositoryOwner}/{headRepository}/contents/.chunk/reviews/{name}?ref={headRefName}' --jq '.content' | base64 --decode
     ```
     From the PR metadata fetched in step 1, substitute `{headRepositoryOwner}` with `headRepositoryOwner.login`, `{headRepository}` with `headRepository.name`, and `{headRefName}` directly. If the directory does not exist on that branch, fall back to the local `.chunk/reviews/`.
   - **Local review**: read every `.md` and `.txt` file in `.chunk/reviews/` at the root of the current project.
   - If no review prompts are found in either location, tell the user and suggest they add one `.md` file per review area to `.chunk/reviews/`. Do not proceed without them.
   - Store each prompt's name and full contents — you will pass them inline to the subagents.

4. **Delegate to one subagent per prompt**: Launch a general-purpose subagent for each review prompt, all in parallel, each with a self-contained prompt built from the template below. Substitute `{{REVIEW_NAME}}`, `{{REVIEW_PROMPT}}`, `{{PR_CONTEXT}}`, and `{{DIFF}}` with the actual content collected in steps 1–3. The subagents have no access to your conversation history — everything they need must be in the prompt.

   For local reviews, omit the PR context section entirely.

   ---
   **Subagent prompt template:**

   ```
   You are performing the "{{REVIEW_NAME}}" code review. Apply the review prompt below to the diff using a two-pass approach. Ignore anything outside this prompt — do not use any prior context. You may read files in the repository to understand the code the diff touches, but do not edit anything; for a PR, the local checkout may differ from the PR's head.

   ## Review Prompt

   {{REVIEW_PROMPT}}

   ## PR Context (if applicable)

   {{PR_CONTEXT}}

   ## Diff

   {{DIFF}}

   ## Instructions

   **Pass 1 — Comprehensive Review** (internal, do NOT include in output):
   Apply the review prompt above to the diff. Do not invent criteria beyond what is in the review prompt. For each potential issue, internally record:
   - File path and line number
   - Category (from the review prompt)
   - Description of the concern
   - Relevant code snippet

   **Pass 2 — Validate and Prioritize**:
   Re-examine every finding from Pass 1. For each finding:
   - Attempt to construct a **concrete failure scenario** (bug triggered, error thrown, security hole exploited, maintenance burden realized). If you cannot construct one, discard the finding.
   - Rate severity as **High**, **Medium** or **Low** using the review prompt's own definitions of each. Where it gives none: High breaks correctness, security or data; Medium is a likely bug or a convention issue with real consequences; Low is a nitpick or subjective preference.
   - Estimate confidence (how certain you are this is a real issue). Discard anything below ~80% confidence.
   - **Keep**: all High and Medium findings. Discard all Low findings.
   - **Cap at 10 findings**. If more than 10 survive filtering, keep the 10 most severe.

   **Output the final filtered findings using this format:**

   ## Review Summary

   **Files reviewed**: <count>
   **Issues identified**: <count after filtering>

   ## Findings

   ### 1. <short title>
   **`<file>:<line>`** | **<Severity>**

   <Explanation — framed as a question unless 100% certain the issue is critical. Reference the specific code and explain the concrete failure scenario.>

   <Optional: suggested fix or alternative approach>

   ---

   (repeat for each finding)

   If no findings survive filtering, output:

   ## Review Summary

   **Files reviewed**: <count>
   **Issues identified**: 0

   No significant issues identified. The changes look good against this review prompt.
   ```

   ---

5. **Combine the results**: Merge the subagents' findings into one review in the same format, with a `**Reviews**:` line after `**Files reviewed**` naming the prompts that ran. Tag each finding with the review it came from, e.g. `**`<file>:<line>`** | **High** | testing`. When two reviews report the same issue, keep the clearer one. Order by severity, High first, and cap the combined review at 10 findings. Do not add findings of your own or soften the subagents' wording.

## Framing Guidelines (for the subagent prompt)

- **Frame findings as questions** unless you are 100% certain the issue is critical.
- Always reference **file and line number** unless the issue is truly cross-cutting.
- Keep explanations concise. One short paragraph per finding, plus an optional suggestion.
- Do not pad the review with praise or filler. If there are no issues, say so clearly and stop.

## Notes

- The prompts in `.chunk/reviews/` are the project's own. Treat them as the authoritative source of review standards.
- If `.chunk/reviews/` has no prompts, do not fall back to generic review criteria — prompt the user to write them first.
- Keep review feedback grounded in the diff. Do not comment on code that was not changed.
- Each subagent must receive the prompt, diff and PR context inline — it has no access to the conversation history.
