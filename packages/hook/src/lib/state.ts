/**
 * Per-project state management — event-namespaced.
 *
 * State is a nested JSON object persisted alongside sentinels that enables
 * cross-event data sharing. Each event stores an `__entries` array:
 * ```
 * {
 *   "UserPromptSubmit": { "__entries": [{ "prompt": "...", "head": "abc123" }, ...] },
 *   "Stop": { "__entries": [{ ... }] }
 * }
 * ```
 *
 * Templating uses array-index access: `{{UserPromptSubmit[0].prompt}}` for
 * the first entry. Plain dot access `{{UserPromptSubmit.prompt}}` is sugar
 * for `{{UserPromptSubmit[0].prompt}}` (first entry).
 */

import { createHash } from "node:crypto";
import { existsSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import { ensureSentinelDir } from "./sentinel";

/** The top-level state shape: event name → event data with __entries. */
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

/** Write the full state object to disk. */
function writeState(sentinelDir: string, projectDir: string, state: State): void {
	ensureSentinelDir(sentinelDir);
	const path = statePath(sentinelDir, projectDir);
	writeFileSync(path, `${JSON.stringify(state, null, 2)}\n`, "utf-8");
}

/**
 * Return existing state if it belongs to the given session, otherwise an
 * empty state. When `sessionId` is provided and differs from the stored
 * `__sessionId`, the old state is discarded (overwrite model).
 */
/** Reserved key for the session metadata entry in state. */
const SESSION_KEY = "__session";

function sessionAwareState(sentinelDir: string, projectDir: string, sessionId?: string): State {
	const existing = readState(sentinelDir, projectDir);
	if (sessionId && existing[SESSION_KEY]?.id !== sessionId) {
		// Different session (or no session stored) — start fresh.
		return {};
	}
	return existing;
}

/**
 * Save event input under its event name (clear + single-entry append).
 *
 * Replaces all existing entries for the event with a single entry
 * containing `data`. The resulting shape is `{ __entries: [data] }` —
 * consistent with `appendEvent` so all events have the same structure.
 *
 * When `sessionId` is provided and differs from the stored session, the
 * entire state is overwritten (session-aware overwrite model).
 */
export function saveEvent(
	sentinelDir: string,
	projectDir: string,
	eventName: string,
	data: Record<string, unknown>,
	sessionId?: string,
): void {
	const existing = sessionAwareState(sentinelDir, projectDir, sessionId);
	if (sessionId) existing[SESSION_KEY] = { id: sessionId };
	existing[eventName] = { __entries: [data] };
	writeState(sentinelDir, projectDir, existing);
}

/**
 * Append event input under its event name, preserving previous entries.
 *
 * Each call pushes a new entry onto the `__entries` array. Consecutive
 * duplicate entries (same `prompt` value) are deduplicated.
 *
 * When `sessionId` is provided and differs from the stored session, the
 * entire state is overwritten (session-aware overwrite model).
 */
export function appendEvent(
	sentinelDir: string,
	projectDir: string,
	eventName: string,
	data: Record<string, unknown>,
	sessionId?: string,
): void {
	const existing = sessionAwareState(sentinelDir, projectDir, sessionId);
	if (sessionId) existing[SESSION_KEY] = { id: sessionId };
	const prev = existing[eventName] ?? {};
	const entries = Array.isArray(prev.__entries)
		? (prev.__entries as Record<string, unknown>[])
		: [];

	// Deduplicate: skip if the latest entry has the same prompt value.
	if (entries.length > 0) {
		const last = entries[entries.length - 1];
		if (last?.prompt === data.prompt) return;
	}

	entries.push(data);

	existing[eventName] = { __entries: entries };
	writeState(sentinelDir, projectDir, existing);
}

/**
 * Read the baseline fingerprint from the first entry for an event.
 *
 * Returns the `fingerprint` field of the first `__entries` element, or
 * undefined if no entries exist or the first entry has no `fingerprint`.
 * The fingerprint is a composite hash of HEAD + working tree diff,
 * capturing the full repo state at the time of the first save/append.
 */
export function getBaselineFingerprint(
	sentinelDir: string,
	projectDir: string,
	eventName: string,
): string | undefined {
	const state = readState(sentinelDir, projectDir);
	const event = state[eventName];
	if (!event) return undefined;
	const entries = Array.isArray(event.__entries)
		? (event.__entries as Record<string, unknown>[])
		: [];
	const first = entries[0];
	if (!first) return undefined;
	const fp = first.fingerprint;
	return typeof fp === "string" && fp.length > 0 ? fp : undefined;
}

/**
 * Load a field from state using dot/bracket notation.
 * Supports `EventName.field`, `EventName[0].field`, and deeper paths.
 */
export function loadField(sentinelDir: string, projectDir: string, field: string): unknown {
	const state = readState(sentinelDir, projectDir);
	return resolveFieldPath(state, field);
}

/**
 * Remove the state file (cleanup).
 *
 * When `sessionId` is provided, the state is only cleared if it belongs
 * to the given session. This prevents one session from clearing another
 * session's state. Without a `sessionId` the clear is unconditional.
 */
export function clearState(sentinelDir: string, projectDir: string, sessionId?: string): void {
	if (sessionId) {
		const existing = readState(sentinelDir, projectDir);
		if (existing[SESSION_KEY]?.id && existing[SESSION_KEY].id !== sessionId) {
			return; // Different session — skip (best effort).
		}
	}
	const path = statePath(sentinelDir, projectDir);
	if (existsSync(path)) rmSync(path);
}

/**
 * Resolve a field path against the state object.
 *
 * Supports dot notation and bracket-index notation:
 *   - `"UserPromptSubmit[0].prompt"` → `state.UserPromptSubmit.__entries[0].prompt`
 *   - `"UserPromptSubmit.prompt"`    → sugar for `UserPromptSubmit[0].prompt`
 *
 * When the first step after the event name lands on an object with an
 * `__entries` array and the next segment is NOT `__entries`, the path
 * is transparently redirected through `__entries[0]` (syntactic sugar).
 */
export function resolveFieldPath(state: State, path: string): unknown {
	const segments = parsePath(path);
	let current: unknown = state;
	for (const seg of segments) {
		if (current === null || current === undefined || typeof current !== "object") {
			return undefined;
		}
		if (typeof seg === "number") {
			// Array index access — also handles __entries sugar for bracket notation
			if (Array.isArray(current)) {
				current = current[seg];
			} else {
				const obj = current as Record<string, unknown>;
				if (Array.isArray(obj.__entries)) {
					current = (obj.__entries as unknown[])[seg];
				} else {
					return undefined;
				}
			}
		} else {
			const obj = current as Record<string, unknown>;
			// Syntactic sugar: if the object has __entries and the key
			// isn't "__entries" itself, redirect through __entries[0].
			if (
				Array.isArray(obj.__entries) &&
				seg !== "__entries" &&
				!(seg in obj && seg !== "__entries")
			) {
				const first = (obj.__entries as unknown[])[0];
				if (first === null || first === undefined || typeof first !== "object") {
					return undefined;
				}
				current = (first as Record<string, unknown>)[seg];
			} else {
				current = obj[seg];
			}
		}
	}
	return current;
}

/**
 * Parse a field path into segments.
 * `"Event[0].field.sub"` → `["Event", 0, "field", "sub"]`
 * `"Event.field"` → `["Event", "field"]`
 */
function parsePath(path: string): (string | number)[] {
	const segments: (string | number)[] = [];
	let i = 0;
	while (i < path.length) {
		if (path[i] === "[") {
			// Parse bracket index
			const close = path.indexOf("]", i);
			if (close === -1) break;
			const idx = Number.parseInt(path.slice(i + 1, close), 10);
			if (!Number.isNaN(idx)) segments.push(idx);
			i = close + 1;
			if (i < path.length && path[i] === ".") i++; // skip trailing dot
		} else if (path[i] === ".") {
			i++; // skip leading dot
		} else {
			// Parse key segment until . or [
			let end = i;
			while (end < path.length && path[end] !== "." && path[end] !== "[") end++;
			segments.push(path.slice(i, end));
			i = end;
		}
	}
	return segments;
}
