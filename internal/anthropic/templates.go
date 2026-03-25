package anthropic

import "fmt"

// buildPromptGenerationPrompt builds the prompt for generating a PR review agent prompt.
func buildPromptGenerationPrompt(analysisReport string, includeAttribution bool) string {
	attributionInstruction := "Do not include reviewer attribution - present rules as team-wide standards."
	if includeAttribution {
		attributionInstruction = "Include which reviewers emphasize each rule/pattern (e.g., 'emphasized by: ryan-circleci, Frozenfire92')."
	}

	return fmt.Sprintf(`You are an expert at creating AI agent system prompts. Your task is to transform a code review analysis report into a PR review agent prompt.

# Input: Analysis Report

The following is an analysis of code review patterns from senior engineers. It contains:
- Per-reviewer analysis with key practices and examples
- Cross-cutting themes that appear across multiple reviewers
- Recommendations for automation, documentation, and training

<analysis_report>
%s
</analysis_report>

# Task

Generate a comprehensive PR review agent prompt that will guide an AI to enforce these patterns and practices when reviewing pull requests.

# Requirements

1. **Role Definition**: Start with a clear role definition for the PR review agent

2. **Core Principles**: Extract the 4-6 most important cross-cutting themes as core principles. Each should be:
   - A clear, actionable statement
   - Focused on the "why" not just the "what"

3. **Review Rules**: Organize rules into logical categories (Testing, Design System, Code Organization, etc.). Each rule should:
   - Be concise and actionable (checklist format)
   - Be specific enough to enforce consistently

4. **Code Examples**: Include concrete code examples showing:
   - What to avoid (bad pattern)
   - What to prefer (good pattern)
   - Use collapsible <details> blocks to keep the prompt scannable
   - Include the most instructive examples from the analysis

5. **Response Format**: Include instructions for how the agent should format its review comments. The format should focus ONLY on issues found - no praise sections.

6. **No Praise**: The agent should focus ONLY on identifying issues. The generated prompt must NOT include:
   - Instructions to compliment or praise the PR author
   - "Praise" or "What's done well" sections
   - Acknowledgment of good patterns or positive reinforcement
   Focus exclusively on what needs to be fixed or improved.

7. **Inline Code Suggestions**: For simple issues with straightforward fixes (1-2 lines of code), the agent should include concrete code suggestions using GitHub's suggestion format:
   `+"`"+`suggestion
   // corrected code here
   `+"`"+`
   Only provide suggestions for clear, mechanical fixes - not for architectural decisions or complex refactors.

8. %s

# Output Format

Output ONLY the markdown content for the PR review agent prompt. Do not include any preamble or explanation - just the prompt itself.

The prompt should be:
- Well-structured with clear headers
- Comprehensive but not overwhelming (aim for ~100-150 lines)
- Immediately usable in GitHub Actions, Claude Code hooks, or other AI review tools`, analysisReport, attributionInstruction)
}
