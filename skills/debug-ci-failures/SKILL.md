---
name: debug-ci-failures
description: >-
  Debug CircleCI build failures, analyze test results, and identify flaky tests.
  Use when the user says "debug CI", "why is CI failing", "fix CI failures",
  "find flaky tests", "what broke in CI", "check CircleCI", or asks about
  failing pipelines or build errors.
version: 1.0.0
allowed-tools:
  - ToolSearch
  - mcp__circleci-mcp-server__get_latest_pipeline_status
  - mcp__circleci-mcp-server__get_build_failure_logs
  - mcp__circleci-mcp-server__find_flaky_tests
  - mcp__circleci-mcp-server__get_job_test_results
  - mcp__circleci-mcp-server__list_followed_projects
  - mcp__circleci-mcp-server__rerun_workflow
  - Bash(git remote get-url origin)
  - Bash(git branch --show-current)
  - Read
  - Grep
  - Glob
---

# Debug CircleCI CI Failures

You are helping the user debug CircleCI build failures. Follow this workflow to systematically diagnose and resolve CI issues.

## Prerequisites

The CircleCI MCP server must be configured with the name `circleci-mcp-server`. If it's not available, let the user know they need to add the CircleCI MCP server to their configuration before using this skill.

## Step 0: Load CircleCI MCP tools

The CircleCI MCP tools are deferred and must be loaded before use. Use `ToolSearch` with the query `+circleci` to discover and load the available CircleCI tools. Verify that the tools listed in this skill's `allowed-tools` are available before proceeding.

## Step 1: Identify the project

Determine which CircleCI project to investigate. Use one of these approaches in order of preference:

1. **Local project context:** If the user is in a git repository, detect the project from the git remote URL and current branch. Read the git remote with `git remote get-url origin` and the branch with `git branch --show-current`.
2. **User-provided URL:** If the user provides a CircleCI URL (pipeline, workflow, or job URL), use it directly.
3. **Project listing:** Use `mcp__circleci-mcp-server__list_followed_projects` to show the user their projects and let them pick one.

## Step 2: Check pipeline status

Use `mcp__circleci-mcp-server__get_latest_pipeline_status` to quickly triage the current state of the pipeline:

- **Passing:** Let the user know the pipeline is green. Ask if they want to investigate a specific older failure.
- **Running:** Let the user know the pipeline is still in progress. Offer to check back or investigate a previous run.
- **Failing:** Note which workflows and jobs failed, then proceed to Step 3 to fetch full logs.

This avoids unnecessary log fetching when there is nothing to debug.

## Step 3: Fetch failure logs

Use `mcp__circleci-mcp-server__get_build_failure_logs` to retrieve the failure logs. Pass the appropriate parameters based on how the project was identified:

- For local context: pass `workspaceRoot`, `gitRemoteURL`, and `branch`
- For a URL: pass the `url` parameter
- For a project slug: pass `projectSlug` and `branch`

## Step 4: Check for flaky tests

Use `mcp__circleci-mcp-server__find_flaky_tests` to check if any tests in the project are known to be flaky. This helps distinguish between genuine failures and intermittent issues.

If flaky tests are found, clearly flag which failures in the logs might be caused by flaky tests.

## Step 5: Get detailed test results

If the failure involves test failures, use `mcp__circleci-mcp-server__get_job_test_results` to get detailed test metadata including:
- Which specific tests failed
- Error messages and stack traces
- Test durations

Always use `filterByTestsResult: 'failure'` when investigating failures so the tool returns only failed tests instead of the full test suite.

## Step 6: Analyze and diagnose

Analyze the tool output directly in your response. Do not write scripts or
create temporary files to process the results.

If the output includes an `<MCPTruncationWarning>`, the data was too large to
return in full. Work through these fallbacks in order:

1. **Narrow to a specific job:** Re-call the tool with a job-level URL (`.../jobs/123`) instead of a pipeline or branch-level URL.
2. **Filter to failures only:** Use `filterByTestsResult: 'failure'` with `get_job_test_results` to return only failed tests.
3. **Ask the user:** If narrowing still truncates, ask the user which job or test they want to focus on.

Based on the collected information, provide:

1. **Root cause analysis:** What went wrong and why
2. **Flaky test identification:** Which failures (if any) are likely due to flaky tests rather than real issues
3. **Fix suggestions:** Concrete steps the user can take to fix the failures
4. **Code references:** If the failures point to specific files or lines, look at the relevant source code in the local workspace to provide targeted fix suggestions

## Step 7: Offer follow-up actions

After diagnosis, offer to:
- Look at the specific source code files that caused failures
- Help fix the failing tests or code
- Rerun the workflow if the failure appears to be flaky — use `mcp__circleci-mcp-server__rerun_workflow` with the `workflowId` from the Step 2 pipeline status output. Set `fromFailed: true` to rerun only from the failed job instead of rerunning the entire workflow.
