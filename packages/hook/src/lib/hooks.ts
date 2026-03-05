/**
 * Hook I/O — low-level stdin JSON parsing.
 *
 * This module provides the raw `HookInput` type and `readHookInput()`
 * for reading Claude Code hook event JSON from stdin. Higher-level
 * abstractions live in `adapter.ts` (strategy pattern) and `check.ts`.
 *
 * Exit-code signaling (`allow` / `block`) is handled by HookAdapter.
 */

/**
 * Common fields present on hook event input.
 *
 * Claude Code uses snake_case (`hook_event_name`, `session_id`).
 * VS Code Copilot uses camelCase for some fields (`hookEventName`, `sessionId`)
 * while keeping others in snake_case (`tool_name`, `tool_input`, `cwd`).
 *
 * Both variants are declared here so that `readHookInput()` returns a
 * correctly-typed object regardless of the provider.
 */
export type HookInput = {
	// snake_case (Claude Code canonical)
	session_id?: string;
	transcript_path?: string;
	cwd?: string;
	permission_mode?: string;
	hook_event_name?: string;
	tool_name?: string;
	tool_input?: Record<string, unknown>;
	tool_use_id?: string;
	stop_hook_active?: boolean;
	// camelCase variants (VS Code Copilot)
	hookEventName?: string;
	sessionId?: string;
	toolName?: string;
	toolInput?: Record<string, unknown>;
	toolUseId?: string;
	stopHookActive?: boolean;
	// Cursor-specific fields
	conversation_id?: string;
	generation_id?: string;
	cursor_version?: string;
	workspace_roots?: string[];
	[key: string]: unknown;
};

/** Read and parse event input from stdin. Returns empty object on failure. */
export async function readHookInput(): Promise<HookInput> {
	try {
		const chunks: Uint8Array[] = [];
		const reader = Bun.stdin.stream().getReader();
		while (true) {
			const { done, value } = await reader.read();
			if (done) break;
			chunks.push(value);
		}
		const text = Buffer.concat(chunks).toString("utf-8").trim();
		if (!text) return {};
		return JSON.parse(text) as HookInput;
	} catch {
		return {};
	}
}
