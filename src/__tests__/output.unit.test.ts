/**
 * Unit Tests — Top-reviewers output helpers
 *
 * Tests pure functions: derivePRRankingsCSVPath, aggregatePRRankings.
 * (printTable and write* functions require console/Bun.write and are not tested here)
 */

import { describe, expect, it } from "bun:test";
import {
	aggregatePRRankings,
	derivePRRankingsCSVPath,
} from "../review_prompt_mining/top-reviewers/output";
import type { ReviewCommentDetail } from "../review_prompt_mining/types";

function makeDetail(
	reviewer: string,
	repo = "org/repo",
	prNumber = 1,
	prTitle = "Test PR",
	prAuthor = "author",
): ReviewCommentDetail {
	return {
		reviewer,
		body: `Review by ${reviewer}`,
		diffHunk: "@@",
		createdAt: "2024-01-01T00:00:00Z",
		pr: {
			repo,
			number: prNumber,
			title: prTitle,
			author: prAuthor,
			url: `https://github.com/${repo}/pull/${prNumber}`,
			state: "MERGED",
		},
	};
}

describe("derivePRRankingsCSVPath", () => {
	it("replaces .json extension with -pr-rankings.csv", () => {
		const result = derivePRRankingsCSVPath("/output/details.json");
		expect(result).toBe("/output/details-pr-rankings.csv");
	});

	it("appends -pr-rankings.csv when no .json extension", () => {
		const result = derivePRRankingsCSVPath("/output/details");
		expect(result).toBe("/output/details-pr-rankings.csv");
	});

	it("handles filename-only path (no directory)", () => {
		const result = derivePRRankingsCSVPath("output.json");
		expect(result).toBe("output-pr-rankings.csv");
	});

	it("handles path with multiple dots in filename", () => {
		const result = derivePRRankingsCSVPath("my.output.data.json");
		expect(result).toBe("my.output.data-pr-rankings.csv");
	});

	it("does not modify non-.json extension", () => {
		const result = derivePRRankingsCSVPath("output.txt");
		expect(result).toBe("output.txt-pr-rankings.csv");
	});
});

describe("aggregatePRRankings", () => {
	it("returns empty array for empty input", () => {
		expect(aggregatePRRankings([])).toEqual([]);
	});

	it("returns a single PR for a single comment", () => {
		const details = [makeDetail("alice", "org/repo", 1)];
		const result = aggregatePRRankings(details);

		expect(result).toHaveLength(1);
		expect(result[0]?.pr_number).toBe(1);
		expect(result[0]?.total_comments).toBe(1);
	});

	it("counts multiple comments on the same PR", () => {
		const details = [
			makeDetail("alice", "org/repo", 1),
			makeDetail("bob", "org/repo", 1),
			makeDetail("carol", "org/repo", 1),
		];
		const result = aggregatePRRankings(details);

		expect(result).toHaveLength(1);
		expect(result[0]?.total_comments).toBe(3);
	});

	it("counts unique reviewers on a PR", () => {
		const details = [
			makeDetail("alice", "org/repo", 1),
			makeDetail("alice", "org/repo", 1), // same reviewer twice
			makeDetail("bob", "org/repo", 1),
		];
		const result = aggregatePRRankings(details);

		expect(result[0]?.reviewer_count).toBe(2); // alice + bob
	});

	it("separates PRs from different repos with the same number", () => {
		const details = [makeDetail("alice", "org/repo-a", 1), makeDetail("bob", "org/repo-b", 1)];
		const result = aggregatePRRankings(details);

		expect(result).toHaveLength(2);
	});

	it("sorts by total_comments descending", () => {
		const details = [
			makeDetail("alice", "org/repo", 1),
			makeDetail("bob", "org/repo", 2),
			makeDetail("carol", "org/repo", 2),
			makeDetail("dave", "org/repo", 2),
		];
		const result = aggregatePRRankings(details);

		// PR #2 has 3 comments, PR #1 has 1 comment
		expect(result[0]?.pr_number).toBe(2);
		expect(result[1]?.pr_number).toBe(1);
	});

	it("assigns rank starting from 1", () => {
		const details = [makeDetail("alice", "org/repo", 1), makeDetail("bob", "org/repo", 2)];
		const result = aggregatePRRankings(details);

		const ranks = result.map((r) => r.rank).sort((a, b) => a - b);
		expect(ranks[0]).toBe(1);
		expect(ranks[1]).toBe(2);
	});

	it("includes repo, pr_number, pr_title, pr_author, pr_url, state", () => {
		const details = [makeDetail("alice", "myorg/myrepo", 42, "My PR Title", "myauthor")];
		const result = aggregatePRRankings(details);

		const row = result[0];
		expect(row?.repo).toBe("myorg/myrepo");
		expect(row?.pr_number).toBe(42);
		expect(row?.pr_title).toBe("My PR Title");
		expect(row?.pr_author).toBe("myauthor");
		expect(row?.pr_url).toBe("https://github.com/myorg/myrepo/pull/42");
		expect(row?.state).toBe("MERGED");
	});

	it("handles multiple repos correctly", () => {
		const details = [
			makeDetail("alice", "org/repo-a", 1),
			makeDetail("alice", "org/repo-a", 1),
			makeDetail("bob", "org/repo-b", 5),
		];
		const result = aggregatePRRankings(details);

		expect(result).toHaveLength(2);
		const repoA = result.find((r) => r.repo === "org/repo-a");
		const repoB = result.find((r) => r.repo === "org/repo-b");
		expect(repoA?.total_comments).toBe(2);
		expect(repoB?.total_comments).toBe(1);
	});
});
