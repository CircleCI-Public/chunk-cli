/**
 * `chunk hook state` command.
 *
 * Manages per-project state that persists across events within a
 * session. This enables cross-event data sharing — e.g., capturing the
 * original prompt in a `UserPromptSubmit` event and reading it back in a
 * later `Stop` or `PreToolUse` event for use in task instruction templates.
 *
 * State is automatically namespaced by event name. When `state save`
 * receives event input from stdin, it stores the entire input under the
 * event name (e.g., `UserPromptSubmit`). Templates reference fields as
 * `{{UserPromptSubmit.prompt}}`.
 *
 * Subcommands:
 *   - `save`    — Read event input from stdin and save under event name.
 *   - `append`  — Append event input as a new entry (records HEAD SHA per entry).
 *   - `load`    — Load a field from state and write to stdout.
 *   - `clear`   — Clear all saved state for the project.
 *
 * Exit codes:
 *   0 — Success
 *   1 — Infra error (cannot write file, bad args, etc.)
 *
 * The state command does not participate in allow/block signaling.
 * It always exits 0 on success and is intended as plumbing for other
 * hooks — not as a hook gate itself.
 */

import type { AgentEvent, HookAdapter } from "../lib/adapter";
import type { ResolvedConfig } from "../lib/config";
import { computeFingerprint, getHeadSha } from "../lib/git";
import { log } from "../lib/log";
import { appendEvent, clearState, loadField, readState, saveEvent } from "../lib/state";

const TAG = "state";

/** CLI flags parsed from argv for the state command. */
export type StateFlags = {
	subcommand: "save" | "append" | "load" | "clear";
	/** Field path for `load` subcommand (e.g., `UserPromptSubmit.prompt`). */
	field?: string;
};

/**
 * Execute the state command.
 *
 * @param config  Resolved config (YAML + env merged).
 * @param adapter Hook adapter for allow/block signaling.
 * @param event   Agent event from stdin (used by `save`).
 * @param flags   CLI flags from argv.
 */
export async function runState(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	flags: StateFlags,
): Promise<void> {
	log(TAG, `subcommand=${flags.subcommand}`);

	switch (flags.subcommand) {
		case "save":
			await handleSave(config, adapter, event);
			break;
		case "append":
			await handleAppend(config, adapter, event);
			break;
		case "load":
			handleLoad(config, flags);
			break;
		case "clear":
			handleClear(config);
			break;
	}
}

// ---------------------------------------------------------------------------
// save — clear + single-entry append (records HEAD + fingerprint like append)
// ---------------------------------------------------------------------------

async function handleSave(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
): Promise<void> {
	const key = adapter.stateKey(event);
	if (!key) {
		log(TAG, "No state key for event, nothing to save");
		return;
	}

	const [head, fingerprint] = await Promise.all([
		getHeadSha(config.projectDir),
		computeFingerprint({ cwd: config.projectDir }),
	]);
	const data: Record<string, unknown> = { ...event.raw };
	if (head) {
		data.head = head;
	}
	if (fingerprint) {
		data.fingerprint = fingerprint;
	}

	saveEvent(config.sentinelDir, config.projectDir, key, data);
	log(TAG, `Saved event: ${key}`);
}

// ---------------------------------------------------------------------------
// append — accumulate event entries with per-entry HEAD + fingerprint
// ---------------------------------------------------------------------------

async function handleAppend(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
): Promise<void> {
	const key = adapter.stateKey(event);
	if (!key) {
		log(TAG, "No state key for event, nothing to append");
		return;
	}

	// Each append records the current HEAD SHA and a composite fingerprint
	// (HEAD + working tree diff). The first entry's fingerprint serves as
	// the session baseline for change detection.
	const [head, fingerprint] = await Promise.all([
		getHeadSha(config.projectDir),
		computeFingerprint({ cwd: config.projectDir }),
	]);
	const data: Record<string, unknown> = { ...event.raw };
	if (head) {
		data.head = head;
	}
	if (fingerprint) {
		data.fingerprint = fingerprint;
	}

	appendEvent(config.sentinelDir, config.projectDir, key, data);
	log(TAG, `Appended event: ${key}`);
}

// ---------------------------------------------------------------------------
// load — read a field from state and write to stdout
// ---------------------------------------------------------------------------

function handleLoad(config: ResolvedConfig, flags: StateFlags): void {
	if (!flags.field) {
		// No field specified — dump entire state
		const state = readState(config.sentinelDir, config.projectDir);
		process.stdout.write(`${JSON.stringify(state, null, 2)}\n`);
		return;
	}

	const value = loadField(config.sentinelDir, config.projectDir, flags.field);
	if (value === undefined) {
		log(TAG, `Field "${flags.field}" not found in state`);
		process.exit(1);
	}

	process.stdout.write(typeof value === "string" ? value : JSON.stringify(value));
}

// ---------------------------------------------------------------------------
// clear — remove all state for the project
// ---------------------------------------------------------------------------

function handleClear(config: ResolvedConfig): void {
	clearState(config.sentinelDir, config.projectDir);
	log(TAG, "State cleared");
}
