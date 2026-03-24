package fixtures

// AnalysisResponse is the canned Claude response for the analysis step.
const AnalysisResponse = `## Review Pattern Analysis

### Reviewer: reviewer-alice
- Focuses on code structure and readability
- Prefers early returns to reduce nesting
- Advocates for const over let

### Reviewer: reviewer-bob
- Focuses on error handling and edge cases
- Catches nil pointer issues

### Cross-cutting Themes
1. Error handling completeness
2. Code readability and structure
3. Immutability preferences`

// PromptResponse is the canned Claude response for the prompt generation step.
const PromptResponse = `# Code Review Prompt

You are a code reviewer. Apply these review standards:

## Core Principles
1. Always handle errors explicitly
2. Prefer early returns to reduce nesting
3. Use const for immutable values
4. Check for nil before dereferencing

## Review Rules
- Flag missing error handling
- Suggest early returns when nesting exceeds 2 levels
- Recommend const over let where applicable`
