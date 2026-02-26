import { graphql } from "@octokit/graphql";
import { printError } from "../utils/errors";
import type { GraphQLRateLimit } from "./types";

export type GraphQLClient = typeof graphql;

export function createGraphQLClient(): GraphQLClient {
	const token = process.env.GITHUB_TOKEN;

	if (!token) {
		printError(
			"GITHUB_TOKEN environment variable is required.",
			undefined,
			"Set it in .env file or export it: export GITHUB_TOKEN=ghp_xxx",
		);
		process.exit(1);
	}

	return graphql.defaults({
		headers: {
			authorization: `token ${token}`,
		},
	});
}

// Check current rate limit status
export async function checkRateLimit(client: GraphQLClient): Promise<GraphQLRateLimit> {
	const result = await client<{ rateLimit: GraphQLRateLimit }>(
		`{ rateLimit { remaining resetAt } }`,
	);
	return result.rateLimit;
}

// Wait until rate limit resets
export async function waitForRateLimitReset(resetAt: string): Promise<void> {
	const resetTime = new Date(resetAt).getTime();
	const waitMs = resetTime - Date.now() + 1000; // +1s buffer
	if (waitMs > 0) {
		console.log(`Rate limit exhausted. Waiting ${Math.ceil(waitMs / 1000)}s until reset...`);
		await new Promise((resolve) => setTimeout(resolve, waitMs));
	}
}

// Validate org access via GraphQL
export async function validateOrgAccess(client: GraphQLClient, org: string): Promise<boolean> {
	try {
		await client<{ organization: { login: string } }>(
			`query($org: String!) { organization(login: $org) { login } }`,
			{ org },
		);
		return true;
	} catch (error: unknown) {
		if (error instanceof Error) {
			if (error.message.includes("Could not resolve to an Organization")) {
				printError(`Organization '${org}' not found or not accessible.`);
				return false;
			}
			if (error.message.includes("Bad credentials") || error.message.includes("401")) {
				printError("Invalid GitHub token.", undefined, "Check your GITHUB_TOKEN.");
				return false;
			}
		}
		throw error;
	}
}
