/**
 * `chunk hook sync` command.
 *
 * Groups multiple exec/task check commands into a single sequential check.
 * This replaces the old coordination mechanism with an explicit, ordered
 * participant list — eliminating ping-pong, stale sentinels, and timing
 * heuristics.
 *
 * Subcommands:
 *   - `check <spec...>` — Check sentinels for a group of commands.
 *
 * Spec format:
 *   Each spec is `type:name`, e.g. `exec:tests`, `task:review`.
 *   Flags for the group are passed after the specs.
 *
 * Behavior:
 *   The sync command maintains its own group sentinel that tracks which
 *   commands in the group have already passed. On each invocation it
 *   resumes from where it left off, skipping already-passed commands.
 *
 *   - If a command's sentinel is "pass" → mark it in the group sentinel,
 *     consume the individual sentinel, and move to the next.
 *   - If a command's sentinel is "missing" → block with a directive to
 *     run it.
 *   - If a command's sentinel is "pending" → block (waiting).
 *   - If a command's sentinel is "fail" → remove the group sentinel
 *     (restart from the beginning on next invocation), block with the
 *     failure message. The individual failing sentinel is also removed
 *     so the agent re-runs it.
 *
 *   When ALL commands pass → remove the group sentinel, allow.
 *
 * Exit codes:
 *   0 — All commands passed (allow)
 *   2 — One or more commands need attention (block)
 *   1 — Infra error
 */

import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import type { AgentEvent, HookAdapter } from "../lib/adapter";
import {
	blockNoCount,
	blockWithLimit,
	evaluateSentinel,
	guardStopEvent,
	matchesTrigger,
	resolveTriggerPatterns,
} from "../lib/check";
import type { ResolvedConfig } from "../lib/config";
import { getExec, getTask } from "../lib/config";
import { detectChanges } from "../lib/git";
import { log } from "../lib/log";
import { expandPlaceholders } from "../lib/placeholders";
import type { SentinelData } from "../lib/sentinel";
import { readSentinel, removeSentinel, resetBlockCount, sentinelPath } from "../lib/sentinel";
import { readState } from "../lib/state";
import { DEFAULT_TASK_SCHEMA, readTaskResult } from "../lib/task-result";
import { readMarker } from "./scope";

const TAG = "sync";

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

/** A parsed command specifier — `exec:tests` or `task:review`. */
export type CommandSpec = {
	type: "exec" | "task";
	name: string;
};

/** Flags for the sync check command. */
export type SyncFlags = {
	subcommand: "check";
	/** Parsed command specifiers in order. */
	specs: CommandSpec[];
	/** --on trigger group. */
	on?: string;
	/** --trigger inline pattern. */
	trigger?: string;
	/** --matcher tool-name regex filter. */
	matcher?: string;
	/** --limit override for all commands. */
	limit?: number;
	/** Per-command flag overrides (e.g., --staged, --always). */
	staged?: boolean;
	always?: boolean;
	/**
	 * How to handle failures in the sync group.
	 *
	 * - `"restart"` (default): remove the entire group sentinel on failure,
	 *   forcing all specs to re-pass from scratch.
	 * - `"retry"`: only remove the failed spec from `group.passed`,
	 *   preserving previously-passed specs. The agent only needs to re-run
	 *   the failed command and any subsequent specs in the sequence.
	 */
	onFail?: "restart" | "retry";
	/**
	 * When true, stop at the first non-pass spec and block immediately
	 * instead of evaluating all specs. By default (bail = false), all specs
	 * are evaluated and non-pass results are collected into a single block
	 * message, giving the agent a complete picture in one round-trip.
	 */
	bail?: boolean;
};

// ---------------------------------------------------------------------------
// Group sentinel — tracks which commands have passed in this group
// ---------------------------------------------------------------------------

/** Shape of the group sentinel file. */
type GroupSentinel = {
	/** Names of specs (in `type:name` format) that have passed. */
	passed: string[];
};

/** Compute a deterministic ID for the group. */
function groupId(projectDir: string, specs: CommandSpec[]): string {
	const key = specs.map((s) => `${s.type}:${s.name}`).join(",");
	const hash = createHash("sha256").update(`${projectDir}:sync:${key}`).digest("hex").slice(0, 16);
	return `sync-${hash}`;
}

/** Full path to the group sentinel file. */
function groupSentinelPath(sentinelDir: string, projectDir: string, specs: CommandSpec[]): string {
	return join(sentinelDir, `${groupId(projectDir, specs)}.json`);
}

/** Read the group sentinel, returning an empty state if missing/malformed. */
function readGroupSentinel(
	sentinelDir: string,
	projectDir: string,
	specs: CommandSpec[],
): GroupSentinel {
	const p = groupSentinelPath(sentinelDir, projectDir, specs);
	if (!existsSync(p)) return { passed: [] };
	try {
		const content = readFileSync(p, "utf-8");
		const data = JSON.parse(content) as GroupSentinel;
		if (Array.isArray(data.passed)) return data;
		return { passed: [] };
	} catch {
		return { passed: [] };
	}
}

/** Write the group sentinel. */
function writeGroupSentinel(
	sentinelDir: string,
	projectDir: string,
	specs: CommandSpec[],
	data: GroupSentinel,
): void {
	mkdirSync(sentinelDir, { recursive: true });
	const p = groupSentinelPath(sentinelDir, projectDir, specs);
	writeFileSync(p, `${JSON.stringify(data, null, 2)}\n`, "utf-8");
}

/** Remove the group sentinel. */
function removeGroupSentinel(sentinelDir: string, projectDir: string, specs: CommandSpec[]): void {
	const p = groupSentinelPath(sentinelDir, projectDir, specs);
	if (existsSync(p)) rmSync(p);
}

/**
 * Reset group state on failure according to the configured on-fail mode.
 *
 * - `"restart"` (default): removes the entire group sentinel, forcing all
 *   specs to re-pass from scratch on the next invocation.
 * - `"retry"`: removes only the failed spec (and any specs after it)
 *   from the group's `passed` list. Previously-passed specs before the
 *   failure point are preserved. This is implemented by rewriting the
 *   group sentinel without the failed spec — the ordered walk in
 *   `runSync()` naturally re-checks it and all subsequent specs.
 */
function resetGroupOnFailure(
	config: ResolvedConfig,
	flags: SyncFlags,
	failedSpecKey: string,
): void {
	const mode = flags.onFail ?? "restart";

	if (mode === "retry") {
		const group = readGroupSentinel(config.sentinelDir, config.projectDir, flags.specs);
		// Remove the failed spec from passed. Specs after the failed one in
		// the ordered list will be re-evaluated naturally since the loop
		// processes specs sequentially and the failed one blocks progression.
		group.passed = group.passed.filter((key) => key !== failedSpecKey);
		writeGroupSentinel(config.sentinelDir, config.projectDir, flags.specs, group);
	} else {
		removeGroupSentinel(config.sentinelDir, config.projectDir, flags.specs);
	}
}

// ---------------------------------------------------------------------------
// Entry point
// ---------------------------------------------------------------------------

/**
 * Run the sync command.
 */
export async function runSync(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	flags: SyncFlags,
): Promise<void> {
	const t = TAG;

	log(t, `sync check: ${flags.specs.map((s) => `${s.type}:${s.name}`).join(", ")}`);

	// Validate that every spec name exists in config — fail early on typos or truncation.
	for (const spec of flags.specs) {
		const known = spec.type === "exec" ? getExec(config, spec.name) : getTask(config, spec.name);
		if (!known) {
			log(t, `ERROR: spec "${spec.type}:${spec.name}" not found in config`);
			process.exit(1);
		}
	}

	// Stop-event recursion guard — use the group limit (first found, or 0).
	const groupLimit = flags.limit ?? resolveGroupLimit(config, flags.specs);
	guardStopEvent(t, adapter, event, groupLimit);

	// Trigger matching — skip if the event doesn't match the trigger filter.
	const patterns = resolveTriggerPatterns(t, config, flags);
	if (patterns.length > 0 && !matchesTrigger(adapter, event, patterns)) {
		log(t, "Event does not match trigger filter, allowing");
		adapter.allow();
	}

	// Read group sentinel to find where we left off.
	const group = readGroupSentinel(config.sentinelDir, config.projectDir, flags.specs);
	log(t, `Group state: ${group.passed.length}/${flags.specs.length} passed`);

	// Session-aware staleness: sentinels from a different session are
	// treated as missing so commands re-run with fresh context.
	const marker = readMarker(config.projectDir);
	const currentSessionId = marker?.sessionId;

	// Hoist detectChanges: precompute change detection results for each unique
	// {fileExt, staged} combination across all exec specs. This avoids
	// redundant git operations when multiple execs share the same file
	// extension filter.
	const changeCache = await precomputeChanges(config, flags);

	// Walk through specs in order, skipping already-passed ones.
	// By default, we walk ALL specs and gather non-pass results into a
	// single combined block message instead of stopping at the first issue.
	// This gives the agent a complete picture of everything that needs
	// attention in one round-trip. With --bail, we stop at the first issue.
	const collected: CollectedIssue[] = [];

	for (const spec of flags.specs) {
		const specKey = `${spec.type}:${spec.name}`;
		if (group.passed.includes(specKey)) {
			log(t, `  ${specKey}: already passed (cached in group sentinel)`);
			continue;
		}

		// Skip-if-no-changes for exec specs
		if (spec.type === "exec") {
			const exec = getExec(config, spec.name);
			if (exec && !exec.always && !flags.always) {
				const cacheKey = changeCacheKey(exec.fileExt, flags.staged);
				const hasChanges = changeCache.get(cacheKey) ?? false;
				if (!hasChanges) {
					group.passed.push(specKey);
					writeGroupSentinel(config.sentinelDir, config.projectDir, flags.specs, group);
					log(
						t,
						`  ${specKey}: no changed files → auto-pass (${group.passed.length}/${flags.specs.length})`,
					);
					continue;
				}
			}
		}

		// Read the individual command's sentinel.
		const sentinel =
			spec.type === "task"
				? readTaskResult(config.sentinelDir, config.projectDir, spec.name)
				: readSentinel(config.sentinelDir, config.projectDir, spec.name);
		const result = evaluateSentinel(sentinel, currentSessionId);

		log(t, `  ${specKey}: ${result.kind}`);

		switch (result.kind) {
			case "pass": {
				// Mark as passed in the group sentinel, consume the individual sentinel.
				group.passed.push(specKey);
				writeGroupSentinel(config.sentinelDir, config.projectDir, flags.specs, group);
				removeSentinel(config.sentinelDir, config.projectDir, spec.name);
				resetBlockCount(config.sentinelDir, config.projectDir, spec.name);
				log(
					t,
					`  ${specKey}: consumed sentinel, updated group (${group.passed.length}/${flags.specs.length})`,
				);
				continue;
			}
			case "missing": {
				if (flags.bail) {
					// Bail mode: block immediately on first missing.
					const reason = await buildMissingMessage(config, event, flags, spec);
					log(t, `  ${specKey}: missing → block (agent must run command first)`);
					blockNoCount(t, adapter, reason, config.projectDir);
					return; // blockNoCount calls process.exit
				}
				// Default: gather and continue.
				const reason = await buildMissingMessage(config, event, flags, spec);
				collected.push({ specKey, kind: "missing", reason });
				log(t, `  ${specKey}: missing → collected (${collected.length})`);
				continue;
			}
			case "pending": {
				// Check timeout.
				const timeout = resolveTimeout(config, spec);
				if (sentinel?.startedAt && timeout > 0) {
					const elapsed = (Date.now() - new Date(sentinel.startedAt).getTime()) / 1000;
					if (elapsed > timeout) {
						if (flags.bail) {
							log(
								t,
								`  ${specKey}: pending (timed out after ${Math.round(elapsed)}s) → resetting group`,
							);
							removeSentinel(config.sentinelDir, config.projectDir, spec.name);
							resetGroupOnFailure(config, flags, specKey);
							const reason =
								`${specLabel(spec)} timed out after ${Math.round(elapsed)}s ` +
								`(timeout: ${timeout}s).\n\n` +
								`Investigate and re-run: ${buildRunCommand(spec, flags)}`;
							blockWithLimit(t, adapter, config, spec.name, groupLimit, reason);
							return;
						}
						// Default: treat timeout as a failure-like issue.
						removeSentinel(config.sentinelDir, config.projectDir, spec.name);
						resetGroupOnFailure(config, flags, specKey);
						// Sync in-memory group after disk reset to prevent subsequent
						// pass cases from re-writing stale passed entries.
						group.passed = readGroupSentinel(
							config.sentinelDir,
							config.projectDir,
							flags.specs,
						).passed;
						const reason =
							`${specLabel(spec)} timed out after ${Math.round(elapsed)}s ` +
							`(timeout: ${timeout}s).\n\n` +
							`Investigate and re-run: ${buildRunCommand(spec, flags)}`;
						collected.push({ specKey, kind: "timeout", reason });
						log(t, `  ${specKey}: timeout → collected (${collected.length})`);
						continue;
					}
				}
				if (flags.bail) {
					log(t, `  ${specKey}: pending → block (waiting for command to complete)`);
					blockNoCount(
						t,
						adapter,
						`${specLabel(spec)} is still running. Wait for completion before retrying.`,
						config.projectDir,
					);
					return;
				}
				// Default: gather pending.
				collected.push({
					specKey,
					kind: "pending",
					reason: `${specLabel(spec)} is still running. Wait for completion before retrying.`,
				});
				log(t, `  ${specKey}: pending → collected (${collected.length})`);
				continue;
			}
			case "fail": {
				const mode = flags.onFail ?? "restart";
				if (flags.bail) {
					if (mode === "retry") {
						log(t, `  ${specKey}: fail → retry reset (removing only failed spec)`);
					} else {
						log(t, `  ${specKey}: fail → resetting group (all commands must re-pass)`);
					}
					resetGroupOnFailure(config, flags, specKey);
					removeSentinel(config.sentinelDir, config.projectDir, spec.name);
					const reason = buildFailMessage(spec, result.sentinel);
					blockWithLimit(t, adapter, config, spec.name, groupLimit, reason);
					return;
				}
				// Default: reset group for failure and gather.
				if (mode === "retry") {
					log(t, `  ${specKey}: fail → retry reset + collected`);
				} else {
					log(t, `  ${specKey}: fail → resetting group + collected`);
				}
				resetGroupOnFailure(config, flags, specKey);
				// Sync in-memory group after disk reset to prevent subsequent
				// pass cases from re-writing stale passed entries.
				group.passed = readGroupSentinel(config.sentinelDir, config.projectDir, flags.specs).passed;
				removeSentinel(config.sentinelDir, config.projectDir, spec.name);
				const reason = buildFailMessage(spec, result.sentinel);
				collected.push({ specKey, kind: "fail", reason });
				log(t, `  ${specKey}: fail → collected (${collected.length})`);
				continue;
			}
		}
	}

	// If any issues were gathered, emit a single combined block message.
	if (collected.length > 0) {
		const message = buildCollectedMessage(collected);
		log(t, `${collected.length} issue(s) found → block`);
		// Use the first collected spec's name for the block count key. If any
		// issue is a fail or timeout (counted), use blockWithLimit; otherwise
		// use blockNoCount (missing/pending are not counted).
		const hasCounted = collected.some((c) => c.kind === "fail" || c.kind === "timeout");
		if (hasCounted) {
			const firstCounted = collected.find(
				(c) => c.kind === "fail" || c.kind === "timeout",
			) as CollectedIssue;
			const specName = firstCounted.specKey.split(":")[1] ?? firstCounted.specKey;
			blockWithLimit(t, adapter, config, specName, groupLimit, message);
		} else {
			blockNoCount(t, adapter, message, config.projectDir);
		}
		return;
	}

	// All specs passed — clean up and allow.
	log(t, "All commands passed → allow");
	removeGroupSentinel(config.sentinelDir, config.projectDir, flags.specs);
	adapter.allow();
}

// ---------------------------------------------------------------------------
// Collected issue types & helpers
// ---------------------------------------------------------------------------

/** An issue gathered during spec evaluation. */
type CollectedIssue = {
	specKey: string;
	kind: "missing" | "pending" | "timeout" | "fail";
	reason: string;
};

/**
 * Build a single combined block message from all gathered issues.
 *
 * Groups issues by kind and formats them into a readable summary so the
 * agent can address everything in one round-trip.
 */
function buildCollectedMessage(issues: CollectedIssue[]): string {
	const sections: string[] = [`## Sync: ${issues.length} issue(s) need attention\n`];

	for (const issue of issues) {
		const label = issue.kind.toUpperCase();
		sections.push(`### [${label}] ${issue.specKey}\n`);
		sections.push(issue.reason);
		sections.push(""); // blank line separator
	}

	sections.push(
		"---",
		"Address all issues above, then retry. Commands that already passed are preserved.",
	);

	return sections.join("\n");
}

// ---------------------------------------------------------------------------
// Message builders
// ---------------------------------------------------------------------------

/** Human-readable label for a spec. */
function specLabel(spec: CommandSpec): string {
	return spec.type === "exec" ? `Exec "${spec.name}"` : `Task "${spec.name}"`;
}

/** Build the `run` command the agent should execute for a missing exec. */
function buildRunCommand(spec: CommandSpec, flags: SyncFlags): string {
	if (spec.type === "exec") {
		const parts = ["chunk hook exec run", spec.name, "--no-check"];
		if (flags.staged) parts.push("--staged");
		if (flags.always) parts.push("--always");
		return parts.join(" ");
	}
	// Tasks don't have a "run" command — the agent spawns a subagent.
	return `(spawn subagent for task "${spec.name}")`;
}

/** Build the block message for a missing sentinel. */
async function buildMissingMessage(
	config: ResolvedConfig,
	event: AgentEvent,
	flags: SyncFlags,
	spec: CommandSpec,
): Promise<string> {
	if (spec.type === "exec") {
		const runCmd = buildRunCommand(spec, flags);
		return (
			`${specLabel(spec)} has no results. Run it first:\n\n` +
			`  ${runCmd}\n\n` +
			`Retry after the command completes.`
		);
	}

	// Task missing — provide sentinel path and instructions.
	return buildTaskMissingMessage(config, event, flags, spec);
}

/** Build the block message for a missing task sentinel. */
async function buildTaskMissingMessage(
	config: ResolvedConfig,
	event: AgentEvent,
	flags: SyncFlags,
	spec: CommandSpec,
): Promise<string> {
	const task = getTask(config, spec.name);
	if (!task) {
		return `Task "${spec.name}" is not configured. Add it to .chunk/hook/config.yml.`;
	}

	const resultPath = sentinelPath(config.sentinelDir, config.projectDir, spec.name);

	// Load instructions if available.
	let instructions = "";
	const instructionsPath = task.instructions || undefined;
	if (instructionsPath) {
		const resolved = instructionsPath.startsWith("/")
			? instructionsPath
			: join(config.projectDir, instructionsPath);
		try {
			if (existsSync(resolved)) {
				instructions = readFileSync(resolved, "utf-8");
				const state = readState(config.sentinelDir, config.projectDir);
				instructions = await expandPlaceholders(instructions, {
					state,
					projectDir: config.projectDir,
					staged: flags.staged,
					event,
				});
			}
		} catch {
			// Ignore — instructions are optional.
		}
	}

	// Load schema.
	let schema = DEFAULT_TASK_SCHEMA;
	if (task.schema) {
		const schemaPath = task.schema.startsWith("/")
			? task.schema
			: join(config.projectDir, task.schema);
		try {
			if (existsSync(schemaPath)) {
				schema = readFileSync(schemaPath, "utf-8");
			}
		} catch {
			// Ignore — use default schema.
		}
	}

	const parts = [`Task "${spec.name}" has no result. Spawn a subagent to complete the task.`, ""];

	if (instructions) {
		parts.push("## Instructions", "", instructions, "");
	}

	parts.push(
		"## Result format",
		"",
		"Write the result as a JSON file. Schema:",
		"",
		"```json",
		schema,
		"```",
		"",
		`Result path: ${resultPath}`,
	);

	return parts.join("\n");
}

/** Build the block message for a failed sentinel. */
function buildFailMessage(spec: CommandSpec, sentinel: SentinelData): string {
	if (spec.type === "exec") {
		const cmd = sentinel.command ?? spec.name;
		const exitCode = sentinel.exitCode ?? 1;
		const output = sentinel.output ?? "(no output)";
		return (
			`${specLabel(spec)} failed (exit ${exitCode}).\n` +
			`Command: ${cmd}\n\n` +
			`Output:\n${output}\n\n` +
			`Fix the issues and retry.`
		);
	}

	// Task failure.
	const reason = sentinel.details ?? "(no reason provided)";
	const agentDetails = sentinel.rawResult ?? reason;
	return `Task blocked: issues found. Fix them before stopping.\n\n${agentDetails}`;
}

// ---------------------------------------------------------------------------
// Change detection cache
// ---------------------------------------------------------------------------

/**
 * Build a cache key for a `{fileExt, staged}` combination.
 *
 * Exported for testing.
 */
export function changeCacheKey(fileExt: string, staged?: boolean): string {
	return `${fileExt || "*"}|${staged ? "staged" : "all"}`;
}

/**
 * Precompute change detection results for all unique `{fileExt, staged}`
 * combinations across exec specs.
 *
 * This avoids redundant `git` invocations when multiple execs share the
 * same file-extension filter. Task specs are skipped — they use a
 * different change-detection path (`hasUncommittedChanges` /
 * `hasStagedChanges`) that is not called from the sync loop.
 */
async function precomputeChanges(
	config: ResolvedConfig,
	flags: SyncFlags,
): Promise<Map<string, boolean>> {
	const cache = new Map<string, boolean>();
	const seen = new Set<string>();

	for (const spec of flags.specs) {
		if (spec.type !== "exec") continue;
		const exec = getExec(config, spec.name);
		if (!exec || exec.always || flags.always) continue;

		const key = changeCacheKey(exec.fileExt, flags.staged);
		if (seen.has(key)) continue;
		seen.add(key);

		const hasChanges = await detectChanges({
			cwd: config.projectDir,
			fileExt: exec.fileExt,
			staged: flags.staged,
		});
		cache.set(key, hasChanges);
		log(TAG, `change detection [${key}]: ${hasChanges ? "changes found" : "no changes"}`);
	}

	return cache;
}

// ---------------------------------------------------------------------------
// Config resolution helpers
// ---------------------------------------------------------------------------

/** Resolve the effective limit for the group (first defined limit wins). */
function resolveGroupLimit(config: ResolvedConfig, specs: CommandSpec[]): number {
	for (const spec of specs) {
		if (spec.type === "exec") {
			const exec = getExec(config, spec.name);
			if (exec && exec.limit > 0) return exec.limit;
		} else {
			const task = getTask(config, spec.name);
			if (task && task.limit > 0) return task.limit;
		}
	}
	return 0;
}

/** Resolve timeout for a specific spec. */
function resolveTimeout(config: ResolvedConfig, spec: CommandSpec): number {
	if (spec.type === "exec") {
		const exec = getExec(config, spec.name);
		return exec?.timeout ?? 300;
	}
	const task = getTask(config, spec.name);
	return task?.timeout ?? 600;
}

// ---------------------------------------------------------------------------
// Spec parsing
// ---------------------------------------------------------------------------

/**
 * Parse command specifiers from positional arguments.
 *
 * Each spec is `type:name` where type is `exec` or `task`.
 * Returns undefined if any spec is invalid.
 */
export function parseSpecs(positionals: string[]): CommandSpec[] | undefined {
	const specs: CommandSpec[] = [];
	for (const arg of positionals) {
		const colon = arg.indexOf(":");
		if (colon === -1) return undefined;
		const type = arg.slice(0, colon);
		const name = arg.slice(colon + 1);
		if (!name) return undefined;
		if (type !== "exec" && type !== "task") return undefined;
		specs.push({ type, name });
	}
	return specs.length > 0 ? specs : undefined;
}
