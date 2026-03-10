/**
 * Environment variable handling for the chunk hook CLI.
 *
 * All env vars use the `CHUNK_HOOK_` prefix. Per-command toggles override the
 * global enable flag. Env values always take precedence over YAML config.
 */

import { isAbsolute, join } from "node:path";

/** Command name — any user-defined string (e.g., "tests", "lint", "review"). */
export type CommandName = string;

/** Subcommand for exec and task commands. */
export type Subcommand = "check" | "run";

/** Read a string env var, returning `fallback` when unset or empty. */
export function env(name: string, fallback?: string): string | undefined {
	const val = process.env[name];
	return val !== undefined && val !== "" ? val : fallback;
}

/** Read a boolean env var (`1`, `true`, `yes` → true). */
export function envBool(name: string): boolean | undefined {
	const val = env(name);
	if (val === undefined) return undefined;
	return ["1", "true", "yes"].includes(val.toLowerCase());
}

/** Check whether the toolkit is globally enabled via `CHUNK_HOOK_ENABLE`. */
export function isGloballyEnabled(): boolean {
	return envBool("CHUNK_HOOK_ENABLE") === true;
}

/**
 * Check whether a specific command is enabled.
 *
 * Resolution order:
 *   1. `CHUNK_HOOK_ENABLE_{NAME}` (per-command override)
 *   2. `CHUNK_HOOK_ENABLE` (global toggle)
 *   3. Falls back to `false` (disabled by default)
 */
export function isEnabled(name: CommandName): boolean {
	const perCommand = envBool(`CHUNK_HOOK_ENABLE_${name.toUpperCase()}`);
	if (perCommand !== undefined) return perCommand;
	return isGloballyEnabled();
}

/** Read per-command timeout override in seconds. */
export function getEnvTimeout(name: CommandName): number | undefined {
	const val = env(`CHUNK_HOOK_TIMEOUT_${name.toUpperCase()}`);
	if (val === undefined) return undefined;
	const n = Number(val);
	return Number.isFinite(n) && n > 0 ? n : undefined;
}

/** Read sentinel directory override. */
export function getEnvSentinelDir(): string | undefined {
	return env("CHUNK_HOOK_SENTINELS_DIR");
}

/** Read config file path override. */
export function getEnvConfigPath(): string | undefined {
	return env("CHUNK_HOOK_CONFIG");
}

/** Read the project directory (set by Claude Code or fallback to cwd). */
export function getProjectDir(): string {
	return env("CLAUDE_PROJECT_DIR") ?? process.cwd();
}

/** Read the project root directory from CHUNK_HOOK_PROJECT_ROOT. */
export function getWorkspaceRoot(): string | undefined {
	return env("CHUNK_HOOK_PROJECT_ROOT");
}

/**
 * Resolve the `--project` flag value to an absolute project directory.
 *
 * If the value is an absolute path, it is returned as-is.
 * If relative, it is joined with `CHUNK_HOOK_PROJECT_ROOT`.
 * Falls back to the event's cwd (or process.cwd()) when no flag is given.
 */
export function resolveProject(flagValue: string | undefined, eventCwd?: string): string {
	if (!flagValue) {
		return eventCwd ?? getProjectDir();
	}

	if (isAbsolute(flagValue)) {
		return flagValue;
	}

	const root = getWorkspaceRoot();
	if (root) {
		return join(root, flagValue);
	}

	// No workspace root — resolve relative to eventCwd or process.cwd()
	return join(eventCwd ?? process.cwd(), flagValue);
}

const DEFAULT_MARKER_TTL_MS = 5 * 60 * 1000;

/** Read marker TTL override in milliseconds. Default: 5 minutes. 0 disables session protection. */
export function getMarkerTtlMs(): number {
	const val = env("CHUNK_HOOK_MARKER_TTL_MS");
	if (val === undefined) return DEFAULT_MARKER_TTL_MS;
	const n = Number(val);
	return Number.isFinite(n) && n >= 0 ? n : DEFAULT_MARKER_TTL_MS;
}
