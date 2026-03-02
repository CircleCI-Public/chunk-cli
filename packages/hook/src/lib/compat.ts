/**
 * Provider compatibility — normalization maps and helpers for multi-IDE support.
 *
 * Claude Code is the canonical provider. Other IDEs (Cursor, VS Code Copilot)
 * read `.claude/settings.json` and translate event/tool names into their own
 * equivalents. This module centralizes the reverse mappings so the adapter
 * can normalize provider-specific values back to canonical Claude Code names.
 */

import type { AgentEvent } from "./adapter";
import type { HookInput } from "./hooks";

// ---------------------------------------------------------------------------
// Event name normalization
// ---------------------------------------------------------------------------

/**
 * Canonical (Claude Code PascalCase) → provider-specific event name mappings.
 *
 * Cursor sends camelCase event names:
 *   PreToolUse → preToolUse, PostToolUse → postToolUse,
 *   UserPromptSubmit → beforeSubmitPrompt, Stop → stop,
 *   SessionStart → sessionStart, SessionEnd → sessionEnd,
 *   NotificationShown → notificationShown
 *
 * VS Code Copilot sends the canonical PascalCase names but uses camelCase
 * for some input *fields* (handled separately in hooks.ts / mapHookInputToEvent).
 *
 * This map is keyed by lowercase for O(1) lookup. Values are the canonical
 * Claude Code PascalCase names.
 */
const EVENT_NAME_CANONICAL: Record<string, string> = {
	// Claude Code canonical (lowercase key → PascalCase value)
	pretooluse: "PreToolUse",
	posttooluse: "PostToolUse",
	userpromptsubmit: "UserPromptSubmit",
	stop: "Stop",
	sessionstart: "SessionStart",
	sessionend: "SessionEnd",
	notificationshown: "NotificationShown",

	// Cursor-specific aliases (Cursor renames UserPromptSubmit)
	beforesubmitprompt: "UserPromptSubmit",
};

/**
 * Normalize an event name to the canonical Claude Code PascalCase form.
 *
 * Handles:
 * - **Cursor:** camelCase event names
 * - **VS Code Copilot:** already PascalCase — passes through unchanged.
 * - **Unknown events:** returned verbatim (no lossy transformation).
 */
export function normalizeEventName(name: string): string {
	if (!name) return name;
	return EVENT_NAME_CANONICAL[name.toLowerCase()] ?? name;
}

// ---------------------------------------------------------------------------
// Tool name normalization
// ---------------------------------------------------------------------------

/** Tool names that represent shell/terminal execution. */
const SHELL_TOOL_NAMES = new Set([
	"Bash", // Claude Code canonical
	"Shell", // Cursor equivalent
	"run_in_terminal", // VS Code Copilot equivalent
]);

/**
 * Check whether a tool name represents a shell/terminal tool.
 *
 * Handles Claude Code (`Bash`), Cursor (`Shell`), VS Code Copilot (`run_in_terminal`).
 */
export function isShellTool(toolName: string | undefined): boolean {
	return !!toolName && SHELL_TOOL_NAMES.has(toolName);
}

// ---------------------------------------------------------------------------
// Pre-tool-use event detection
// ---------------------------------------------------------------------------

/**
 * Check whether an event name represents a pre-tool-use event.
 * Case-insensitive to handle all providers.
 */
export function isPreToolUseEvent(eventName: string): boolean {
	return eventName.toLowerCase() === "pretooluse";
}

// ---------------------------------------------------------------------------
// Stop event detection
// ---------------------------------------------------------------------------

/**
 * Check whether an event name represents a stop event.
 * Case-insensitive to handle all providers.
 */
export function isStopEvent(eventName: string): boolean {
	return eventName.toLowerCase() === "stop";
}

// ---------------------------------------------------------------------------
// Shell command extraction
// ---------------------------------------------------------------------------

/**
 * Extract the shell command string from tool input.
 * All current providers use `tool_input.command`.
 */
export function extractShellCommand(
	toolInput: Record<string, unknown> | undefined,
): string | undefined {
	const cmd = toolInput?.command;
	return typeof cmd === "string" ? cmd : undefined;
}

// ---------------------------------------------------------------------------
// Hook input field normalization (VS Code Copilot camelCase → canonical)
// ---------------------------------------------------------------------------

/**
 * Convert raw hook input to a normalized AgentEvent.
 *
 * VS Code Copilot uses camelCase for some fields (`hookEventName`, `sessionId`)
 * while keeping others in snake_case. This function normalizes both variants.
 */
export function mapHookInputToEvent(input: HookInput): AgentEvent {
	return {
		eventName: (input.hook_event_name ?? input.hookEventName ?? "") as string,
		toolName: (input.tool_name ?? input.toolName) as string | undefined,
		toolInput: (input.tool_input ?? input.toolInput) as Record<string, unknown> | undefined,
		cwd: input.cwd as string | undefined,
		raw: input,
	};
}

/**
 * Extract the session ID from a raw hook payload.
 * Normalizes both snake_case and camelCase variants.
 */
export function extractSessionId(raw: Record<string, unknown>): string | undefined {
	return (
		(typeof raw.session_id === "string" ? raw.session_id : undefined) ??
		(typeof raw.sessionId === "string" ? (raw.sessionId as string) : undefined)
	);
}

/**
 * Check whether the stop-hook-active flag is set in a raw hook payload.
 * Normalizes both snake_case and camelCase variants.
 */
export function isStopHookActive(raw: Record<string, unknown>): boolean {
	const active = raw.stop_hook_active ?? raw.stopHookActive;
	return active === true;
}
