/**
 * Hook adapter — the abstraction seam between provider-specific hook I/O
 * and agent-agnostic core logic.
 *
 * The adapter owns all provider-specific semantics: event names, tool names,
 * env vars, and behavioral queries. Core logic (exec, task, state, check)
 * calls adapter methods — it never inspects event/tool names directly.
 *
 * Currently only a Claude Code adapter is implemented. When a second provider
 * is added, implement `HookAdapter` for it and update `getAdapter()`.
 */

import {
	extractShellCommand,
	isPreToolUseEvent,
	isShellTool,
	isStopEvent,
	isStopHookActive,
	mapHookInputToEvent,
	normalizeEventName,
} from "./compat";
import { readHookInput } from "./hooks";

// Re-export mapHookInputToEvent so existing consumers (tests, etc.) can import
// from adapter.ts without changing their imports.
export { mapHookInputToEvent };

// ---------------------------------------------------------------------------
// AgentEvent — normalized event shape
// ---------------------------------------------------------------------------

/** Normalized event from any hook provider. */
export type AgentEvent = {
	/** Verbatim event name from the framework (e.g. "PreToolUse", "preToolUse"). */
	eventName: string;
	/** Tool being invoked, if applicable. */
	toolName?: string;
	/** Tool input/arguments. */
	toolInput?: Record<string, unknown>;
	/** Working directory. */
	cwd?: string;
	/** Full raw input — preserves all provider-specific fields. */
	raw: Record<string, unknown>;
};

// ---------------------------------------------------------------------------
// HookAdapter — strategy pattern interface
// ---------------------------------------------------------------------------

/**
 * The adapter owns all provider-specific semantics.
 * Core logic asks behavioral questions; it never inspects event/tool names.
 */
export type HookAdapter = {
	/** Read the incoming event from stdin (or other transport). */
	readEvent(): Promise<AgentEvent>;

	/** Signal allow — exit 0 for all current providers. */
	allow(): never;

	/** Signal block — stderr + exit 2 for all current providers. */
	block(reason: string): never;

	/** Get the project root directory. */
	getProjectDir(): string;

	// -- Behavioral queries (replace hardcoded event/tool name checks) --

	/** Is this a stop-recursion event? */
	isStopRecursion(event: AgentEvent): boolean;

	/** Is this a shell/terminal tool call? */
	isShellToolCall(event: AgentEvent): boolean;

	/** Extract the shell command string, if this is a shell tool call. */
	getShellCommand(event: AgentEvent): string | undefined;

	/** Return the state namespace key for this event. */
	stateKey(event: AgentEvent): string;

	/** Extract a short command summary for log context. */
	commandSummary(event: AgentEvent): string;
};

// ---------------------------------------------------------------------------
// Shared stdin/exit-code base
// ---------------------------------------------------------------------------

/**
 * Shared I/O implementation for providers that use the stdin JSON + exit-code
 * protocol (currently all of them: Claude, Cursor, Copilot).
 */
export function createStdinExitCodeBase(): Pick<HookAdapter, "readEvent" | "allow" | "block"> {
	return {
		async readEvent(): Promise<AgentEvent> {
			const input = await readHookInput();
			return mapHookInputToEvent(input);
		},

		allow(): never {
			process.exit(0);
		},

		block(reason: string): never {
			process.stderr.write(`${reason}\n`);
			process.exit(2);
		},
	};
}

// ---------------------------------------------------------------------------
// Claude Code adapter
// ---------------------------------------------------------------------------

/** Create a HookAdapter for Claude Code hooks. */
export function createClaudeAdapter(): HookAdapter {
	const base = createStdinExitCodeBase();

	return {
		...base,

		getProjectDir(): string {
			return process.cwd();
		},

		isStopRecursion(event: AgentEvent): boolean {
			return isStopEvent(event.eventName) && isStopHookActive(event.raw);
		},

		isShellToolCall(event: AgentEvent): boolean {
			return isPreToolUseEvent(event.eventName) && isShellTool(event.toolName);
		},

		getShellCommand(event: AgentEvent): string | undefined {
			if (!this.isShellToolCall(event)) return undefined;
			return extractShellCommand(event.toolInput);
		},

		stateKey(event: AgentEvent): string {
			return normalizeEventName(event.eventName);
		},

		commandSummary(event: AgentEvent): string {
			const cmd = this.getShellCommand(event);
			if (!cmd) return "";
			const truncated = cmd.length > 80 ? `${cmd.slice(0, 77)}...` : cmd;
			return ` command=${JSON.stringify(truncated)}`;
		},
	};
}

// ---------------------------------------------------------------------------
// Adapter selection
// ---------------------------------------------------------------------------

/**
 * Return the appropriate HookAdapter for the current environment.
 *
 * Currently always returns the Claude adapter. When a second provider is
 * added, detection logic goes here (e.g. env-var sniffing).
 */
export function getAdapter(): HookAdapter {
	return createClaudeAdapter();
}
