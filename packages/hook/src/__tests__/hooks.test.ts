import { describe, expect, it } from "bun:test";
import type { AgentEvent, HookAdapter } from "../lib/adapter";
import { createClaudeAdapter } from "../lib/adapter";
import { matchesTrigger } from "../lib/check";

// ---------------------------------------------------------------------------
// Adapter-backed matchesTrigger() — tests via Claude adapter behavioral methods
// ---------------------------------------------------------------------------

/** Build an AgentEvent from partial Claude hook fields. */
function event(partial: Partial<AgentEvent> = {}): AgentEvent {
	return { eventName: "", raw: {}, ...partial };
}

/** The Claude adapter provides the behavioral methods under test. */
const adapter: HookAdapter = createClaudeAdapter();

describe("matchesTrigger() via adapter", () => {
	it("matches when patterns list is empty (no filter)", () => {
		const e = event({
			eventName: "PreToolUse",
			toolName: "Bash",
			toolInput: { command: "ls -la" },
		});
		expect(matchesTrigger(adapter, e, [])).toBe(true);
	});

	it("matches PreToolUse:Bash when command contains a trigger pattern", () => {
		const e = event({
			eventName: "PreToolUse",
			toolName: "Bash",
			toolInput: { command: "git commit -m 'fix tests'" },
		});
		expect(matchesTrigger(adapter, e, ["git commit", "git push"])).toBe(true);
	});

	it("does not match PreToolUse:Bash when command has no trigger pattern", () => {
		const e = event({
			eventName: "PreToolUse",
			toolName: "Bash",
			toolInput: { command: "npm test" },
		});
		expect(matchesTrigger(adapter, e, ["git commit", "git push"])).toBe(false);
	});

	it("matching is case-insensitive", () => {
		const e = event({
			eventName: "PreToolUse",
			toolName: "Bash",
			toolInput: { command: "GIT COMMIT -m 'done'" },
		});
		expect(matchesTrigger(adapter, e, ["git commit"])).toBe(true);
	});

	it("does not match non-shell events when patterns are set", () => {
		const e = event({ eventName: "TaskCompleted" });
		expect(matchesTrigger(adapter, e, ["git commit"])).toBe(false);
	});

	it("does not match non-shell tool calls when patterns are set", () => {
		const e = event({
			eventName: "PreToolUse",
			toolName: "Write",
			toolInput: { path: "/some/file" },
		});
		expect(matchesTrigger(adapter, e, ["git commit"])).toBe(false);
	});

	it("matches non-shell tool calls when patterns list is empty", () => {
		const e = event({
			eventName: "PreToolUse",
			toolName: "Write",
			toolInput: { path: "/some/file" },
		});
		expect(matchesTrigger(adapter, e, [])).toBe(true);
	});

	it("does not match when tool_input has no command", () => {
		const e = event({
			eventName: "PreToolUse",
			toolName: "Bash",
			toolInput: {},
		});
		expect(matchesTrigger(adapter, e, ["git commit"])).toBe(false);
	});

	it("does not match when tool_input.command is not a string", () => {
		const e = event({
			eventName: "PreToolUse",
			toolName: "Bash",
			toolInput: { command: 42 },
		});
		expect(matchesTrigger(adapter, e, ["git commit"])).toBe(false);
	});
});
