import type { GraphQLClient } from "../graphql-client";
import { waitForRateLimitReset } from "../graphql-client";
import type { GraphQLRateLimit, OrgReposResponse } from "../types";

const REPOS_QUERY = `
  query OrgRepos($org: String!, $cursor: String) {
    organization(login: $org) {
      repositories(first: 100, after: $cursor, isArchived: false) {
        pageInfo { hasNextPage endCursor }
        nodes { name }
      }
    }
    rateLimit { remaining resetAt }
  }
`;

export interface RepoIteratorOptions {
	org: string;
	filterRepos?: string[];
	onProgress?: (fetched: number) => void;
}

// Fetch all repo names from an organization
export async function fetchOrgRepos(
	client: GraphQLClient,
	options: RepoIteratorOptions,
): Promise<string[]> {
	const { org, filterRepos, onProgress } = options;

	// If specific repos are provided, validate and return them
	if (filterRepos && filterRepos.length > 0) {
		return filterRepos;
	}

	// Otherwise fetch all repos from org
	const repos: string[] = [];
	let cursor: string | null = null;
	let hasNextPage = true;
	let rateLimit: GraphQLRateLimit | null = null;

	while (hasNextPage) {
		const result: OrgReposResponse = await client<OrgReposResponse>(REPOS_QUERY, {
			org,
			cursor,
		});

		rateLimit = result.rateLimit;

		const repoData = result.organization.repositories;

		for (const node of repoData.nodes) {
			repos.push(node.name);
		}

		onProgress?.(repos.length);

		hasNextPage = repoData.pageInfo.hasNextPage;
		cursor = repoData.pageInfo.endCursor;

		// Check rate limit
		if (rateLimit.remaining < 500 && hasNextPage) {
			await waitForRateLimitReset(rateLimit.resetAt);
		}
	}

	return repos;
}
