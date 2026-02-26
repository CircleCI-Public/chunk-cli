/**
 * Use Claude AI to generate PR review agent prompt from analysis report
 */

import Anthropic from "@anthropic-ai/sdk";
import { printError } from "../../utils/errors";

export interface PromptGeneratorConfig {
	model?: string;
	maxTokens?: number;
	includeReviewerAttribution?: boolean;
}

/**
 * Create Anthropic client with API key validation
 */
export function createClaudeClient(): Anthropic {
	const apiKey = process.env.ANTHROPIC_API_KEY;

	if (!apiKey) {
		printError(
			"ANTHROPIC_API_KEY environment variable is required.",
			undefined,
			"Get your API key from: https://console.anthropic.com/",
		);
		process.exit(1);
	}

	return new Anthropic({ apiKey });
}

/**
 * Generate a PR review agent prompt using Claude
 */
export async function generateReviewPrompt(
	client: Anthropic,
	analysisReportContent: string,
	config: PromptGeneratorConfig = {},
): Promise<string> {
	const model = config.model || process.env.CLAUDE_MODEL_HEAVY || "claude-opus-4-5-20251101";
	const maxTokens = config.maxTokens || Number(process.env.CLAUDE_MAX_TOKENS) || 8000;
	const includeAttribution = config.includeReviewerAttribution ?? false;

	const prompt = buildClaudePrompt(analysisReportContent, includeAttribution);

	try {
		const response = await client.messages.create({
			model,
			max_tokens: maxTokens,
			messages: [
				{
					role: "user",
					content: prompt,
				},
			],
		});

		// Extract text from response
		const textContent = response.content.find((block) => block.type === "text");
		if (!textContent || textContent.type !== "text") {
			throw new Error("No text content in Claude response");
		}

		return textContent.text;
	} catch (error) {
		if (error instanceof Anthropic.APIError) {
			if (error.status === 429) {
				printError("Rate limit reached.", undefined, "Please wait and try again.");
			} else if (error.status === 401) {
				printError("Invalid API key.", undefined, "Check ANTHROPIC_API_KEY.");
			} else {
				printError(`Anthropic API error (${error.status}): ${error.message}`);
			}
		}
		throw error;
	}
}

/**
 * Build the prompt to send to Claude for generating the review agent prompt
 */
function buildClaudePrompt(analysisReport: string, includeAttribution: boolean): string {
	const attributionInstruction = includeAttribution
		? "Include which reviewers emphasize each rule/pattern (e.g., 'emphasized by: ryan-circleci, Frozenfire92')."
		: "Do not include reviewer attribution - present rules as team-wide standards.";

	return `You are an expert at creating AI agent system prompts. Your task is to transform a code review analysis report into a PR review agent prompt.

# Input: Analysis Report

The following is an analysis of code review patterns from senior engineers. It contains:
- Per-reviewer analysis with key practices and examples
- Cross-cutting themes that appear across multiple reviewers
- Recommendations for automation, documentation, and training

<analysis_report>
${analysisReport}
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
   \`\`\`suggestion
   // corrected code here
   \`\`\`
   Only provide suggestions for clear, mechanical fixes - not for architectural decisions or complex refactors.

8. ${attributionInstruction}

# Output Format

Output ONLY the markdown content for the PR review agent prompt. Do not include any preamble or explanation - just the prompt itself.

The prompt should be:
- Well-structured with clear headers
- Comprehensive but not overwhelming (aim for ~100-150 lines)
- Immediately usable in GitHub Actions, Claude Code hooks, or other AI review tools`;
}
