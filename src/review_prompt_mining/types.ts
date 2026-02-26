// Combined PR + Comments for output
export interface PRWithReviews {
	pr: {
		repo: string;
		number: number;
		title: string;
		author: string;
		createdAt: string;
		state: string;
		url: string;
	};
	reviewerActivity: Array<{
		reviewer: string;
		body: string;
		diffHunk?: string;
		createdAt: string;
		[key: string]: unknown;
	}>;
}

// ============================================
// Top Reviewers Feature (GraphQL-based)
// ============================================

export interface UserActivity {
	login: string;
	totalActivity: number; // sum of all below
	reviewsGiven: number; // APPROVED + CHANGES_REQUESTED + COMMENTED
	approvals: number;
	changesRequested: number;
	reviewComments: number; // line-level comments in review threads
	reposActiveIn: Set<string>; // breadth indicator
}

// GraphQL response types
export interface GraphQLRateLimit {
	remaining: number;
	resetAt: string;
}

export type GraphQLReviewState =
	| "APPROVED"
	| "CHANGES_REQUESTED"
	| "COMMENTED"
	| "DISMISSED"
	| "PENDING";

export interface GraphQLReviewNode {
	author: { login: string } | null;
	state: GraphQLReviewState;
	createdAt: string;
}

export interface GraphQLReviewCommentNode {
	author: { login: string } | null;
	body: string;
	diffHunk: string;
	createdAt: string;
}

// PR state from GraphQL API
export type GraphQLPRState = "OPEN" | "CLOSED" | "MERGED";

// Comment detail for top-reviewers details JSON output (enriched with PR metadata)
export interface ReviewCommentDetail {
	reviewer: string;
	body: string;
	diffHunk: string;
	createdAt: string;
	pr: {
		repo: string;
		number: number;
		title: string;
		author: string;
		url: string;
		state: GraphQLPRState;
	};
}

// PR Rankings for CSV output (top-reviewers command)
export interface PRRankingRow {
	rank: number;
	repo: string;
	pr_number: number;
	pr_title: string;
	pr_author: string;
	pr_url: string;
	total_comments: number;
	reviewer_count: number;
	state: string;
}

export interface GraphQLPRNode {
	number: number;
	title: string;
	url: string;
	state: GraphQLPRState;
	updatedAt: string;
	author: { login: string } | null;
	reviews: {
		nodes: GraphQLReviewNode[];
	};
	reviewThreads: {
		nodes: Array<{
			comments: {
				nodes: GraphQLReviewCommentNode[];
			};
		}>;
	};
}

export interface GraphQLPageInfo {
	hasNextPage: boolean;
	endCursor: string | null;
}

export interface GraphQLRepoNode {
	name: string;
}

export interface OrgReposResponse {
	organization: {
		repositories: {
			pageInfo: GraphQLPageInfo;
			nodes: GraphQLRepoNode[];
		};
	};
	rateLimit: GraphQLRateLimit;
}

export interface RepoPRsResponse {
	repository: {
		pullRequests: {
			pageInfo: GraphQLPageInfo;
			nodes: GraphQLPRNode[];
		};
	} | null;
	rateLimit: GraphQLRateLimit;
}
