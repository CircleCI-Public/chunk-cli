/**
 * `chunk hook scope` — per-repo activity gate for multi-repo workspaces.
 *
 * In VS Code multi-root workspaces, Claude Code merges all
 * `.claude/settings.json` files so hooks fire for every repo — even repos
 * the agent hasn't touched. The scope command prevents expensive hooks
 * (tests, lint) from running in inactive repos.
 *
 * **Auto-activate:** The `exec` and `task` handlers call
 * `activateScope()` automatically before checking the gate — if the
 * stdin payload references the current project AND contains a session ID,
 * the scope is activated as a side effect and the function returns `true`.
 * This means no separate `scope activate` hook entry is needed in the
 * default template.
 *
 * **Activation requires context:** The marker is only written when the
 * raw payload contains both file paths matching the project and a session
 * ID. Agent-invoked commands (`exec run --no-check`) and direct CLI
 * invocations without stdin context do not activate the scope. The `exec`
 * handler skips the scope gate entirely for `--no-check` since those run
 * in the target repo via `process.cwd()`.
 *
 * **Session binding:** The marker file stores the session ID from the
 * hook payload. When a session ID is available in a subsequent event, it
 * is compared to the stored one — a mismatch means the marker belongs to
 * a different session (possibly a parallel agent) and is treated as
 * inactive. When no session ID is present, session validation is skipped
 * and only file existence is checked.
 *
 * **Subagent safety:** When a marker already exists and a different
 * session ID attempts to activate the same project (e.g. a subagent
 * spawned by the parent), the existing marker is preserved — as long as
 * it has not expired. This prevents scope gaps when control returns to
 * the parent.
 *
 * **TTL-based expiry:** The marker timestamp is refreshed on every
 * `activateScope()` call that returns `true` for the same session.
 * A different session can reclaim an expired marker (>5 min) from a dead
 * session where `SessionEnd` never fired. Same-session calls always
 * bypass TTL — pauses of any length are safe.
 *
 * Subcommands:
 *   activate   — Read stdin JSON, activate scope if file paths reference
 *                the project directory and a session ID is present.
 *                Available for explicit use when no exec/task hook is
 *                present in a hook group.
 *   deactivate — Remove the scope marker file.
 *
 * Usage in hooks:
 *   SessionStart/SessionEnd: `chunk hook scope deactivate`
 *   (Optional) standalone:   `chunk hook scope activate`
 *
 * The marker lives at `.chunk/hook/.chunk-hook-active`.
 * In single-repo workspaces every tool call matches, so the scope is
 * always active — no behavior change.
 */

import { existsSync, mkdirSync, readFileSync, unlinkSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";

import { getMarkerTtlMs } from "../lib/env";
import { log, logVerbose } from "../lib/log";

const TAG = "scope";

/** Scope marker file path relative to project root. */
export const MARKER_REL = join(".chunk", "hook", ".chunk-hook-active");

/**
 * How long a marker remains valid without being refreshed.
 *
 * Same-session calls bypass TTL (the owning session can always reclaim).
 * TTL only gates different-session reclaim: when a new session encounters
 * an expired marker from a dead session (e.g. VS Code closed without
 * firing SessionEnd), it can overwrite it.
 *
 * 5 minutes: covers all observed active-work gaps (max 125 s in
 * experiment data) with margin, while keeping dead-session wait short.
 *
 * Override with `CHUNK_HOOK_MARKER_TTL_MS` (milliseconds).
 */
export const MARKER_TTL_MS = getMarkerTtlMs();

/** Check whether a marker has exceeded the TTL. */
function isExpired(marker: MarkerContent, now: number = Date.now()): boolean {
	return now - marker.timestamp > MARKER_TTL_MS;
}

// ---------------------------------------------------------------------------
// Path extraction from raw stdin JSON
// ---------------------------------------------------------------------------

/** Keys in tool_input that commonly hold absolute file paths. */
const PATH_KEYS = ["file_path", "filePath", "path", "file", "directory", "dir", "command"] as const;

/**
 * Extract absolute file paths from the raw stdin JSON payload.
 *
 * Looks in `tool_input` for common path-bearing keys. For the `command`
 * key, extracts the first absolute-path token (stopping at shell
 * metacharacters like `;`, `|`, `>`, `&`, `)`).
 */
export function extractFilePaths(raw: Record<string, unknown>): string[] {
	const toolInput = raw.tool_input;
	if (!toolInput || typeof toolInput !== "object") return [];

	const input = toolInput as Record<string, unknown>;
	const paths: string[] = [];

	for (const key of PATH_KEYS) {
		const val = input[key];
		if (typeof val !== "string") continue;

		if (key === "command") {
			// Extract the first absolute-path token, stopping at shell operators
			const match = val.match(/\/[^\s;|>&)]+/);
			if (match) paths.push(match[0]);
		} else if (val.startsWith("/")) {
			paths.push(val);
		}
	}

	return paths;
}

/**
 * Check if any extracted file paths reference the given project directory.
 *
 * Returns `"match"` when at least one path starts with `projectDir/`.
 * Returns `"no-paths"` when no file paths were extracted (e.g. Stop,
 * SessionStart events) — callers should check existing markers instead of
 * treating this as a match.
 * Returns `"mismatch"` when paths were found but none reference the project.
 */
export function matchesProject(
	projectDir: string,
	raw: Record<string, unknown>,
): "match" | "no-paths" | "mismatch" {
	const paths = extractFilePaths(raw);
	if (paths.length === 0) return "no-paths";
	const hit = paths.some((p) => p.startsWith(`${projectDir}/`) || p === projectDir);
	return hit ? "match" : "mismatch";
}

// ---------------------------------------------------------------------------
// Marker file I/O
// ---------------------------------------------------------------------------

/** Content stored in the marker file. */
export type MarkerContent = {
	sessionId: string;
	timestamp: number;
};

/**
 * Write the marker file with session ID and timestamp.
 *
 * Throws on fatal I/O errors (caller decides how to handle).
 */
function writeMarker(projectDir: string, sessionId: string): void {
	const markerPath = join(projectDir, MARKER_REL);
	const markerDir = dirname(markerPath);

	if (!existsSync(markerDir)) {
		mkdirSync(markerDir, { recursive: true });
	}

	const content: MarkerContent = { sessionId, timestamp: Date.now() };
	writeFileSync(markerPath, `${JSON.stringify(content)}\n`);
}

/** Read the marker file. Returns undefined if absent or malformed. */
export function readMarker(projectDir: string): MarkerContent | undefined {
	const markerPath = join(projectDir, MARKER_REL);
	try {
		const text = readFileSync(markerPath, "utf-8").trim();
		const parsed = JSON.parse(text) as MarkerContent;
		if (typeof parsed.sessionId === "string" && typeof parsed.timestamp === "number") {
			return parsed;
		}
		return undefined;
	} catch {
		return undefined;
	}
}

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Activate scope for the given project if the stdin payload references it.
 *
 * Returns `true` if the scope is active after this call (either freshly
 * activated or already active from a previous event in the same session).
 * Returns `false` if the scope is not active (paths target a different
 * repo and no prior activation exists for the current session).
 *
 * **Activation requires explicit path match:** The marker is only written
 * when `raw` contains file paths that match the project AND a `sessionId`
 * is provided. Events without file paths (e.g. Stop, SessionStart) never
 * create new markers — they only check existing ones. This prevents every
 * repo from auto-activating on events that carry no file context.
 *
 * **Subagent-safe:** If a marker already exists with a different session
 * ID, the existing marker is preserved (not overwritten). This prevents
 * subagents from clobbering the parent session's scope.
 *
 * @param projectDir - Absolute project root.
 * @param raw - Full stdin JSON (parsed by caller).
 * @param sessionId - Session ID from the hook payload (optional).
 * @returns Whether the scope is active after this call.
 */
export function activateScope(
	projectDir: string,
	raw: Record<string, unknown>,
	sessionId?: string,
): boolean {
	const match = matchesProject(projectDir, raw);
	const toolName = raw.tool_name ?? raw.toolName;
	log(
		TAG,
		`activate: tool=${String(toolName ?? "(none)")} match=${match} session=${sessionId ? sessionId.slice(0, 8) : "none"}`,
	);
	logVerbose(TAG, `raw payload: ${JSON.stringify(raw)}`);

	// Activation: write the marker when we have a session ID and file paths
	// that reference this project.  "no-paths" and "mismatch" events never
	// auto-activate — only an explicit path match does.
	const shouldActivate = sessionId && match === "match";

	if (shouldActivate) {
		// Subagent safety: if a marker already exists with a different session
		// ID, preserve it — unless the marker has expired (dead session).
		const existing = readMarker(projectDir);
		if (existing && existing.sessionId !== sessionId) {
			if (!isExpired(existing)) {
				log(TAG, `already active for ${projectDir} (owned by ${existing.sessionId}, keeping)`);
				return true;
			}
			log(TAG, `expired marker from ${existing.sessionId}, reclaiming for ${sessionId}`);
		}
		writeMarker(projectDir, sessionId);
		log(TAG, `activated ${join(projectDir, MARKER_REL)}`);
		return true;
	}

	// Fallback: check if a prior marker exists.
	const existing = readMarker(projectDir);
	if (!existing) {
		if (match === "mismatch") {
			log(TAG, `paths do not reference ${projectDir}, not active`);
		} else {
			log(TAG, `not active for ${projectDir}`);
		}
		return false;
	}

	// If we have a session ID, validate it matches the stored one.
	if (sessionId && existing.sessionId && sessionId !== existing.sessionId) {
		if (!isExpired(existing)) {
			log(
				TAG,
				`valid marker from session ${existing.sessionId} (current ${sessionId}), not active`,
			);
			return false;
		}
		// Expired marker from a dead session — reclaim it.
		writeMarker(projectDir, sessionId);
		log(TAG, `expired marker from ${existing.sessionId}, reclaimed for ${sessionId}`);
		return true;
	}

	// Same session (or no session to compare) — refresh the timestamp
	// so the marker stays live for TTL purposes.
	if (sessionId && existing.sessionId === sessionId) {
		writeMarker(projectDir, sessionId);
	}
	log(TAG, `already active for ${projectDir} (from prior event)`);
	return true;
}

/**
 * Remove the scope marker file.
 *
 * Session-aware: when `sessionId` is provided, only removes the marker
 * if it belongs to the same session. This prevents an agent from
 * deactivating a scope that belongs to a different session (e.g. a
 * parallel agent or parent session).
 *
 * @param projectDir - Absolute project root.
 * @param sessionId  - Current session ID. When provided, validates ownership.
 */
export function deactivateScope(projectDir: string, sessionId?: string): void {
	const markerPath = join(projectDir, MARKER_REL);

	if (sessionId) {
		const marker = readMarker(projectDir);
		if (marker && marker.sessionId !== sessionId) {
			log(
				TAG,
				`deactivate skipped: marker owned by session ${marker.sessionId}, ` +
					`current session is ${sessionId}`,
			);
			return;
		}
	}

	try {
		unlinkSync(markerPath);
		log(TAG, `deactivated ${markerPath}`);
	} catch {
		log(TAG, `deactivate (no marker) ${markerPath}`);
	}
}
