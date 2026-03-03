/**
 * Unit Tests — Activity aggregator for top-reviewers
 */

import { describe, expect, it } from "bun:test";
import {
	aggregateActivity,
	aggregateDetails,
	topN,
} from "../review_prompt_mining/top-reviewers/aggregator";
import type { ReviewCommentDetail, UserActivity } from "../review_prompt_mining/types";

function makeActivity(
	login: string,
	overrides: Partial<Omit<UserActivity, "login" | "reposActiveIn">> & {
		repos?: string[];
	} = {},
): UserActivity {
	return {
		login,
		totalActivity: overrides.totalActivity ?? 10,
		reviewsGiven: overrides.reviewsGiven ?? 5,
		approvals: overrides.approvals ?? 3,
		changesRequested: overrides.changesRequested ?? 1,
		reviewComments: overrides.reviewComments ?? 1,
		reposActiveIn: new Set(overrides.repos ?? ["org/repo"]),
	};
}

function makeDetail(reviewer: string, repo = "org/repo", prNumber = 1): ReviewCommentDetail {
	return {
		reviewer,
		body: `Review by ${reviewer}`,
		diffHunk: "@@",
		createdAt: "2024-01-01T00:00:00Z",
		pr: {
			repo,
			number: prNumber,
			title: "Test PR",
			author: "author",
			url: `https://github.com/${repo}/pull/${prNumber}`,
			state: "MERGED",
		},
	};
}

describe("aggregateActivity", () => {
	it("returns empty array for no inputs", () => {
		expect(aggregateActivity([])).toEqual([]);
	});

	it("returns activities from a single map", () => {
		const map = new Map([["alice", makeActivity("alice")]]);
		const result = aggregateActivity([map]);

		expect(result).toHaveLength(1);
		expect(result[0]?.login).toBe("alice");
	});

	it("merges activities from multiple maps for the same login", () => {
		const map1 = new Map([
			[
				"alice",
				makeActivity("alice", {
					totalActivity: 10,
					reviewsGiven: 5,
					approvals: 3,
					changesRequested: 1,
					reviewComments: 1,
					repos: ["org/repo-a"],
				}),
			],
		]);
		const map2 = new Map([
			[
				"alice",
				makeActivity("alice", {
					totalActivity: 6,
					reviewsGiven: 3,
					approvals: 2,
					changesRequested: 0,
					reviewComments: 1,
					repos: ["org/repo-b"],
				}),
			],
		]);
		const result = aggregateActivity([map1, map2]);

		expect(result).toHaveLength(1);
		const alice = result[0];
		expect(alice?.totalActivity).toBe(16);
		expect(alice?.reviewsGiven).toBe(8);
		expect(alice?.approvals).toBe(5);
		expect(alice?.changesRequested).toBe(1);
		expect(alice?.reviewComments).toBe(2);
		expect(alice?.reposActiveIn.size).toBe(2);
		expect(alice?.reposActiveIn.has("org/repo-a")).toBe(true);
		expect(alice?.reposActiveIn.has("org/repo-b")).toBe(true);
	});

	it("keeps different logins separate", () => {
		const map1 = new Map([["alice", makeActivity("alice", { totalActivity: 10 })]]);
		const map2 = new Map([["bob", makeActivity("bob", { totalActivity: 5 })]]);
		const result = aggregateActivity([map1, map2]);

		expect(result).toHaveLength(2);
		const logins = result.map((a) => a.login);
		expect(logins).toContain("alice");
		expect(logins).toContain("bob");
	});

	it("sorts by totalActivity descending", () => {
		const map = new Map([
			["alice", makeActivity("alice", { totalActivity: 5 })],
			["bob", makeActivity("bob", { totalActivity: 15 })],
			["carol", makeActivity("carol", { totalActivity: 10 })],
		]);
		const result = aggregateActivity([map]);

		expect(result[0]?.login).toBe("bob");
		expect(result[1]?.login).toBe("carol");
		expect(result[2]?.login).toBe("alice");
	});

	it("does not mutate original maps", () => {
		const original = makeActivity("alice", { totalActivity: 10 });
		const map = new Map([["alice", original]]);
		aggregateActivity([map, map]);

		// Original should be untouched
		expect(original.totalActivity).toBe(10);
	});

	it("merges repos from overlapping maps into a single set", () => {
		const map1 = new Map([["alice", makeActivity("alice", { repos: ["org/a", "org/b"] })]]);
		const map2 = new Map([["alice", makeActivity("alice", { repos: ["org/b", "org/c"] })]]);
		const result = aggregateActivity([map1, map2]);

		expect(result[0]?.reposActiveIn.size).toBe(3);
	});
});

describe("topN", () => {
	it("returns empty array for empty input", () => {
		expect(topN([], 5)).toEqual([]);
	});

	it("returns all items when n >= length", () => {
		const activities = [makeActivity("alice"), makeActivity("bob")];
		expect(topN(activities, 10)).toHaveLength(2);
	});

	it("returns first n items", () => {
		const activities = [makeActivity("alice"), makeActivity("bob"), makeActivity("carol")];
		const result = topN(activities, 2);

		expect(result).toHaveLength(2);
		expect(result[0]?.login).toBe("alice");
		expect(result[1]?.login).toBe("bob");
	});

	it("returns empty array for n = 0", () => {
		const activities = [makeActivity("alice")];
		expect(topN(activities, 0)).toEqual([]);
	});

	it("returns exactly 1 item for n = 1", () => {
		const activities = [makeActivity("alice"), makeActivity("bob")];
		const result = topN(activities, 1);

		expect(result).toHaveLength(1);
		expect(result[0]?.login).toBe("alice");
	});
});

describe("aggregateDetails", () => {
	it("returns empty array for empty input", () => {
		expect(aggregateDetails([])).toEqual([]);
	});

	it("flattens a single array", () => {
		const details = [makeDetail("alice"), makeDetail("bob")];
		const result = aggregateDetails([details]);

		expect(result).toHaveLength(2);
	});

	it("flattens multiple arrays", () => {
		const details1 = [makeDetail("alice", "org/repo-a")];
		const details2 = [makeDetail("bob", "org/repo-b"), makeDetail("carol", "org/repo-b")];
		const result = aggregateDetails([details1, details2]);

		expect(result).toHaveLength(3);
	});

	it("handles empty sub-arrays", () => {
		const result = aggregateDetails([[], []]);
		expect(result).toEqual([]);
	});

	it("preserves comment order (first array items come first)", () => {
		const details1 = [makeDetail("alice")];
		const details2 = [makeDetail("bob")];
		const result = aggregateDetails([details1, details2]);

		expect(result[0]?.reviewer).toBe("alice");
		expect(result[1]?.reviewer).toBe("bob");
	});
});
