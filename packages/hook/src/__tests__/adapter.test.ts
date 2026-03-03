import { describe, expect, it } from "bun:test";
import type { AgentEvent } from "../lib/adapter";
import {
	createClaudeAdapter,
	createStdinExitCodeBase,
	getAdapter,
	mapHookInputToEvent,
} from "../lib/adapter";

// ---------------------------------------------------------------------------
// Helper: build an AgentEvent from partial fields
// ---------------------------------------------------------------------------

function event(partial: Partial<AgentEvent> = {}): AgentEvent {
	return {
		eventName: "",
		raw: {},
		...partial,
	};
}

// ---------------------------------------------------------------------------
// createStdinExitCodeBase()
// ---------------------------------------------------------------------------

describe("createStdinExitCodeBase()", () => {
	it("returns an object with readEvent, allow, and block", () => {
		const base = createStdinExitCodeBase();
		expect(typeof base.readEvent).toBe("function");
		expect(typeof base.allow).toBe("function");
		expect(typeof base.block).toBe("function");
	});
});

// ---------------------------------------------------------------------------
// createClaudeAdapter() — behavioral methods
// ---------------------------------------------------------------------------

describe("createClaudeAdapter()", () => {
	const adapter = createClaudeAdapter();

	describe("getProjectDir()", () => {
		it("returns process.cwd()", () => {
			expect(adapter.getProjectDir()).toBe(process.cwd());
		});
	});

	describe("isStopRecursion()", () => {
		it("returns true for Stop event with stop_hook_active", () => {
			expect(
				adapter.isStopRecursion(event({ eventName: "Stop", raw: { stop_hook_active: true } })),
			).toBe(true);
		});

		it("returns false for Stop event without stop_hook_active", () => {
			expect(
				adapter.isStopRecursion(event({ eventName: "Stop", raw: { stop_hook_active: false } })),
			).toBe(false);
		});

		it("returns false for Stop event with missing stop_hook_active", () => {
			expect(adapter.isStopRecursion(event({ eventName: "Stop", raw: {} }))).toBe(false);
		});

		it("returns false for non-Stop events even with stop_hook_active", () => {
			expect(
				adapter.isStopRecursion(
					event({
						eventName: "PreToolUse",
						raw: { stop_hook_active: true },
					}),
				),
			).toBe(false);
		});

		it("returns true for Stop event with camelCase stopHookActive", () => {
			expect(
				adapter.isStopRecursion(event({ eventName: "Stop", raw: { stopHookActive: true } })),
			).toBe(true);
		});

		it("prefers snake_case stop_hook_active over camelCase", () => {
			expect(
				adapter.isStopRecursion(
					event({
						eventName: "Stop",
						raw: { stop_hook_active: false, stopHookActive: true },
					}),
				),
			).toBe(false);
		});

		// Cursor compatibility: camelCase event names
		it("returns true for Cursor stop event with stop_hook_active", () => {
			expect(
				adapter.isStopRecursion(event({ eventName: "stop", raw: { stop_hook_active: true } })),
			).toBe(true);
		});

		it("returns false for Cursor stop event without stop_hook_active", () => {
			expect(adapter.isStopRecursion(event({ eventName: "stop", raw: {} }))).toBe(false);
		});
	});

	describe("isShellToolCall()", () => {
		it("returns true for PreToolUse:Bash", () => {
			expect(adapter.isShellToolCall(event({ eventName: "PreToolUse", toolName: "Bash" }))).toBe(
				true,
			);
		});

		it("returns false for PreToolUse with non-Bash tool", () => {
			expect(adapter.isShellToolCall(event({ eventName: "PreToolUse", toolName: "Write" }))).toBe(
				false,
			);
		});

		it("returns false for non-PreToolUse event", () => {
			expect(adapter.isShellToolCall(event({ eventName: "Stop", toolName: "Bash" }))).toBe(false);
		});

		it("returns false when toolName is absent", () => {
			expect(adapter.isShellToolCall(event({ eventName: "PreToolUse" }))).toBe(false);
		});

		// Cursor compatibility: camelCase event name + Shell tool name
		it("returns true for Cursor preToolUse:Shell", () => {
			expect(adapter.isShellToolCall(event({ eventName: "preToolUse", toolName: "Shell" }))).toBe(
				true,
			);
		});

		// VS Code Copilot compatibility: run_in_terminal tool name
		it("returns true for VS Code PreToolUse:run_in_terminal", () => {
			expect(
				adapter.isShellToolCall(event({ eventName: "PreToolUse", toolName: "run_in_terminal" })),
			).toBe(true);
		});
	});

	describe("getShellCommand()", () => {
		it("extracts command from a shell tool call", () => {
			expect(
				adapter.getShellCommand(
					event({
						eventName: "PreToolUse",
						toolName: "Bash",
						toolInput: { command: "npm test" },
					}),
				),
			).toBe("npm test");
		});

		it("returns undefined for non-shell events", () => {
			expect(
				adapter.getShellCommand(event({ eventName: "Stop", toolInput: { command: "npm test" } })),
			).toBeUndefined();
		});

		it("returns undefined when command is not a string", () => {
			expect(
				adapter.getShellCommand(
					event({
						eventName: "PreToolUse",
						toolName: "Bash",
						toolInput: { command: 42 },
					}),
				),
			).toBeUndefined();
		});

		it("returns undefined when toolInput has no command", () => {
			expect(
				adapter.getShellCommand(
					event({
						eventName: "PreToolUse",
						toolName: "Bash",
						toolInput: {},
					}),
				),
			).toBeUndefined();
		});

		it("returns undefined when toolInput is absent", () => {
			expect(
				adapter.getShellCommand(event({ eventName: "PreToolUse", toolName: "Bash" })),
			).toBeUndefined();
		});

		// Cursor compatibility: Shell tool name
		it("extracts command from Cursor Shell tool call", () => {
			expect(
				adapter.getShellCommand(
					event({
						eventName: "preToolUse",
						toolName: "Shell",
						toolInput: { command: "go test ./..." },
					}),
				),
			).toBe("go test ./...");
		});
	});

	describe("stateKey()", () => {
		it("returns the verbatim event name", () => {
			expect(adapter.stateKey(event({ eventName: "UserPromptSubmit" }))).toBe("UserPromptSubmit");
		});

		it("returns empty string for empty eventName", () => {
			expect(adapter.stateKey(event({ eventName: "" }))).toBe("");
		});

		// Cursor compatibility: camelCase event names normalize to PascalCase
		it("normalizes Cursor stop → Stop", () => {
			expect(adapter.stateKey(event({ eventName: "stop" }))).toBe("Stop");
		});

		it("normalizes Cursor preToolUse → PreToolUse", () => {
			expect(adapter.stateKey(event({ eventName: "preToolUse" }))).toBe("PreToolUse");
		});

		// Cursor compatibility: renamed event
		it("normalizes Cursor beforeSubmitPrompt → UserPromptSubmit", () => {
			expect(adapter.stateKey(event({ eventName: "beforeSubmitPrompt" }))).toBe("UserPromptSubmit");
		});
	});

	describe("commandSummary()", () => {
		it("returns formatted summary for shell tool calls", () => {
			expect(
				adapter.commandSummary(
					event({
						eventName: "PreToolUse",
						toolName: "Bash",
						toolInput: { command: "git commit -m 'fix'" },
					}),
				),
			).toBe(` command="git commit -m 'fix'"`);
		});

		it("truncates commands longer than 80 chars", () => {
			const longCmd = "a".repeat(100);
			const result = adapter.commandSummary(
				event({
					eventName: "PreToolUse",
					toolName: "Bash",
					toolInput: { command: longCmd },
				}),
			);
			expect(result).toContain("...");
			expect(result).toBe(` command="${"a".repeat(77)}..."`);
		});

		it("returns empty string for non-shell events", () => {
			expect(adapter.commandSummary(event({ eventName: "Stop" }))).toBe("");
		});

		it("returns empty string when command is missing", () => {
			expect(
				adapter.commandSummary(
					event({
						eventName: "PreToolUse",
						toolName: "Bash",
						toolInput: {},
					}),
				),
			).toBe("");
		});
	});
});

// ---------------------------------------------------------------------------
// getAdapter()
// ---------------------------------------------------------------------------

describe("getAdapter()", () => {
	it("returns a valid HookAdapter", () => {
		const adapter = getAdapter();
		expect(typeof adapter.readEvent).toBe("function");
		expect(typeof adapter.allow).toBe("function");
		expect(typeof adapter.block).toBe("function");
		expect(typeof adapter.getProjectDir).toBe("function");
		expect(typeof adapter.isStopRecursion).toBe("function");
		expect(typeof adapter.isShellToolCall).toBe("function");
		expect(typeof adapter.getShellCommand).toBe("function");
		expect(typeof adapter.stateKey).toBe("function");
		expect(typeof adapter.commandSummary).toBe("function");
	});

	it("returns a Claude adapter (default)", () => {
		const adapter = getAdapter();
		expect(adapter.getProjectDir()).toBe(process.cwd());
	});
});

// ---------------------------------------------------------------------------
// mapHookInputToEvent()
// ---------------------------------------------------------------------------

describe("mapHookInputToEvent()", () => {
	it("maps snake_case hook input to AgentEvent", () => {
		const input = {
			hook_event_name: "PreToolUse",
			tool_name: "Bash",
			tool_input: { command: "go test ./..." },
			cwd: "/repo",
			session_id: "abc-123",
		};

		const ev = mapHookInputToEvent(input);
		expect(ev.eventName).toBe("PreToolUse");
		expect(ev.toolName).toBe("Bash");
		expect(ev.toolInput).toEqual({ command: "go test ./..." });
		expect(ev.cwd).toBe("/repo");
		expect(ev.raw).toBe(input);
	});

	it("handles missing fields gracefully", () => {
		const ev = mapHookInputToEvent({});
		expect(ev.eventName).toBe("");
		expect(ev.toolName).toBeUndefined();
		expect(ev.toolInput).toBeUndefined();
		expect(ev.cwd).toBeUndefined();
		expect(ev.raw).toEqual({});
	});

	it("preserves all raw fields", () => {
		const input = {
			hook_event_name: "Stop",
			stop_hook_active: true,
			transcript_path: "/tmp/transcript.json",
		};

		const ev = mapHookInputToEvent(input);
		expect(ev.raw.stop_hook_active).toBe(true);
		expect(ev.raw.transcript_path).toBe("/tmp/transcript.json");
	});

	it("maps camelCase hook input (VS Code Copilot format)", () => {
		const input = {
			hookEventName: "PostToolUse",
			tool_name: "run_in_terminal",
			tool_input: { command: "npm test" },
			cwd: "/workspace/project",
			sessionId: "abc-123",
		};

		const ev = mapHookInputToEvent(input);
		expect(ev.eventName).toBe("PostToolUse");
		expect(ev.toolName).toBe("run_in_terminal");
		expect(ev.toolInput).toEqual({ command: "npm test" });
		expect(ev.cwd).toBe("/workspace/project");
		expect(ev.raw).toBe(input);
	});

	it("prefers snake_case over camelCase when both present", () => {
		const input = {
			hook_event_name: "PreToolUse",
			hookEventName: "PostToolUse",
			tool_name: "Bash",
			toolName: "Write",
		};

		const ev = mapHookInputToEvent(input);
		expect(ev.eventName).toBe("PreToolUse");
		expect(ev.toolName).toBe("Bash");
	});

	it("falls back to camelCase toolInput when snake_case absent", () => {
		const input = {
			hookEventName: "PreToolUse",
			toolName: "Bash",
			toolInput: { command: "ls" },
			cwd: "/workspace",
		};

		const ev = mapHookInputToEvent(input);
		expect(ev.eventName).toBe("PreToolUse");
		expect(ev.toolName).toBe("Bash");
		expect(ev.toolInput).toEqual({ command: "ls" });
	});
});
