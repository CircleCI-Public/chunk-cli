/**
 * Unit Tests — Markdown report formatter
 */

import { describe, expect, it } from "bun:test";
import {
	formatMarkdownReport,
	type ReportMetadata,
} from "../review_prompt_mining/analyze/report-formatter";

const baseMetadata: ReportMetadata = {
	inputFile: "details.json",
	totalComments: 42,
	reviewers: ["alice", "bob"],
	analyzedAt: "2024-06-01T12:00:00Z",
};

describe("formatMarkdownReport", () => {
	it("returns a string", () => {
		const result = formatMarkdownReport("Some analysis.", baseMetadata);
		expect(typeof result).toBe("string");
	});

	it("includes the analysis body", () => {
		const result = formatMarkdownReport("My detailed analysis here.", baseMetadata);
		expect(result).toContain("My detailed analysis here.");
	});

	it("includes the analyzedAt timestamp", () => {
		const result = formatMarkdownReport("analysis", baseMetadata);
		expect(result).toContain("2024-06-01T12:00:00Z");
	});

	it("includes the input file name", () => {
		const result = formatMarkdownReport("analysis", baseMetadata);
		expect(result).toContain("details.json");
	});

	it("includes the total comments count", () => {
		const result = formatMarkdownReport("analysis", baseMetadata);
		expect(result).toContain("42");
	});

	it("includes all reviewer names", () => {
		const result = formatMarkdownReport("analysis", baseMetadata);
		expect(result).toContain("alice");
		expect(result).toContain("bob");
	});

	it("formats reviewers as a comma-separated list", () => {
		const result = formatMarkdownReport("analysis", baseMetadata);
		expect(result).toContain("alice, bob");
	});

	it("includes a heading", () => {
		const result = formatMarkdownReport("analysis", baseMetadata);
		expect(result).toContain("# Code Review Pattern Analysis");
	});

	it("handles a single reviewer", () => {
		const metadata: ReportMetadata = { ...baseMetadata, reviewers: ["carol"] };
		const result = formatMarkdownReport("analysis", metadata);
		expect(result).toContain("carol");
	});

	it("handles empty reviewers array", () => {
		const metadata: ReportMetadata = { ...baseMetadata, reviewers: [] };
		const result = formatMarkdownReport("analysis", metadata);
		expect(typeof result).toBe("string");
	});

	it("handles zero total comments", () => {
		const metadata: ReportMetadata = { ...baseMetadata, totalComments: 0 };
		const result = formatMarkdownReport("analysis", metadata);
		expect(result).toContain("0");
	});

	it("preserves multiline analysis", () => {
		const analysis = "Line 1\nLine 2\nLine 3";
		const result = formatMarkdownReport(analysis, baseMetadata);
		expect(result).toContain("Line 1");
		expect(result).toContain("Line 2");
		expect(result).toContain("Line 3");
	});
});
