import { describe, expect, it } from "bun:test";
import {
	extractSessionId,
	extractShellCommand,
	isPreToolUseEvent,
	isShellTool,
	isStopEvent,
	isStopHookActive,
	mapHookInputToEvent,
	normalizeEventName,
} from "../lib/compat";

// ---------------------------------------------------------------------------
// normalizeEventName()
// ---------------------------------------------------------------------------

describe("normalizeEventName()", () => {
	describe("Claude Code (PascalCase — canonical, pass-through)", () => {
		it("preserves PreToolUse", () => {
			expect(normalizeEventName("PreToolUse")).toBe("PreToolUse");
		});

		it("preserves PostToolUse", () => {
			expect(normalizeEventName("PostToolUse")).toBe("PostToolUse");
		});

		it("preserves UserPromptSubmit", () => {
			expect(normalizeEventName("UserPromptSubmit")).toBe("UserPromptSubmit");
		});

		it("preserves Stop", () => {
			expect(normalizeEventName("Stop")).toBe("Stop");
		});

		it("preserves SessionStart", () => {
			expect(normalizeEventName("SessionStart")).toBe("SessionStart");
		});

		it("preserves SessionEnd", () => {
			expect(normalizeEventName("SessionEnd")).toBe("SessionEnd");
		});

		it("preserves NotificationShown", () => {
			expect(normalizeEventName("NotificationShown")).toBe("NotificationShown");
		});
	});

	describe("Cursor (camelCase — normalized to PascalCase)", () => {
		it("normalizes preToolUse → PreToolUse", () => {
			expect(normalizeEventName("preToolUse")).toBe("PreToolUse");
		});

		it("normalizes postToolUse → PostToolUse", () => {
			expect(normalizeEventName("postToolUse")).toBe("PostToolUse");
		});

		it("normalizes stop → Stop", () => {
			expect(normalizeEventName("stop")).toBe("Stop");
		});

		it("normalizes sessionStart → SessionStart", () => {
			expect(normalizeEventName("sessionStart")).toBe("SessionStart");
		});

		it("normalizes sessionEnd → SessionEnd", () => {
			expect(normalizeEventName("sessionEnd")).toBe("SessionEnd");
		});

		it("normalizes notificationShown → NotificationShown", () => {
			expect(normalizeEventName("notificationShown")).toBe("NotificationShown");
		});

		it("normalizes beforeSubmitPrompt → UserPromptSubmit (Cursor rename)", () => {
			expect(normalizeEventName("beforeSubmitPrompt")).toBe("UserPromptSubmit");
		});
	});

	describe("edge cases", () => {
		it("returns empty string for empty input", () => {
			expect(normalizeEventName("")).toBe("");
		});

		it("returns unknown events verbatim", () => {
			expect(normalizeEventName("CustomEvent")).toBe("CustomEvent");
		});

		it("handles case variations of known events", () => {
			expect(normalizeEventName("STOP")).toBe("Stop");
			expect(normalizeEventName("PRETOOLUSE")).toBe("PreToolUse");
		});
	});
});

// ---------------------------------------------------------------------------
// isShellTool()
// ---------------------------------------------------------------------------

describe("isShellTool()", () => {
	describe("Claude Code", () => {
		it("returns true for Bash", () => {
			expect(isShellTool("Bash")).toBe(true);
		});
	});

	describe("Cursor", () => {
		it("returns true for Shell", () => {
			expect(isShellTool("Shell")).toBe(true);
		});
	});

	describe("VS Code Copilot", () => {
		it("returns true for run_in_terminal", () => {
			expect(isShellTool("run_in_terminal")).toBe(true);
		});
	});

	describe("non-shell tools", () => {
		it("returns false for Write", () => {
			expect(isShellTool("Write")).toBe(false);
		});

		it("returns false for Read", () => {
			expect(isShellTool("Read")).toBe(false);
		});

		it("returns false for Edit", () => {
			expect(isShellTool("Edit")).toBe(false);
		});

		it("returns false for undefined", () => {
			expect(isShellTool(undefined)).toBe(false);
		});

		it("returns false for empty string", () => {
			expect(isShellTool("")).toBe(false);
		});
	});
});

// ---------------------------------------------------------------------------
// isPreToolUseEvent()
// ---------------------------------------------------------------------------

describe("isPreToolUseEvent()", () => {
	it("returns true for Claude Code PreToolUse", () => {
		expect(isPreToolUseEvent("PreToolUse")).toBe(true);
	});

	it("returns true for Cursor preToolUse", () => {
		expect(isPreToolUseEvent("preToolUse")).toBe(true);
	});

	it("returns false for PostToolUse", () => {
		expect(isPreToolUseEvent("PostToolUse")).toBe(false);
	});

	it("returns false for Stop", () => {
		expect(isPreToolUseEvent("Stop")).toBe(false);
	});

	it("returns false for empty string", () => {
		expect(isPreToolUseEvent("")).toBe(false);
	});
});

// ---------------------------------------------------------------------------
// isStopEvent()
// ---------------------------------------------------------------------------

describe("isStopEvent()", () => {
	it("returns true for Claude Code Stop", () => {
		expect(isStopEvent("Stop")).toBe(true);
	});

	it("returns true for Cursor stop", () => {
		expect(isStopEvent("stop")).toBe(true);
	});

	it("returns false for PreToolUse", () => {
		expect(isStopEvent("PreToolUse")).toBe(false);
	});

	it("returns false for empty string", () => {
		expect(isStopEvent("")).toBe(false);
	});
});

// ---------------------------------------------------------------------------
// extractShellCommand()
// ---------------------------------------------------------------------------

describe("extractShellCommand()", () => {
	it("extracts command string from tool input", () => {
		expect(extractShellCommand({ command: "npm test" })).toBe("npm test");
	});

	it("returns undefined when command is not a string", () => {
		expect(extractShellCommand({ command: 42 })).toBeUndefined();
	});

	it("returns undefined when command is missing", () => {
		expect(extractShellCommand({})).toBeUndefined();
	});

	it("returns undefined for undefined input", () => {
		expect(extractShellCommand(undefined)).toBeUndefined();
	});
});

// ---------------------------------------------------------------------------
// mapHookInputToEvent() — VS Code Copilot field normalization
// ---------------------------------------------------------------------------

describe("mapHookInputToEvent()", () => {
	it("maps snake_case hook input (Claude Code canonical)", () => {
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

	it("handles missing fields gracefully", () => {
		const ev = mapHookInputToEvent({});
		expect(ev.eventName).toBe("");
		expect(ev.toolName).toBeUndefined();
		expect(ev.toolInput).toBeUndefined();
		expect(ev.cwd).toBeUndefined();
	});

	it("falls back to camelCase toolInput when snake_case absent", () => {
		const input = {
			hookEventName: "PreToolUse",
			toolName: "Bash",
			toolInput: { command: "ls" },
			cwd: "/workspace",
		};
		const ev = mapHookInputToEvent(input);
		expect(ev.toolInput).toEqual({ command: "ls" });
	});
});

// ---------------------------------------------------------------------------
// extractSessionId() — VS Code Copilot camelCase session ID
// ---------------------------------------------------------------------------

describe("extractSessionId()", () => {
	it("extracts snake_case session_id (Claude Code canonical)", () => {
		expect(extractSessionId({ session_id: "abc-123" })).toBe("abc-123");
	});

	it("extracts camelCase sessionId (VS Code Copilot)", () => {
		expect(extractSessionId({ sessionId: "def-456" })).toBe("def-456");
	});

	it("prefers snake_case over camelCase when both present", () => {
		expect(extractSessionId({ session_id: "snake", sessionId: "camel" })).toBe("snake");
	});

	it("returns undefined when neither field is present", () => {
		expect(extractSessionId({})).toBeUndefined();
	});

	it("returns undefined when session_id is not a string", () => {
		expect(extractSessionId({ session_id: 42 })).toBeUndefined();
	});

	it("falls back to camelCase when snake_case is non-string", () => {
		expect(extractSessionId({ session_id: null, sessionId: "fallback" })).toBe("fallback");
	});

	it("extracts conversation_id (Cursor)", () => {
		expect(extractSessionId({ conversation_id: "d15be335-98f9-4b9e" })).toBe("d15be335-98f9-4b9e");
	});

	it("prefers session_id and sessionId over conversation_id", () => {
		expect(extractSessionId({ session_id: "claude", conversation_id: "cursor" })).toBe("claude");
		expect(extractSessionId({ sessionId: "copilot", conversation_id: "cursor" })).toBe("copilot");
	});

	it("falls back to conversation_id when session_id/sessionId are non-string", () => {
		expect(
			extractSessionId({ session_id: null, sessionId: undefined, conversation_id: "cursor-fb" }),
		).toBe("cursor-fb");
	});
});

// ---------------------------------------------------------------------------
// isStopHookActive() — VS Code Copilot camelCase stopHookActive
// ---------------------------------------------------------------------------

describe("isStopHookActive()", () => {
	it("returns true for snake_case stop_hook_active (Claude Code)", () => {
		expect(isStopHookActive({ stop_hook_active: true })).toBe(true);
	});

	it("returns true for camelCase stopHookActive (VS Code Copilot)", () => {
		expect(isStopHookActive({ stopHookActive: true })).toBe(true);
	});

	it("returns false when stop_hook_active is false", () => {
		expect(isStopHookActive({ stop_hook_active: false })).toBe(false);
	});

	it("returns false when neither field is present", () => {
		expect(isStopHookActive({})).toBe(false);
	});

	it("prefers snake_case over camelCase when both present", () => {
		expect(isStopHookActive({ stop_hook_active: false, stopHookActive: true })).toBe(false);
	});

	it("returns false for non-boolean truthy values", () => {
		expect(isStopHookActive({ stop_hook_active: 1 })).toBe(false);
	});
});
