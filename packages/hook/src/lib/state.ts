/**
 * Per-project state management — event-namespaced.
 *
 * State is a nested JSON object persisted alongside sentinels that enables
 * cross-event data sharing. Structure:
 * `{ "UserPromptSubmit": { "prompt": "...", ... }, "Stop": { ... } }`
 */

import { createHash } from "node:crypto";
import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { ensureSentinelDir } from "./sentinel";

/** The top-level state shape: event name → event data. */
export type State = Record<string, Record<string, unknown>>;

/** Compute a deterministic state file name for a project. */
function stateFileName(projectDir: string): string {
	const hash = createHash("sha256").update(projectDir).digest("hex").slice(0, 16);
	return `state-${hash}.json`;
}

/** Full path to the state file for a project. */
export function statePath(sentinelDir: string, projectDir: string): string {
	return join(sentinelDir, stateFileName(projectDir));
}

/**
 * Read the entire state object for a project.
 * Returns an empty object if the file is missing or malformed.
 */
export function readState(sentinelDir: string, projectDir: string): State {
	const path = statePath(sentinelDir, projectDir);
	if (!existsSync(path)) return {};
	try {
		const content = readFileSync(path, "utf-8");
		const parsed = JSON.parse(content);
		if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) {
			return {};
		}
		return parsed as State;
	} catch {
		return {};
	}
}

/**
 * Save event input under its event name.
 * Saving the same event again overwrites that event's data entirely.
 */
export function saveEvent(
	sentinelDir: string,
	projectDir: string,
	eventName: string,
	data: Record<string, unknown>,
): void {
	ensureSentinelDir(sentinelDir);
	const existing = readState(sentinelDir, projectDir);
	existing[eventName] = data;
	const path = statePath(sentinelDir, projectDir);
	writeFileSync(path, `${JSON.stringify(existing, null, 2)}\n`, "utf-8");
}

/**
 * Load a field from state using dot notation.
 * Supports `EventName.field` and deeper paths.
 */
export function loadField(sentinelDir: string, projectDir: string, field: string): unknown {
	const state = readState(sentinelDir, projectDir);
	return resolveFieldPath(state, field);
}

/** Remove the state file (cleanup). */
export function clearState(sentinelDir: string, projectDir: string): void {
	const path = statePath(sentinelDir, projectDir);
	if (existsSync(path)) rmSync(path);
}

/**
 * Resolve a dot-separated field path against the state object.
 * Example: `"UserPromptSubmit.prompt"` → `state.UserPromptSubmit.prompt`
 */
export function resolveFieldPath(state: State, path: string): unknown {
	const parts = path.split(".");
	let current: unknown = state;
	for (const part of parts) {
		if (current === null || current === undefined || typeof current !== "object") {
			return undefined;
		}
		current = (current as Record<string, unknown>)[part];
	}
	return current;
}
