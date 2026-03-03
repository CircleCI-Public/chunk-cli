import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { sentinelPath, writeSentinel } from "../lib/sentinel";
import { readTaskResult, taskResultToSentinel, validateTaskResult } from "../lib/task-result";

describe("task-result", () => {
	const testDir = join(tmpdir(), "chunk-hook-test-task", String(Date.now()));
	const projectDir = "/fake/project";
	const name = "review" as const;

	beforeEach(() => {
		mkdirSync(testDir, { recursive: true });
	});

	afterEach(() => {
		rmSync(testDir, { recursive: true, force: true });
	});

	// -----------------------------------------------------------------------
	// validateTaskResult
	// -----------------------------------------------------------------------

	describe("validateTaskResult()", () => {
		it("accepts an allow result", () => {
			const result = validateTaskResult({ decision: "allow" });
			expect(result).toEqual({ decision: "allow" });
		});

		it("accepts a block result with reason and extra fields", () => {
			const input = {
				decision: "block" as const,
				reason: "Found issues",
				issues: [{ severity: "HIGH", file: "main.ts:10", message: "unused var" }],
			};
			const result = validateTaskResult(input);
			expect(result).toEqual(input);
		});

		it("rejects null", () => {
			expect(validateTaskResult(null)).toBeUndefined();
		});

		it("rejects non-object", () => {
			expect(validateTaskResult("string")).toBeUndefined();
		});

		it("rejects object without decision field", () => {
			expect(validateTaskResult({ reason: "something" })).toBeUndefined();
		});

		it("rejects object with invalid decision value", () => {
			expect(validateTaskResult({ decision: "pass" })).toBeUndefined();
		});
	});

	// -----------------------------------------------------------------------
	// taskResultToSentinel
	// -----------------------------------------------------------------------

	describe("taskResultToSentinel()", () => {
		it("converts an allow result to a pass sentinel", () => {
			const sentinel = taskResultToSentinel({ decision: "allow" });
			expect(sentinel.status).toBe("pass");
			expect(sentinel.details).toBe("Task passed.");
			expect(sentinel.startedAt).toBeDefined();
			expect(sentinel.finishedAt).toBeDefined();
		});

		it("converts an allow result with reason", () => {
			const sentinel = taskResultToSentinel({
				decision: "allow",
				reason: "All checks passed",
			});
			expect(sentinel.status).toBe("pass");
			expect(sentinel.details).toBe("All checks passed");
		});

		it("converts a block result to a fail sentinel", () => {
			const raw = JSON.stringify({
				decision: "block",
				reason: "SQL injection found",
				issues: [{ severity: "CRITICAL", file: "main.ts:5" }],
			});
			const sentinel = taskResultToSentinel(
				{ decision: "block", reason: "SQL injection found" },
				raw,
			);
			expect(sentinel.status).toBe("fail");
			expect(sentinel.details).toBe("SQL injection found");
			expect(sentinel.rawResult).toBe(raw);
		});

		it("handles a block result with no reason", () => {
			const sentinel = taskResultToSentinel({ decision: "block" });
			expect(sentinel.status).toBe("fail");
			expect(sentinel.details).toBe("(no reason provided)");
		});

		it("does not attach rawResult on allow", () => {
			const raw = JSON.stringify({ decision: "allow", reason: "ok" });
			const sentinel = taskResultToSentinel({ decision: "allow", reason: "ok" }, raw);
			expect(sentinel.status).toBe("pass");
			expect(sentinel.rawResult).toBeUndefined();
		});
	});

	// -----------------------------------------------------------------------
	// readTaskResult (non-consuming)
	// -----------------------------------------------------------------------

	describe("readTaskResult()", () => {
		it("returns undefined when no file exists", () => {
			const result = readTaskResult(testDir, projectDir, name);
			expect(result).toBeUndefined();
		});

		it("reads an allow result without deleting the file", () => {
			const path = sentinelPath(testDir, projectDir, name);
			writeFileSync(path, JSON.stringify({ decision: "allow" }), "utf-8");

			const result = readTaskResult(testDir, projectDir, name);
			expect(result).toBeDefined();
			expect(result?.status).toBe("pass");
			// File should still exist (non-consuming)
			expect(existsSync(path)).toBe(true);
		});

		it("reads a block result and preserves raw JSON", () => {
			const path = sentinelPath(testDir, projectDir, name);
			const taskJson = {
				decision: "block",
				reason: "Found a bug",
				issues: [{ severity: "HIGH", file: "x.ts:3", message: "bug" }],
			};
			const raw = JSON.stringify(taskJson);
			writeFileSync(path, raw, "utf-8");

			const result = readTaskResult(testDir, projectDir, name);
			expect(result).toBeDefined();
			expect(result?.status).toBe("fail");
			expect(result?.details).toBe("Found a bug");
			expect(result?.rawResult).toBe(raw);
			// File should still exist (non-consuming)
			expect(existsSync(path)).toBe(true);
		});

		it("passes through existing SentinelData format (legacy compat)", () => {
			writeSentinel(testDir, projectDir, name, {
				status: "pending",
				startedAt: "2024-01-01T00:00:00Z",
				project: projectDir,
			});

			const result = readTaskResult(testDir, projectDir, name);
			expect(result).toBeDefined();
			expect(result?.status).toBe("pending");
			expect(result?.startedAt).toBe("2024-01-01T00:00:00Z");
		});

		it("returns undefined for malformed JSON (file preserved)", () => {
			const path = sentinelPath(testDir, projectDir, name);
			writeFileSync(path, "not json at all", "utf-8");

			const result = readTaskResult(testDir, projectDir, name);
			expect(result).toBeUndefined();
			// File should still exist (non-consuming, no cleanup)
			expect(existsSync(path)).toBe(true);
		});

		it("returns undefined for valid JSON without decision field", () => {
			const path = sentinelPath(testDir, projectDir, name);
			writeFileSync(path, JSON.stringify({ foo: "bar" }), "utf-8");

			const result = readTaskResult(testDir, projectDir, name);
			expect(result).toBeUndefined();
		});
	});
});
