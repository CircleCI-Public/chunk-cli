import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
	appendEvent,
	clearState,
	getBaselineFingerprint,
	loadField,
	readState,
	resolveFieldPath,
	saveEvent,
	statePath,
} from "../lib/state";

describe("state", () => {
	const testDir = join(tmpdir(), "chunk-hook-test-state", String(Date.now()));
	const sentinelDir = join(testDir, "sentinels");
	const projectDir = join(testDir, "project");

	beforeEach(() => {
		mkdirSync(sentinelDir, { recursive: true });
		mkdirSync(projectDir, { recursive: true });
	});

	afterEach(() => {
		rmSync(testDir, { recursive: true, force: true });
	});

	describe("statePath()", () => {
		it("returns a deterministic path based on project dir hash", () => {
			const path1 = statePath(sentinelDir, projectDir);
			const path2 = statePath(sentinelDir, projectDir);
			expect(path1).toBe(path2);
			expect(path1).toContain("state-");
			expect(path1).toMatch(/\.json$/);
		});

		it("returns different paths for different project dirs", () => {
			const path1 = statePath(sentinelDir, "/project/a");
			const path2 = statePath(sentinelDir, "/project/b");
			expect(path1).not.toBe(path2);
		});
	});

	describe("saveEvent() / readState()", () => {
		it("saves event data as a single-entry __entries array", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "Fix the tests",
				session_id: "abc",
			});
			const state = readState(sentinelDir, projectDir);
			expect(state).toEqual({
				UserPromptSubmit: {
					__entries: [{ prompt: "Fix the tests", session_id: "abc" }],
				},
			});
		});

		it("preserves other events when saving a new one", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "hello" });
			saveEvent(sentinelDir, projectDir, "Stop", { stop_hook_active: true });
			const state = readState(sentinelDir, projectDir);
			expect(state).toEqual({
				UserPromptSubmit: { __entries: [{ prompt: "hello" }] },
				Stop: { __entries: [{ stop_hook_active: true }] },
			});
		});

		it("overwrites entire event data on re-save", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "old",
				extra: "data",
			});
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "new",
			});
			const state = readState(sentinelDir, projectDir);
			const entries = state.UserPromptSubmit?.__entries;
			expect(entries).toEqual([{ prompt: "new" }]);
		});

		it("returns empty object when no state file exists", () => {
			const state = readState(sentinelDir, projectDir);
			expect(state).toEqual({});
		});

		it("returns empty object for malformed state file", () => {
			const path = statePath(sentinelDir, projectDir);
			writeFileSync(path, "not-json", "utf-8");
			const state = readState(sentinelDir, projectDir);
			expect(state).toEqual({});
		});

		it("returns empty object for array JSON", () => {
			const path = statePath(sentinelDir, projectDir);
			writeFileSync(path, "[1,2,3]", "utf-8");
			const state = readState(sentinelDir, projectDir);
			expect(state).toEqual({});
		});
	});

	describe("appendEvent()", () => {
		it("creates a single-entry array on first append", () => {
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "first",
				head: "aaa",
			});
			const state = readState(sentinelDir, projectDir);
			expect(state.UserPromptSubmit?.__entries).toEqual([{ prompt: "first", head: "aaa" }]);
		});

		it("accumulates entries on successive appends", () => {
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "first",
				head: "aaa",
			});
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "second",
				head: "bbb",
			});
			const state = readState(sentinelDir, projectDir);
			const entries = state.UserPromptSubmit?.__entries as Record<string, unknown>[];
			expect(entries).toHaveLength(2);
			expect(entries[0]).toEqual({ prompt: "first", head: "aaa" });
			expect(entries[1]).toEqual({ prompt: "second", head: "bbb" });
		});

		it("deduplicates consecutive entries with the same prompt", () => {
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "same",
				head: "aaa",
			});
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "same",
				head: "bbb",
			});
			const state = readState(sentinelDir, projectDir);
			const entries = state.UserPromptSubmit?.__entries as Record<string, unknown>[];
			expect(entries).toHaveLength(1);
		});

		it("does not deduplicate entries with different prompts", () => {
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "first",
			});
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "second",
			});
			const state = readState(sentinelDir, projectDir);
			const entries = state.UserPromptSubmit?.__entries as Record<string, unknown>[];
			expect(entries).toHaveLength(2);
		});

		it("preserves other events", () => {
			saveEvent(sentinelDir, projectDir, "Stop", { active: true });
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "hi" });
			const state = readState(sentinelDir, projectDir);
			expect(state.Stop).toBeDefined();
			expect(state.UserPromptSubmit).toBeDefined();
		});
	});

	describe("loadField()", () => {
		it("loads a top-level event field via dot notation (sugar for [0])", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "Fix the bug",
			});
			expect(loadField(sentinelDir, projectDir, "UserPromptSubmit.prompt")).toBe("Fix the bug");
		});

		it("loads a field via explicit bracket notation", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "Fix the bug",
			});
			expect(loadField(sentinelDir, projectDir, "UserPromptSubmit[0].prompt")).toBe("Fix the bug");
		});

		it("loads a deeply nested field", () => {
			saveEvent(sentinelDir, projectDir, "PreToolUse", {
				tool_input: { command: "git commit" },
			});
			expect(loadField(sentinelDir, projectDir, "PreToolUse.tool_input.command")).toBe(
				"git commit",
			);
		});

		it("returns the entire event block for an event name only", () => {
			saveEvent(sentinelDir, projectDir, "Stop", { active: true });
			const result = loadField(sentinelDir, projectDir, "Stop");
			// Returns the __entries wrapper — the raw event block
			expect(result).toEqual({ __entries: [{ active: true }] });
		});

		it("returns undefined for missing event", () => {
			saveEvent(sentinelDir, projectDir, "Stop", { x: 1 });
			expect(loadField(sentinelDir, projectDir, "UserPromptSubmit.prompt")).toBeUndefined();
		});

		it("returns undefined for missing field within event", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "hi" });
			expect(loadField(sentinelDir, projectDir, "UserPromptSubmit.missing")).toBeUndefined();
		});

		it("returns undefined when no state exists", () => {
			expect(loadField(sentinelDir, projectDir, "UserPromptSubmit.prompt")).toBeUndefined();
		});

		it("loads specific entry by index with multiple appends", () => {
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "first",
				head: "aaa",
			});
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "second",
				head: "bbb",
			});
			expect(loadField(sentinelDir, projectDir, "UserPromptSubmit[0].prompt")).toBe("first");
			expect(loadField(sentinelDir, projectDir, "UserPromptSubmit[1].prompt")).toBe("second");
			// Dot notation is sugar for [0]
			expect(loadField(sentinelDir, projectDir, "UserPromptSubmit.prompt")).toBe("first");
		});
	});

	describe("clearState()", () => {
		it("removes the state file", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "x" });
			const path = statePath(sentinelDir, projectDir);
			expect(existsSync(path)).toBe(true);

			clearState(sentinelDir, projectDir);
			expect(existsSync(path)).toBe(false);
		});

		it("does not throw when state file does not exist", () => {
			expect(() => clearState(sentinelDir, projectDir)).not.toThrow();
		});
	});

	describe("session-aware state", () => {
		it("saves session ID as __session entry", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "hi" }, "session-1");
			const state = readState(sentinelDir, projectDir);
			expect(state.__session).toEqual({ id: "session-1" });
		});

		it("preserves state within the same session", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "first" }, "session-1");
			saveEvent(sentinelDir, projectDir, "Stop", { active: true }, "session-1");
			const state = readState(sentinelDir, projectDir);
			expect(state.UserPromptSubmit).toBeDefined();
			expect(state.Stop).toBeDefined();
			expect(state.__session).toEqual({ id: "session-1" });
		});

		it("overwrites state when session changes (save)", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "old" }, "session-1");
			saveEvent(sentinelDir, projectDir, "Stop", { active: true }, "session-2");
			const state = readState(sentinelDir, projectDir);
			// Old session data is gone
			expect(state.UserPromptSubmit).toBeUndefined();
			// New session data present
			expect(state.Stop).toEqual({ __entries: [{ active: true }] });
			expect(state.__session).toEqual({ id: "session-2" });
		});

		it("overwrites state when session changes (append)", () => {
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "old" }, "session-1");
			appendEvent(sentinelDir, projectDir, "Stop", { active: true }, "session-2");
			const state = readState(sentinelDir, projectDir);
			expect(state.UserPromptSubmit).toBeUndefined();
			expect(state.Stop).toEqual({ __entries: [{ active: true }] });
			expect(state.__session).toEqual({ id: "session-2" });
		});

		it("does not overwrite when sessionId is omitted", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "old" }, "session-1");
			saveEvent(sentinelDir, projectDir, "Stop", { active: true });
			const state = readState(sentinelDir, projectDir);
			// Both events present — no session comparison when omitted
			expect(state.UserPromptSubmit).toBeDefined();
			expect(state.Stop).toBeDefined();
		});

		it("clearState skips when session does not match", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "keep" }, "session-1");
			clearState(sentinelDir, projectDir, "session-2");
			const state = readState(sentinelDir, projectDir);
			expect(state.UserPromptSubmit).toBeDefined();
		});

		it("clearState succeeds when session matches", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "gone" }, "session-1");
			clearState(sentinelDir, projectDir, "session-1");
			const state = readState(sentinelDir, projectDir);
			expect(state).toEqual({});
		});

		it("clearState succeeds unconditionally without sessionId", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "gone" }, "session-1");
			clearState(sentinelDir, projectDir);
			const state = readState(sentinelDir, projectDir);
			expect(state).toEqual({});
		});
	});

	describe("getBaselineFingerprint()", () => {
		it("returns fingerprint from first entry after save", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "fix bug",
				fingerprint: "fp_abc123",
			});
			expect(getBaselineFingerprint(sentinelDir, projectDir, "UserPromptSubmit")).toBe("fp_abc123");
		});

		it("returns fingerprint from last entry after multiple appends", () => {
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "first",
				fingerprint: "fp_first",
			});
			appendEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "second",
				fingerprint: "fp_second",
			});
			expect(getBaselineFingerprint(sentinelDir, projectDir, "UserPromptSubmit")).toBe("fp_second");
		});

		it("returns undefined when event does not exist", () => {
			expect(getBaselineFingerprint(sentinelDir, projectDir, "UserPromptSubmit")).toBeUndefined();
		});

		it("returns undefined when first entry has no fingerprint", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "no fingerprint",
				head: "abc123",
			});
			expect(getBaselineFingerprint(sentinelDir, projectDir, "UserPromptSubmit")).toBeUndefined();
		});

		it("returns undefined when fingerprint is empty string", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "empty fp",
				fingerprint: "",
			});
			expect(getBaselineFingerprint(sentinelDir, projectDir, "UserPromptSubmit")).toBeUndefined();
		});
	});

	describe("resolveFieldPath()", () => {
		it("resolves a top-level key", () => {
			const state = { UserPromptSubmit: { prompt: "hello" } };
			expect(resolveFieldPath(state, "UserPromptSubmit")).toEqual({
				prompt: "hello",
			});
		});

		it("resolves a dotted path", () => {
			const state = { UserPromptSubmit: { prompt: "world" } };
			expect(resolveFieldPath(state, "UserPromptSubmit.prompt")).toBe("world");
		});

		it("resolves deeply nested paths", () => {
			const state = {
				PreToolUse: { tool_input: { command: "git push", args: ["--force"] } },
			};
			expect(resolveFieldPath(state, "PreToolUse.tool_input.command")).toBe("git push");
		});

		it("returns undefined for missing top-level key", () => {
			expect(resolveFieldPath({}, "Missing.field")).toBeUndefined();
		});

		it("returns undefined for missing nested field", () => {
			const state = { UserPromptSubmit: { prompt: "hi" } };
			expect(resolveFieldPath(state, "UserPromptSubmit.nonexistent")).toBeUndefined();
		});

		it("returns undefined for path through non-object", () => {
			const state = { UserPromptSubmit: { prompt: "hi" } };
			expect(resolveFieldPath(state, "UserPromptSubmit.prompt.nested")).toBeUndefined();
		});

		describe("__entries sugar", () => {
			it("dot notation resolves to __entries[0]", () => {
				const state = {
					UserPromptSubmit: {
						__entries: [{ prompt: "first" }, { prompt: "second" }],
					},
				};
				expect(resolveFieldPath(state, "UserPromptSubmit.prompt")).toBe("first");
			});

			it("bracket notation accesses specific entries", () => {
				const state = {
					UserPromptSubmit: {
						__entries: [
							{ prompt: "first", head: "aaa" },
							{ prompt: "second", head: "bbb" },
						],
					},
				};
				expect(resolveFieldPath(state, "UserPromptSubmit[0].prompt")).toBe("first");
				expect(resolveFieldPath(state, "UserPromptSubmit[1].prompt")).toBe("second");
				expect(resolveFieldPath(state, "UserPromptSubmit[1].head")).toBe("bbb");
			});

			it("returns undefined for out-of-bounds index", () => {
				const state = {
					UserPromptSubmit: {
						__entries: [{ prompt: "only" }],
					},
				};
				expect(resolveFieldPath(state, "UserPromptSubmit[5].prompt")).toBeUndefined();
			});

			it("allows direct __entries access", () => {
				const state = {
					UserPromptSubmit: {
						__entries: [{ prompt: "a" }, { prompt: "b" }],
					},
				};
				const entries = resolveFieldPath(state, "UserPromptSubmit.__entries");
				expect(Array.isArray(entries)).toBe(true);
				expect(entries).toHaveLength(2);
			});

			it("returns undefined for missing field in entry", () => {
				const state = {
					UserPromptSubmit: { __entries: [{ prompt: "hi" }] },
				};
				expect(resolveFieldPath(state, "UserPromptSubmit.nonexistent")).toBeUndefined();
			});
		});
	});
});
