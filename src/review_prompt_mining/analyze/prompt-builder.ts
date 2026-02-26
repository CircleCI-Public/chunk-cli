import type { ReviewCommentWithContext, ReviewerGroup } from "./json-parser";

/**
 * Estimate token count for a given text.
 * Uses a conservative estimate of ~4 characters per token.
 * Actual tokenization varies, but this is a reasonable approximation for Claude.
 */
export function estimateTokenCount(text: string): number {
	return Math.ceil(text.length / 4);
}

/**
 * Build analysis prompt for Claude AI
 */
export function buildAnalysisPrompt(reviewerGroups: ReviewerGroup[]): string {
	const totalComments = reviewerGroups.reduce((sum, g) => sum + g.totalComments, 0);
	const reviewerNames = reviewerGroups.map((g) => g.reviewer).join(", ");

	return `You are analyzing code review feedback from senior engineers at CircleCI.

# Context
You have ${totalComments} review comments from ${reviewerGroups.length} reviewer(s) across multiple repositories. Your goal is to identify:
1. What patterns and practices each reviewer emphasizes
2. What key principles they're trying to teach
3. Recurring themes across their feedback

# Data
${formatReviewerData(reviewerGroups)}

# Instructions
Analyze the review comments and produce a structured report with these sections:

## 1. Per-Reviewer Analysis
For each reviewer (${reviewerNames}):

### Key Practices
Identify 3-7 patterns in their feedback. For each pattern:
- **Name**: Short, descriptive title
- **Description**: What principle/practice they're emphasizing
- **Examples**: 2-3 concrete examples with code context and quotes

Examples of patterns to look for:
- Observability/instrumentation preferences (like preferring specific o11y methods)
- Naming conventions (like "o11y" abbreviation, metric naming patterns)
- Code organization principles
- Testing approaches
- Performance considerations
- Architectural guidance
- Error handling patterns

### Notable Repos
Identify which repositories have particularly instructive feedback and why.

## 2. Cross-Cutting Themes
Identify 2-4 themes that appear across multiple reviewers or are especially important

## 3. Recommendations
Based on the patterns, what could be:
- Automated (linters, CI checks)
- Documented (style guides, architectural docs)
- Taught (onboarding, examples)

# Output Format
Use clear markdown with headers, bullet points, and code snippets where relevant.
Keep it concise but specific - use actual quotes from the comments.`;
}

/**
 * Format reviewer data for the prompt
 */
function formatReviewerData(groups: ReviewerGroup[]): string {
	let output = "";

	for (const group of groups) {
		output += `\n## ${group.reviewer} (${group.totalComments} comments)\n\n`;

		// Group comments by repo for better readability
		const repoGroups = groupCommentsByRepo(group.comments);

		for (const [repo, comments] of Object.entries(repoGroups)) {
			output += `### Repository: ${repo} (${comments.length} comments)\n\n`;

			for (const comment of comments) {
				output += formatComment(comment);
				output += "\n";
			}
			output += "\n";
		}
	}

	return output;
}

/**
 * Group comments by repository
 */
function groupCommentsByRepo(
	comments: ReviewCommentWithContext[],
): Record<string, ReviewCommentWithContext[]> {
	const groups: Record<string, ReviewCommentWithContext[]> = {};

	for (const comment of comments) {
		const key = comment.repo;
		if (!groups[key]) groups[key] = [];
		groups[key].push(comment);
	}

	return groups;
}

/**
 * Format a single comment
 */
function formatComment(comment: ReviewCommentWithContext): string {
	let formatted = `> ${comment.body}\n`;

	// Include PR context if available
	if (comment.pr) {
		formatted += `\n**PR**: [#${comment.pr.number}](${comment.pr.url}) - ${comment.pr.title} (by ${comment.pr.author})\n`;
	}

	// Include diff hunk if available (all comments from details.json are line-level)
	if (comment.diffHunk) {
		formatted += `\n**Code context:**\n\`\`\`\n${comment.diffHunk}\n\`\`\`\n`;
	}

	return formatted;
}
