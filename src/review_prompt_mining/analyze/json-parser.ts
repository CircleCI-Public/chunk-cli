import type { PRWithReviews, ReviewCommentDetail } from "../types";

// Matches the details.json structure from top-reviewers command
export interface DetailsJSONOutput {
	metadata: {
		organization: string;
		since: string;
		analyzedAt: string;
		totalReposAnalyzed: number;
		totalComments: number;
	};
	comments: ReviewCommentDetail[];
}

// Unified comment interface used by analyze pipeline
export interface ReviewCommentWithContext {
	reviewer: string;
	body: string;
	diffHunk: string;
	createdAt: string;
	repo: string;
	pr?: {
		// Optional for backward compatibility with old details.json files
		number: number;
		title: string;
		author: string;
		url: string;
		state: string;
	};
}

export interface ReviewerGroup {
	reviewer: string;
	comments: ReviewCommentWithContext[];
	totalComments: number;
}

// Result from parsing either input format
export interface ParsedInput {
	comments: ReviewCommentWithContext[];
	metadata: {
		organization?: string;
		since?: string;
		analyzedAt?: string;
		totalComments: number;
	};
}

/**
 * Parse input JSON with auto-detection of format.
 * Supports both:
 * - reviews.json from mine command (has 'reviews' array)
 * - details.json from top-reviewers command (has 'comments' array)
 */
export async function parseInputJSON(filePath: string): Promise<ParsedInput> {
	const file = Bun.file(filePath);

	if (!(await file.exists())) {
		throw new Error(`File not found: ${filePath}`);
	}

	try {
		const data = await file.json();

		// Auto-detect format based on structure
		if (data.comments && Array.isArray(data.comments)) {
			// details.json from top-reviewers
			const comments = toCommentsWithContext(data.comments);
			return {
				comments,
				metadata: {
					organization: data.metadata?.organization,
					since: data.metadata?.since,
					analyzedAt: data.metadata?.analyzedAt,
					totalComments: comments.length,
				},
			};
		} else if (data.reviews && Array.isArray(data.reviews)) {
			// reviews.json from mine command
			const comments = flattenMineOutput(data.reviews);
			return {
				comments,
				metadata: {
					organization: data.extraction_metadata?.organization,
					since: data.extraction_metadata?.time_range,
					analyzedAt: data.extraction_metadata?.extracted_at,
					totalComments: comments.length,
				},
			};
		} else {
			throw new Error(
				"Invalid JSON format: expected 'comments' array (from top-reviewers) or 'reviews' array (from mine)",
			);
		}
	} catch (error) {
		if (error instanceof SyntaxError) {
			throw new Error(`Invalid JSON file: ${error.message}`);
		}
		throw error;
	}
}

/**
 * Convert ReviewCommentDetail array to ReviewCommentWithContext array
 * (For details.json from top-reviewers)
 * Supports both new format (nested pr object) and legacy format (repo string)
 */
function toCommentsWithContext(comments: ReviewCommentDetail[]): ReviewCommentWithContext[] {
	return comments.map((comment) => {
		// New format: has nested `pr` object with metadata
		if (comment.pr && typeof comment.pr === "object") {
			return {
				reviewer: comment.reviewer,
				body: comment.body,
				diffHunk: comment.diffHunk,
				createdAt: comment.createdAt,
				repo: comment.pr.repo,
				pr: {
					number: comment.pr.number,
					title: comment.pr.title,
					author: comment.pr.author,
					url: comment.pr.url,
					state: comment.pr.state,
				},
			};
		}
		// Legacy format: `repo` is a string field directly on comment (old details.json files)
		return {
			reviewer: comment.reviewer,
			body: comment.body,
			diffHunk: comment.diffHunk,
			createdAt: comment.createdAt,
			repo: (comment as unknown as { repo: string }).repo,
		};
	});
}

/**
 * Flatten PRWithReviews into individual comments with context
 * (For reviews.json from mine command)
 */
function flattenMineOutput(prs: PRWithReviews[]): ReviewCommentWithContext[] {
	const comments: ReviewCommentWithContext[] = [];

	for (const prData of prs) {
		// Handle both camelCase and snake_case from JSON
		const reviewerActivity =
			prData.reviewerActivity ||
			(prData as unknown as { reviewer_activity: typeof prData.reviewerActivity })
				.reviewer_activity ||
			[];

		for (const comment of reviewerActivity) {
			comments.push({
				reviewer: comment.reviewer,
				body: comment.body,
				diffHunk: comment.diffHunk || "",
				createdAt: comment.createdAt,
				repo: prData.pr.repo,
				pr: {
					number: prData.pr.number,
					title: prData.pr.title,
					author: prData.pr.author,
					url: prData.pr.url,
					state: prData.pr.state,
				},
			});
		}
	}

	return comments;
}

/**
 * Group comments by reviewer
 */
export function groupByReviewer(comments: ReviewCommentWithContext[]): ReviewerGroup[] {
	const groups = new Map<string, ReviewCommentWithContext[]>();

	for (const comment of comments) {
		const existing = groups.get(comment.reviewer) || [];
		existing.push(comment);
		groups.set(comment.reviewer, existing);
	}

	return Array.from(groups.entries()).map(([reviewer, comments]) => ({
		reviewer,
		comments,
		totalComments: comments.length,
	}));
}

/**
 * Limit comments per reviewer to most recent N comments
 */
export function limitCommentsPerReviewer(
	groups: ReviewerGroup[],
	maxComments: number,
): ReviewerGroup[] {
	return groups.map((group) => {
		if (group.comments.length <= maxComments) return group;
		// Take most recent N comments (sorted by createdAt desc)
		const sorted = [...group.comments].sort(
			(a, b) => new Date(b.createdAt).getTime() - new Date(a.createdAt).getTime(),
		);
		const limited = sorted.slice(0, maxComments);
		return {
			reviewer: group.reviewer,
			comments: limited,
			totalComments: limited.length,
		};
	});
}
