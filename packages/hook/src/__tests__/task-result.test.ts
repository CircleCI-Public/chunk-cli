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

		it("injects sessionId into allow sentinel when provided", () => {
			const sentinel = taskResultToSentinel({ decision: "allow" }, undefined, "sess-123");
			expect(sentinel.status).toBe("pass");
			expect(sentinel.sessionId).toBe("sess-123");
		});

		it("injects sessionId into block sentinel when provided", () => {
			const sentinel = taskResultToSentinel(
				{ decision: "block", reason: "bad" },
				undefined,
				"sess-456",
			);
			expect(sentinel.status).toBe("fail");
			expect(sentinel.sessionId).toBe("sess-456");
		});

		it("omits sessionId when not provided", () => {
			const sentinel = taskResultToSentinel({ decision: "allow" });
			expect(sentinel.sessionId).toBeUndefined();
		});

		it("omits sessionId when undefined is passed explicitly", () => {
			const sentinel = taskResultToSentinel({ decision: "block" }, undefined, undefined);
			expect(sentinel.sessionId).toBeUndefined();
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

		it("passes through existing SentinelData format", () => {
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

		it("injects sessionId into converted TaskResult", () => {
			const path = sentinelPath(testDir, projectDir, name);
			writeFileSync(path, JSON.stringify({ decision: "allow" }), "utf-8");

			const result = readTaskResult(testDir, projectDir, name, "sess-abc");
			expect(result).toBeDefined();
			expect(result?.status).toBe("pass");
			expect(result?.sessionId).toBe("sess-abc");
		});

		it("does not inject sessionId into existing SentinelData format", () => {
			writeSentinel(testDir, projectDir, name, {
				status: "pass",
				startedAt: "2024-01-01T00:00:00Z",
				project: projectDir,
			});

			const result = readTaskResult(testDir, projectDir, name, "sess-xyz");
			expect(result).toBeDefined();
			// SentinelData pass-through should NOT have sessionId injected —
			// it already has whatever the original writer set.
			expect(result?.sessionId).toBeUndefined();
		});

		it("injects sessionId into block TaskResult", () => {
			const path = sentinelPath(testDir, projectDir, name);
			writeFileSync(path, JSON.stringify({ decision: "block", reason: "nope" }), "utf-8");

			const result = readTaskResult(testDir, projectDir, name, "sess-block");
			expect(result).toBeDefined();
			expect(result?.status).toBe("fail");
			expect(result?.sessionId).toBe("sess-block");
		});
	});
});
