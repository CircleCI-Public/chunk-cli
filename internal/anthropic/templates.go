package anthropic

import "fmt"

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
