import type { GraphQLClient } from "../graphql-client";
import { waitForRateLimitReset } from "../graphql-client";
import type {
	GraphQLPRNode,
	GraphQLRateLimit,
	RepoPRsResponse,
	ReviewCommentDetail,
	UserActivity,
} from "../types";

// Reduced batch size to avoid GitHub API timeouts on large repos
const PR_REVIEWS_QUERY = `
  query RepoReviewActivity($org: String!, $repo: String!, $cursor: String) {
    repository(owner: $org, name: $repo) {
      pullRequests(first: 20, after: $cursor, orderBy: {field: UPDATED_AT, direction: DESC}) {
        pageInfo { hasNextPage endCursor }
        nodes {
          number
          title
          url
          state
          updatedAt
          author { login }
          reviews(first: 50) {
            nodes {
              author { login }
              state
              createdAt
            }
          }
          reviewThreads(first: 100) {
            nodes {
              comments(first: 100) {
                nodes {
                  author { login }
                  body
                  diffHunk
                  createdAt
                }
              }
            }
          }
        }
      }
    }
    rateLimit { remaining resetAt }
  }
`;

const MAX_RETRIES = 3;
const INITIAL_RETRY_DELAY_MS = 2000;

// Delay helper
function delay(ms: number): Promise<void> {
	return new Promise((resolve) => setTimeout(resolve, ms));
}

// Check if error message looks like an HTML response (GitHub 500/503 error)
function isHtmlErrorResponse(message: string): boolean {
	return message.includes("<!DOCTYPE") || message.includes("<html") || message.includes("Unicorn");
}

// Check if error is retryable (timeout or server error)
function isRetryableError(error: Error): boolean {
	const message = error.message;
	return (
		message.includes("couldn't respond") ||
		message.includes("timeout") ||
		message.includes("ETIMEDOUT") ||
		isHtmlErrorResponse(message)
	);
}

// Execute query with retry logic for timeouts and server errors
async function executeWithRetry<T>(
	client: GraphQLClient,
	query: string,
	variables: Record<string, unknown>,
): Promise<T> {
	let lastError: Error | null = null;

	for (let attempt = 0; attempt < MAX_RETRIES; attempt++) {
		try {
			return await client<T>(query, variables);
		} catch (error) {
			if (error instanceof Error && isRetryableError(error)) {
				lastError = error;
				const delayMs = INITIAL_RETRY_DELAY_MS * 2 ** attempt;
				const errorType = isHtmlErrorResponse(error.message)
					? "GitHub API error (500/503)"
					: "Timeout";
				console.warn(
					`\n  ${errorType} on attempt ${attempt + 1}/${MAX_RETRIES}, retrying in ${delayMs / 1000}s...`,
				);
				await delay(delayMs);
				continue;
			}
			throw error;
		}
	}

	// Provide cleaner error message for HTML responses
	if (lastError && isHtmlErrorResponse(lastError.message)) {
		throw new Error(
			"GitHub API returned server error (500/503) after multiple retries. " +
				"This is usually a temporary issue - please try again in a few minutes.",
		);
	}

	throw lastError || new Error("Max retries exceeded");
}

// Known bot usernames and patterns
const BOT_PATTERNS = [
	/\[bot\]$/, // GitHub Apps: dependabot[bot], renovate[bot]
	/-bot$/i, // Custom bots: propel-code-bot, some-bot
	/^circleci-app$/i, // CircleCI GitHub App
	/^wiz-inc-/i, // Wiz security scanner
	/^github-actions$/i, // GitHub Actions
	/^dependabot$/i, // Dependabot (non-app version)
	/^renovate$/i, // Renovate (non-app version)
	/^codecov$/i, // Codecov
	/^sonarcloud$/i, // SonarCloud
];

// Check if a user is a bot
function isBot(login: string): boolean {
	return BOT_PATTERNS.some((pattern) => pattern.test(login));
}

// Create a new empty UserActivity object
function createEmptyActivity(login: string): UserActivity {
	return {
		login,
		totalActivity: 0,
		reviewsGiven: 0,
		approvals: 0,
		changesRequested: 0,
		reviewComments: 0,
		reposActiveIn: new Set(),
	};
}

// Get or create activity for a user
function getOrCreateActivity(activityMap: Map<string, UserActivity>, login: string): UserActivity {
	let activity = activityMap.get(login);
	if (!activity) {
		activity = createEmptyActivity(login);
		activityMap.set(login, activity);
	}
	return activity;
}

// Process a single PR's reviews and comments
function processPR(
	pr: GraphQLPRNode,
	since: Date | undefined, // undefined means include all comments (no date filter)
	repoName: string,
	activityMap: Map<string, UserActivity>,
	commentDetails: ReviewCommentDetail[],
): void {
	// Get PR author to exclude self-reviews
	const prAuthor = pr.author?.login?.toLowerCase();

	// Process reviews (approvals, changes requested, comments)
	for (const review of pr.reviews.nodes) {
		if (!review.author?.login) continue;
		if (isBot(review.author.login)) continue;
		// Skip self-reviews (PR author reviewing their own PR)
		if (prAuthor && review.author.login.toLowerCase() === prAuthor) continue;

		// Only filter by date if since is provided
		if (since) {
			const reviewDate = new Date(review.createdAt);
			if (reviewDate < since) continue;
		}

		const activity = getOrCreateActivity(activityMap, review.author.login);
		activity.reposActiveIn.add(repoName);

		// Count by review state (approvals/changes don't count toward totalActivity)
		switch (review.state) {
			case "APPROVED":
				activity.approvals++;
				activity.reviewsGiven++;
				break;
			case "CHANGES_REQUESTED":
				activity.changesRequested++;
				activity.reviewsGiven++;
				break;
			case "COMMENTED":
				activity.reviewsGiven++;
				break;
			// DISMISSED and PENDING are not counted
		}
	}

	// Process review thread comments (line-level comments)
	for (const thread of pr.reviewThreads.nodes) {
		for (const comment of thread.comments.nodes) {
			if (!comment.author?.login) continue;
			if (isBot(comment.author.login)) continue;
			// Skip self-comments (PR author commenting on their own PR)
			if (prAuthor && comment.author.login.toLowerCase() === prAuthor) continue;

			// Only filter by date if since is provided
			if (since) {
				const commentDate = new Date(comment.createdAt);
				if (commentDate < since) continue;
			}

			const activity = getOrCreateActivity(activityMap, comment.author.login);
			activity.reposActiveIn.add(repoName);
			activity.reviewComments++;
			activity.totalActivity++;

			// Collect comment details with PR metadata
			commentDetails.push({
				reviewer: comment.author.login,
				body: comment.body,
				diffHunk: comment.diffHunk,
				createdAt: comment.createdAt,
				pr: {
					repo: repoName,
					number: pr.number,
					title: pr.title,
					author: pr.author?.login ?? "unknown",
					url: pr.url,
					state: pr.state,
				},
			});
		}
	}
}

export interface FetchReviewActivityOptions {
	org: string;
	repo: string;
	since?: Date; // Optional - not used when maxPRs is set
	maxPRs?: number; // Stop after N PRs (takes precedence over since)
	onProgress?: (prsProcessed: number) => void;
}

// Result type for fetchReviewActivity
export interface FetchReviewActivityResult {
	activity: Map<string, UserActivity>;
	details: ReviewCommentDetail[];
}

// Fetch review activity for a single repo
export async function fetchReviewActivity(
	client: GraphQLClient,
	options: FetchReviewActivityOptions,
): Promise<FetchReviewActivityResult> {
	const { org, repo, since, maxPRs, onProgress } = options;
	const activityMap = new Map<string, UserActivity>();
	const commentDetails: ReviewCommentDetail[] = [];

	let cursor: string | null = null;
	let hasNextPage = true;
	let prsProcessed = 0;
	let rateLimit: GraphQLRateLimit | null = null;

	while (hasNextPage) {
		const result: RepoPRsResponse = await executeWithRetry<RepoPRsResponse>(
			client,
			PR_REVIEWS_QUERY,
			{
				org,
				repo,
				cursor,
			},
		);

		rateLimit = result.rateLimit;

		// Repository might be null if access is denied
		if (!result.repository) {
			break;
		}

		const prData = result.repository.pullRequests;

		for (const pr of prData.nodes) {
			// Check if PR count limit reached (maxPRs mode)
			if (maxPRs && prsProcessed >= maxPRs) {
				hasNextPage = false;
				break;
			}

			// Check if PR is older than our date boundary (since mode, only when maxPRs not set)
			if (!maxPRs && since) {
				const prUpdatedAt = new Date(pr.updatedAt);
				if (prUpdatedAt < since) {
					// PRs are ordered by updatedAt DESC, so we can stop here
					hasNextPage = false;
					break;
				}
			}

			// When maxPRs is set, don't filter comments by date (pass undefined)
			// When since is set, filter comments by date
			processPR(pr, maxPRs ? undefined : since, repo, activityMap, commentDetails);
			prsProcessed++;
		}

		onProgress?.(prsProcessed);

		if (hasNextPage) {
			hasNextPage = prData.pageInfo.hasNextPage;
			cursor = prData.pageInfo.endCursor;
		}

		// Check rate limit
		if (rateLimit.remaining < 500 && hasNextPage) {
			await waitForRateLimitReset(rateLimit.resetAt);
		}
	}

	return { activity: activityMap, details: commentDetails };
}
