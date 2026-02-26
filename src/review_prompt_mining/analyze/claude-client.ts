import Anthropic from "@anthropic-ai/sdk";
import { printError } from "../../utils/errors";
import type { ReviewerGroup } from "./json-parser";

/**
 * Check if an error is a token limit error from the Anthropic API
 */
export function isTokenLimitError(error: unknown): boolean {
	if (error instanceof Anthropic.APIError) {
		return error.message.includes("prompt is too long");
	}
	return false;
}

export interface ClaudeAnalysisConfig {
	model?: string;
	maxTokens?: number;
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
 * Analyze review patterns using Claude AI
 */
export async function analyzeReviews(
	client: Anthropic,
	reviewerGroups: ReviewerGroup[],
	promptBuilder: (groups: ReviewerGroup[]) => string,
	config: ClaudeAnalysisConfig = {},
): Promise<string> {
	const model = config.model || process.env.CLAUDE_MODEL || "claude-sonnet-4-5-20250929";
	// Analysis needs more tokens than standard tasks
	const maxTokens =
		config.maxTokens || Math.max(Number(process.env.CLAUDE_MAX_TOKENS) || 8000, 16000);

	const prompt = promptBuilder(reviewerGroups);

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
