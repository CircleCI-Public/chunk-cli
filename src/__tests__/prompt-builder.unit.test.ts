/**
 * Unit Tests — Analysis prompt builder utilities
 */

import { describe, expect, it } from "bun:test";
import type {
	ReviewCommentWithContext,
	ReviewerGroup,
} from "../review_prompt_mining/analyze/json-parser";
import {
	buildAnalysisPrompt,
	estimateTokenCount,
} from "../review_prompt_mining/analyze/prompt-builder";

function makeGroup(reviewer: string, commentBodies: string[], repo = "org/repo"): ReviewerGroup {
	const comments: ReviewCommentWithContext[] = commentBodies.map((body, i) => ({
		reviewer,
		body,
		diffHunk: `@@ -${i},1 +${i},1 @@`,
		createdAt: `2024-01-0${i + 1}T00:00:00Z`,
		repo,
	}));
	return { reviewer, comments, totalComments: comments.length };
}

describe("estimateTokenCount", () => {
	it("returns 0 for empty string", () => {
		expect(estimateTokenCount("")).toBe(0);
	});

	it("estimates 1 token for a 4-character string", () => {
		expect(estimateTokenCount("abcd")).toBe(1);
	});

	it("rounds up (ceiling) for non-divisible lengths", () => {
		// 5 chars → ceil(5/4) = 2
		expect(estimateTokenCount("abcde")).toBe(2);
	});

	it("scales linearly with string length", () => {
		const text = "a".repeat(400);
		expect(estimateTokenCount(text)).toBe(100);
	});

	it("handles a single character", () => {
		expect(estimateTokenCount("x")).toBe(1);
	});
});

describe("buildAnalysisPrompt", () => {
	it("returns a non-empty string", () => {
		const groups = [makeGroup("alice", ["looks good"])];
		const result = buildAnalysisPrompt(groups);
		expect(typeof result).toBe("string");
		expect(result.length).toBeGreaterThan(0);
	});

	it("includes the reviewer name", () => {
		const groups = [makeGroup("alice", ["nice work"])];
		const result = buildAnalysisPrompt(groups);
		expect(result).toContain("alice");
	});

	it("includes all reviewer names for multiple reviewers", () => {
		const groups = [makeGroup("alice", ["comment 1"]), makeGroup("bob", ["comment 2"])];
		const result = buildAnalysisPrompt(groups);
		expect(result).toContain("alice");
		expect(result).toContain("bob");
	});

	it("includes the total comment count", () => {
		const groups = [makeGroup("alice", ["c1", "c2", "c3"])];
		const result = buildAnalysisPrompt(groups);
		expect(result).toContain("3");
	});

	it("includes comment body text", () => {
		const groups = [makeGroup("alice", ["prefer immutable data structures"])];
		const result = buildAnalysisPrompt(groups);
		expect(result).toContain("prefer immutable data structures");
	});

	it("includes repository name", () => {
		const groups = [makeGroup("alice", ["comment"], "myorg/myrepo")];
		const result = buildAnalysisPrompt(groups);
		expect(result).toContain("myorg/myrepo");
	});

	it("includes diff hunk when present", () => {
		const group: ReviewerGroup = {
			reviewer: "alice",
			comments: [
				{
					reviewer: "alice",
					body: "use const",
					diffHunk: "@@ -1,3 +1,3 @@\n-let x = 1\n+const x = 1",
					createdAt: "2024-01-01T00:00:00Z",
					repo: "org/repo",
				},
			],
			totalComments: 1,
		};
		const result = buildAnalysisPrompt([group]);
		expect(result).toContain("const x = 1");
	});

	it("includes PR metadata when present", () => {
		const group: ReviewerGroup = {
			reviewer: "alice",
			comments: [
				{
					reviewer: "alice",
					body: "change requested",
					diffHunk: "",
					createdAt: "2024-01-01T00:00:00Z",
					repo: "org/repo",
					pr: {
						number: 42,
						title: "Add feature X",
						author: "bob",
						url: "https://github.com/org/repo/pull/42",
						state: "MERGED",
					},
				},
			],
			totalComments: 1,
		};
		const result = buildAnalysisPrompt([group]);
		expect(result).toContain("#42");
		expect(result).toContain("Add feature X");
	});

	it("handles empty groups array", () => {
		const result = buildAnalysisPrompt([]);
		expect(typeof result).toBe("string");
		// Should mention 0 comments
		expect(result).toContain("0");
	});

	it("handles groups with no comments", () => {
		const group: ReviewerGroup = { reviewer: "alice", comments: [], totalComments: 0 };
		const result = buildAnalysisPrompt([group]);
		expect(result).toContain("alice");
	});
});
