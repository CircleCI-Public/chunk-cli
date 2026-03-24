package anthropic

import "fmt"

// buildAnalysisPrompt builds the prompt for analyzing review patterns.
func buildAnalysisPrompt(reviewerData string, totalComments, reviewerCount int, reviewerNames string) string {
	return fmt.Sprintf(`You are analyzing code review feedback from senior engineers at CircleCI.

# Context
You have %d review comments from %d reviewer(s) across multiple repositories. Your goal is to identify:
1. What patterns and practices each reviewer emphasizes
2. What key principles they're trying to teach
3. Recurring themes across their feedback

# Data
%s

# Instructions
Analyze the review comments and produce a structured report with these sections:

## 1. Per-Reviewer Analysis
For each reviewer (%s):

### Key Practices
Identify 3-7 patterns in their feedback. For each pattern:
- **Name**: Short, descriptive title
- **Description**: What principle/practice they're emphasizing
- **Examples**: 2-3 concrete examples with code context and quotes

## 2. Cross-Cutting Themes
Identify 2-4 themes that appear across multiple reviewers or are especially important

## 3. Recommendations
Based on the patterns, what could be:
- Automated (linters, CI checks)
- Documented (style guides, architectural docs)
- Taught (onboarding, examples)

# Output Format
Use clear markdown with headers, bullet points, and code snippets where relevant.
Keep it concise but specific - use actual quotes from the comments.`, totalComments, reviewerCount, reviewerData, reviewerNames)
}

// buildPromptGenerationPrompt builds the prompt for generating a PR review agent prompt.
func buildPromptGenerationPrompt(analysisReport string, includeAttribution bool) string {
	attributionInstruction := "Do not include reviewer attribution - present rules as team-wide standards."
	if includeAttribution {
		attributionInstruction = "Include which reviewers emphasize each rule/pattern."
	}

	return fmt.Sprintf(`You are an expert at creating AI agent system prompts. Your task is to transform a code review analysis report into a PR review agent prompt.

# Input: Analysis Report

<analysis_report>
%s
</analysis_report>

# Task

Generate a comprehensive PR review agent prompt that will guide an AI to enforce these patterns and practices when reviewing pull requests.

# Requirements

1. **Role Definition**: Start with a clear role definition for the PR review agent
2. **Core Principles**: Extract the 4-6 most important cross-cutting themes as core principles
3. **Review Rules**: Organize rules into logical categories
4. **Code Examples**: Include concrete code examples
5. **Response Format**: Include instructions for how the agent should format its review comments
6. **No Praise**: Focus ONLY on identifying issues
7. **Inline Code Suggestions**: For simple issues, include concrete code suggestions
8. %s

# Output Format

Output ONLY the markdown content for the PR review agent prompt. Do not include any preamble or explanation - just the prompt itself.`, analysisReport, attributionInstruction)
}
