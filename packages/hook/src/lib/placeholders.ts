/**
 * Placeholder expansion for task instructions.
 *
 * Scans a template string for `{{KEY}}` patterns and replaces them with
 * values from the project state and git context.
 *
 * Resolution order:
 *   1. State field paths (dot notation, resolved against event-namespaced state)
 *   2. Git placeholders (CHANGED_FILES, CHANGED_PACKAGES)
 *   3. Unresolved placeholders are replaced with empty string
 */

import type { AgentEvent } from "./adapter";
import { normalizeEventName } from "./compat";
import { getChangedFiles, getChangedPackages } from "./git";
import { log } from "./log";
import type { State } from "./state";
import { resolveFieldPath } from "./state";

const TAG = "placeholders";

/** Options for placeholder expansion. */
export type ExpandOptions = {
	/** Event-namespaced state (from state.ts). */
	state: State;
	/** Project root directory (for git operations). */
	projectDir: string;
	/** Only consider staged files for git placeholders. */
	staged?: boolean;
	/** File extension filter for git placeholders. */
	fileExt?: string;
	/**
	 * The triggering event. When provided, its raw fields are overlaid
	 * in-memory under the event name so that placeholders like
	 * `{{Stop.transcript_path}}` resolve without an explicit `state save`.
	 */
	event?: AgentEvent;
};

/**
 * Expand all `{{KEY}}` placeholders in a template string.
 *
 * State field paths are resolved first (synchronous), then git
 * placeholders are resolved lazily (only if referenced). Unresolved
 * placeholders are replaced with empty string.
 */
export async function expandPlaceholders(template: string, opts: ExpandOptions): Promise<string> {
	// Use a local regex to avoid shared lastIndex state across sequential or concurrent calls.
	// Supports dot notation and bracket-index notation: {{Event.field}}, {{Event[0].field}}
	// biome-ignore lint/complexity/noUselessEscapeInRegex: \] needed inside char class to match literal ]
	const re = /\{\{([A-Za-z_][A-Za-z0-9_.\[\]]*)\}\}/g;

	// Quick check: any placeholders at all?
	if (!re.test(template)) return template;
	re.lastIndex = 0;

	// Collect all unique placeholder keys
	const placeholders = new Set<string>();
	let match: RegExpExecArray | null = re.exec(template);
	while (match !== null) {
		if (match[1]) placeholders.add(match[1]);
		match = re.exec(template);
	}
	// re.lastIndex is automatically reset to 0 when exec() returns null

	// Build the replacement map
	const replacements = new Map<string, string>();

	// 1. Overlay triggering event input into state (in-memory only).
	//    Merge the live event fields into entry [0] so that both saved
	//    and live fields are accessible, with live values winning on conflict.
	const state: State = { ...opts.state };
	const eventName = opts.event?.eventName ? normalizeEventName(opts.event.eventName) : undefined;
	if (eventName && opts.event) {
		const raw = opts.event.raw as Record<string, unknown>;
		const prev = state[eventName] ?? {};
		const prevEntries = Array.isArray(prev.__entries)
			? (prev.__entries as Record<string, unknown>[])
			: [];
		if (prevEntries.length > 0) {
			// Merge live event into the first entry (live wins on conflict),
			// preserve remaining entries unchanged.
			const merged = { ...prevEntries[0], ...raw };
			state[eventName] = { __entries: [merged, ...prevEntries.slice(1)] };
		} else {
			// No prior state for this event — wrap the live event as entry [0].
			state[eventName] = { __entries: [{ ...raw }] };
		}
	}

	// 2. State field paths (dot notation resolved against nested state)
	for (const key of placeholders) {
		const value = resolveFieldPath(state, key);
		if (value !== undefined) {
			replacements.set(key, String(value));
		}
	}

	// 3. Git placeholders (only fetch if referenced and not already resolved)
	const needsFiles =
		(placeholders.has("CHANGED_FILES") && !replacements.has("CHANGED_FILES")) ||
		(placeholders.has("CHANGED_PACKAGES") && !replacements.has("CHANGED_PACKAGES"));

	if (needsFiles) {
		const files = await getChangedFiles({
			cwd: opts.projectDir,
			stagedOnly: opts.staged ?? false,
			fileExt: opts.fileExt ?? "",
		});

		if (placeholders.has("CHANGED_FILES") && !replacements.has("CHANGED_FILES")) {
			replacements.set("CHANGED_FILES", files.join(" "));
		}
		if (placeholders.has("CHANGED_PACKAGES") && !replacements.has("CHANGED_PACKAGES")) {
			replacements.set("CHANGED_PACKAGES", getChangedPackages(files).join(" "));
		}
	}

	// Log unresolved placeholders
	for (const key of placeholders) {
		if (!replacements.has(key)) {
			log(TAG, `Unresolved placeholder: {{${key}}}`);
		}
	}

	// Apply replacements (re.lastIndex is 0 here after exec() exhausted the loop above)
	return template.replace(re, (_full, key: string) => {
		return replacements.get(key) ?? "";
	});
}
