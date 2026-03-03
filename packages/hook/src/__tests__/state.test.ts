import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
	clearState,
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
		it("saves event data under the event name", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "Fix the tests",
				session_id: "abc",
			});
			const state = readState(sentinelDir, projectDir);
			expect(state).toEqual({
				UserPromptSubmit: { prompt: "Fix the tests", session_id: "abc" },
			});
		});

		it("preserves other events when saving a new one", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", { prompt: "hello" });
			saveEvent(sentinelDir, projectDir, "Stop", { stop_hook_active: true });
			const state = readState(sentinelDir, projectDir);
			expect(state).toEqual({
				UserPromptSubmit: { prompt: "hello" },
				Stop: { stop_hook_active: true },
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
			expect(state.UserPromptSubmit).toEqual({ prompt: "new" });
			// "extra" is gone — full overwrite, not merge
			expect(state.UserPromptSubmit).not.toHaveProperty("extra");
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

	describe("loadField()", () => {
		it("loads a top-level event field via dot notation", () => {
			saveEvent(sentinelDir, projectDir, "UserPromptSubmit", {
				prompt: "Fix the bug",
			});
			expect(loadField(sentinelDir, projectDir, "UserPromptSubmit.prompt")).toBe("Fix the bug");
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
			expect(result).toEqual({ active: true });
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
	});
});
