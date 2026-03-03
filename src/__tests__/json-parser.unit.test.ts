/**
 * Unit Tests — Review comment JSON parsing utilities
 *
 * Tests the pure functions: groupByReviewer, limitCommentsPerReviewer.
 * (parseInputJSON requires Bun.file so it is not tested here)
 */

import { describe, expect, it } from "bun:test";
import {
	groupByReviewer,
	limitCommentsPerReviewer,
	type ReviewCommentWithContext,
} from "../review_prompt_mining/analyze/json-parser";

function makeComment(
	reviewer: string,
	repo = "org/repo",
	createdAt = "2024-01-01T00:00:00Z",
): ReviewCommentWithContext {
	return {
		reviewer,
		body: `Comment by ${reviewer}`,
		diffHunk: "@@",
		createdAt,
		repo,
	};
}

describe("groupByReviewer", () => {
	it("returns empty array for empty input", () => {
		expect(groupByReviewer([])).toEqual([]);
	});

	it("groups a single comment correctly", () => {
		const comments = [makeComment("alice")];
		const groups = groupByReviewer(comments);

		expect(groups).toHaveLength(1);
		expect(groups[0]?.reviewer).toBe("alice");
		expect(groups[0]?.comments).toHaveLength(1);
		expect(groups[0]?.totalComments).toBe(1);
	});

	it("groups multiple comments by the same reviewer", () => {
		const comments = [makeComment("alice"), makeComment("alice"), makeComment("alice")];
		const groups = groupByReviewer(comments);

		expect(groups).toHaveLength(1);
		expect(groups[0]?.reviewer).toBe("alice");
		expect(groups[0]?.totalComments).toBe(3);
	});

	it("creates separate groups for different reviewers", () => {
		const comments = [makeComment("alice"), makeComment("bob"), makeComment("carol")];
		const groups = groupByReviewer(comments);

		expect(groups).toHaveLength(3);
		const reviewers = groups.map((g) => g.reviewer);
		expect(reviewers).toContain("alice");
		expect(reviewers).toContain("bob");
		expect(reviewers).toContain("carol");
	});

	it("interleaved comments are still grouped together per reviewer", () => {
		const comments = [
			makeComment("alice"),
			makeComment("bob"),
			makeComment("alice"),
			makeComment("bob"),
		];
		const groups = groupByReviewer(comments);

		expect(groups).toHaveLength(2);
		const aliceGroup = groups.find((g) => g.reviewer === "alice");
		const bobGroup = groups.find((g) => g.reviewer === "bob");
		expect(aliceGroup?.totalComments).toBe(2);
		expect(bobGroup?.totalComments).toBe(2);
	});

	it("totalComments matches comments.length", () => {
		const comments = [makeComment("alice"), makeComment("alice"), makeComment("bob")];
		const groups = groupByReviewer(comments);

		for (const group of groups) {
			expect(group.totalComments).toBe(group.comments.length);
		}
	});
});

describe("limitCommentsPerReviewer", () => {
	it("returns groups unchanged when comments <= maxComments", () => {
		const groups = groupByReviewer([makeComment("alice"), makeComment("alice")]);
		const limited = limitCommentsPerReviewer(groups, 5);

		expect(limited[0]?.comments).toHaveLength(2);
		expect(limited[0]?.totalComments).toBe(2);
	});

	it("limits group to maxComments when over the limit", () => {
		const comments = [
			makeComment("alice", "org/repo", "2024-01-01T00:00:00Z"),
			makeComment("alice", "org/repo", "2024-01-02T00:00:00Z"),
			makeComment("alice", "org/repo", "2024-01-03T00:00:00Z"),
			makeComment("alice", "org/repo", "2024-01-04T00:00:00Z"),
			makeComment("alice", "org/repo", "2024-01-05T00:00:00Z"),
		];
		const groups = groupByReviewer(comments);
		const limited = limitCommentsPerReviewer(groups, 3);

		expect(limited[0]?.comments).toHaveLength(3);
		expect(limited[0]?.totalComments).toBe(3);
	});

	it("keeps the most recent comments when limiting", () => {
		const comments = [
			makeComment("alice", "org/repo", "2024-01-01T00:00:00Z"),
			makeComment("alice", "org/repo", "2024-01-05T00:00:00Z"),
			makeComment("alice", "org/repo", "2024-01-03T00:00:00Z"),
		];
		const groups = groupByReviewer(comments);
		const limited = limitCommentsPerReviewer(groups, 2);

		const keptDates = limited[0]?.comments.map((c) => c.createdAt) ?? [];
		expect(keptDates).toContain("2024-01-05T00:00:00Z");
		expect(keptDates).toContain("2024-01-03T00:00:00Z");
		expect(keptDates).not.toContain("2024-01-01T00:00:00Z");
	});

	it("handles empty groups array", () => {
		expect(limitCommentsPerReviewer([], 10)).toEqual([]);
	});

	it("handles maxComments = 0 by returning empty comments", () => {
		const comments = [makeComment("alice"), makeComment("alice")];
		const groups = groupByReviewer(comments);
		const limited = limitCommentsPerReviewer(groups, 0);

		expect(limited[0]?.comments).toHaveLength(0);
		expect(limited[0]?.totalComments).toBe(0);
	});

	it("preserves reviewer name in limited group", () => {
		const comments = [makeComment("alice"), makeComment("alice"), makeComment("alice")];
		const groups = groupByReviewer(comments);
		const limited = limitCommentsPerReviewer(groups, 1);

		expect(limited[0]?.reviewer).toBe("alice");
	});

	it("leaves groups within limit untouched", () => {
		const comments = [
			makeComment("alice"),
			makeComment("bob"),
			makeComment("bob"),
			makeComment("bob"),
			makeComment("bob"),
		];
		const groups = groupByReviewer(comments);
		const limited = limitCommentsPerReviewer(groups, 2);

		const aliceGroup = limited.find((g) => g.reviewer === "alice");
		const bobGroup = limited.find((g) => g.reviewer === "bob");

		// alice has 1 comment which is <= 2, so unchanged
		expect(aliceGroup?.totalComments).toBe(1);
		// bob has 4 comments which is > 2, so limited to 2
		expect(bobGroup?.totalComments).toBe(2);
	});
});
