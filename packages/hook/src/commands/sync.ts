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
import type { ExecCheckVerdict, SentinelCheckResult } from "../lib/check";
import {
	blockNoCount,
	blockWithLimit,
	evaluateSentinel,
	guardStopEvent,
	matchesTrigger,
	preEvaluateExec,
	resolveTriggerPatterns,
} from "../lib/check";
import type { ResolvedConfig } from "../lib/config";
import { getExec, getTask } from "../lib/config";
import { computeFingerprint, detectChanges } from "../lib/git";
import { log } from "../lib/log";
import type { SentinelData } from "../lib/sentinel";
import { removeSentinel, resetBlockCount, sentinelPath } from "../lib/sentinel";
import { getBaselineFingerprint } from "../lib/state";
import { loadInstructions, readTaskResult, resolveTaskSchemaContent } from "../lib/task-result";
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

	// Hoist detectChanges: precompute change detection results for each unique
	// {fileExt, staged} combination across all exec specs. This avoids
	// redundant git operations when multiple execs share the same file
	// extension filter.
	const changeCache = await precomputeChanges(config, flags);

	// Precompute fingerprints for exec specs with changes, used for
	// content-aware sentinel staleness detection.
	const hashCache = await precomputeFingerprints(config, flags, changeCache);

	// Session-aware staleness: sentinels from a different session are
	// treated as missing so commands re-run with fresh context.
	const marker = readMarker(config.projectDir);
	const currentSessionId = marker?.sessionId;

	// Precompute task-level skip-if-no-changes: compare the baseline HEAD
	// (saved on the first UserPromptSubmit) against the current HEAD and
	// working tree. If HEAD is unchanged and no uncommitted changes exist,
	// task specs with always=false can be skipped (e.g., question-only
	// interactions where the agent never modified code).
	const taskNoChanges = await precomputeTaskNoChanges(config);

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

		// ------------------------------------------------------------------
		// Exec specs: delegate to the shared pre-evaluation pipeline.
		// ------------------------------------------------------------------
		if (spec.type === "exec") {
			const exec = getExec(config, spec.name);
			if (!exec) continue; // validated at entry

			// Build cache from precomputed values for this spec.
			const cacheKey = changeCacheKey(exec.fileExt, flags.staged);
			const fpKey = fingerprintCacheKey(config, spec, flags);
			const resolvedExec = {
				...exec,
				always: exec.always || (flags.always ?? false),
			};
			const cache = {
				hasChanges: changeCache.get(cacheKey),
				contentHash: hashCache.get(fpKey),
			};

			// Trigger matching is handled at the sync group level, so pass
			// empty trigger flags to skip the per-spec trigger check.
			const verdict = await preEvaluateExec(
				config,
				adapter,
				event,
				resolvedExec,
				{ name: spec.name, staged: flags.staged },
				cache,
			);

			const handled = handleExecVerdict(
				config,
				adapter,
				event,
				flags,
				spec,
				specKey,
				group,
				groupLimit,
				verdict,
				collected,
				t,
			);
			if (handled === "exit") return;
			continue;
		}

		// ------------------------------------------------------------------
		// Task specs: skip-if-no-changes, then evaluate sentinel directly.
		// ------------------------------------------------------------------
		if (taskNoChanges) {
			const task = getTask(config, spec.name);
			if (task && !task.always && !flags.always) {
				group.passed.push(specKey);
				writeGroupSentinel(config.sentinelDir, config.projectDir, flags.specs, group);
				log(
					t,
					`  ${specKey}: no code changes since baseline → auto-pass (${group.passed.length}/${flags.specs.length})`,
				);
				continue;
			}
		}

		const sentinel = readTaskResult(
			config.sentinelDir,
			config.projectDir,
			spec.name,
			currentSessionId,
		);
		const result = evaluateSentinel(sentinel, currentSessionId);

		log(t, `  ${specKey}: ${result.kind}`);

		const handled = await handleTaskVerdict(
			config,
			adapter,
			event,
			flags,
			spec,
			specKey,
			group,
			groupLimit,
			result,
			sentinel,
			collected,
			t,
		);
		if (handled === "exit") return;
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
// Exec verdict handler
// ---------------------------------------------------------------------------

/**
 * Map an `ExecCheckVerdict` to a sync-loop action.
 *
 * Returns `"exit"` when the loop should terminate (bail mode block),
 * `"continue"` otherwise.
 */
function handleExecVerdict(
	config: ResolvedConfig,
	adapter: HookAdapter,
	_event: AgentEvent,
	flags: SyncFlags,
	spec: CommandSpec,
	specKey: string,
	group: GroupSentinel,
	groupLimit: number,
	verdict: ExecCheckVerdict,
	collected: CollectedIssue[],
	t: string,
): "exit" | "continue" {
	log(t, `  ${specKey}: ${verdict.kind}`);

	switch (verdict.kind) {
		case "skip-trigger":
		case "skip-no-changes": {
			group.passed.push(specKey);
			writeGroupSentinel(config.sentinelDir, config.projectDir, flags.specs, group);
			log(
				t,
				`  ${specKey}: ${verdict.kind} → auto-pass (${group.passed.length}/${flags.specs.length})`,
			);
			return "continue";
		}
		case "pass": {
			group.passed.push(specKey);
			writeGroupSentinel(config.sentinelDir, config.projectDir, flags.specs, group);
			resetBlockCount(config.sentinelDir, config.projectDir, spec.name);
			log(t, `  ${specKey}: pass → updated group (${group.passed.length}/${flags.specs.length})`);
			return "continue";
		}
		case "missing": {
			const reason =
				`${specLabel(spec)} has no results. Run it first:\n\n` +
				`  ${buildRunCommand(spec, flags)}\n\n` +
				`Retry after the command completes.`;
			if (flags.bail) {
				log(t, `  ${specKey}: missing → block (agent must run command first)`);
				blockNoCount(t, adapter, reason, config.projectDir);
				return "exit";
			}
			collected.push({ specKey, kind: "missing", reason });
			log(t, `  ${specKey}: missing → collected (${collected.length})`);
			return "continue";
		}
		case "pending": {
			const timeout = resolveTimeout(config, spec);
			const sentinel = verdict.sentinel;
			if (sentinel?.startedAt && timeout > 0) {
				const elapsed = (Date.now() - new Date(sentinel.startedAt).getTime()) / 1000;
				if (elapsed > timeout) {
					removeSentinel(config.sentinelDir, config.projectDir, spec.name);
					resetGroupOnFailure(config, flags, specKey);
					const reason =
						`${specLabel(spec)} timed out after ${Math.round(elapsed)}s ` +
						`(timeout: ${timeout}s).\n\n` +
						`Investigate and re-run: ${buildRunCommand(spec, flags)}`;
					if (flags.bail) {
						log(
							t,
							`  ${specKey}: pending (timed out after ${Math.round(elapsed)}s) → resetting group`,
						);
						blockWithLimit(t, adapter, config, spec.name, groupLimit, reason);
						return "exit";
					}
					group.passed = readGroupSentinel(
						config.sentinelDir,
						config.projectDir,
						flags.specs,
					).passed;
					collected.push({ specKey, kind: "timeout", reason });
					log(t, `  ${specKey}: timeout → collected (${collected.length})`);
					return "continue";
				}
			}
			const pendingReason = `${specLabel(spec)} is still running. Wait for completion before retrying.`;
			if (flags.bail) {
				log(t, `  ${specKey}: pending → block (waiting for command to complete)`);
				blockNoCount(t, adapter, pendingReason, config.projectDir);
				return "exit";
			}
			collected.push({ specKey, kind: "pending", reason: pendingReason });
			log(t, `  ${specKey}: pending → collected (${collected.length})`);
			return "continue";
		}
		case "fail": {
			const mode = flags.onFail ?? "restart";
			resetGroupOnFailure(config, flags, specKey);
			removeSentinel(config.sentinelDir, config.projectDir, spec.name);
			const reason = buildFailMessage(spec, verdict.sentinel);
			if (flags.bail) {
				log(t, `  ${specKey}: fail → ${mode === "retry" ? "retry reset" : "resetting group"}`);
				blockWithLimit(t, adapter, config, spec.name, groupLimit, reason);
				return "exit";
			}
			group.passed = readGroupSentinel(config.sentinelDir, config.projectDir, flags.specs).passed;
			collected.push({ specKey, kind: "fail", reason });
			log(t, `  ${specKey}: fail → collected (${collected.length})`);
			return "continue";
		}
	}
}

// ---------------------------------------------------------------------------
// Task verdict handler
// ---------------------------------------------------------------------------

/**
 * Map an `evaluateSentinel` result to a sync-loop action for task specs.
 *
 * Returns `"exit"` when the loop should terminate (bail mode block),
 * `"continue"` otherwise.
 */
async function handleTaskVerdict(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	flags: SyncFlags,
	spec: CommandSpec,
	specKey: string,
	group: GroupSentinel,
	groupLimit: number,
	result: SentinelCheckResult,
	sentinel: SentinelData | undefined,
	collected: CollectedIssue[],
	t: string,
): Promise<"exit" | "continue"> {
	switch (result.kind) {
		case "pass": {
			group.passed.push(specKey);
			writeGroupSentinel(config.sentinelDir, config.projectDir, flags.specs, group);
			resetBlockCount(config.sentinelDir, config.projectDir, spec.name);
			log(t, `  ${specKey}: pass → updated group (${group.passed.length}/${flags.specs.length})`);
			return "continue";
		}
		case "missing": {
			const reason = await buildMissingMessage(config, event, flags, spec);
			if (flags.bail) {
				log(t, `  ${specKey}: missing → block (agent must run task first)`);
				blockNoCount(t, adapter, reason, config.projectDir);
				return "exit";
			}
			collected.push({ specKey, kind: "missing", reason });
			log(t, `  ${specKey}: missing → collected (${collected.length})`);
			return "continue";
		}
		case "pending": {
			const timeout = resolveTimeout(config, spec);
			if (sentinel?.startedAt && timeout > 0) {
				const elapsed = (Date.now() - new Date(sentinel.startedAt).getTime()) / 1000;
				if (elapsed > timeout) {
					removeSentinel(config.sentinelDir, config.projectDir, spec.name);
					resetGroupOnFailure(config, flags, specKey);
					const reason =
						`${specLabel(spec)} timed out after ${Math.round(elapsed)}s ` +
						`(timeout: ${timeout}s).\n\n` +
						`Investigate and re-run: ${buildRunCommand(spec, flags)}`;
					if (flags.bail) {
						log(
							t,
							`  ${specKey}: pending (timed out after ${Math.round(elapsed)}s) → resetting group`,
						);
						blockWithLimit(t, adapter, config, spec.name, groupLimit, reason);
						return "exit";
					}
					group.passed = readGroupSentinel(
						config.sentinelDir,
						config.projectDir,
						flags.specs,
					).passed;
					collected.push({ specKey, kind: "timeout", reason });
					log(t, `  ${specKey}: timeout → collected (${collected.length})`);
					return "continue";
				}
			}
			const pendingReason = `${specLabel(spec)} is still running. Wait for completion before retrying.`;
			if (flags.bail) {
				log(t, `  ${specKey}: pending → block (waiting for task to complete)`);
				blockNoCount(t, adapter, pendingReason, config.projectDir);
				return "exit";
			}
			collected.push({ specKey, kind: "pending", reason: pendingReason });
			log(t, `  ${specKey}: pending → collected (${collected.length})`);
			return "continue";
		}
		case "fail": {
			const mode = flags.onFail ?? "restart";
			resetGroupOnFailure(config, flags, specKey);
			removeSentinel(config.sentinelDir, config.projectDir, spec.name);
			const reason = buildFailMessage(spec, result.sentinel);
			if (flags.bail) {
				log(t, `  ${specKey}: fail → ${mode === "retry" ? "retry reset" : "resetting group"}`);
				blockWithLimit(t, adapter, config, spec.name, groupLimit, reason);
				return "exit";
			}
			group.passed = readGroupSentinel(config.sentinelDir, config.projectDir, flags.specs).passed;
			collected.push({ specKey, kind: "fail", reason });
			log(t, `  ${specKey}: fail → collected (${collected.length})`);
			return "continue";
		}
	}
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
	const instructions = await loadInstructions(
		task.instructions || undefined,
		config.projectDir,
		config.sentinelDir,
		flags.staged,
		event,
	);

	// Load schema.
	const schema = resolveTaskSchemaContent(config.projectDir, task.schema);

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

/**
 * Build a cache key for fingerprint lookups, scoped by exec name so each
 * spec gets its own fingerprint (different execs may have different fileExt).
 */
function fingerprintCacheKey(config: ResolvedConfig, spec: CommandSpec, flags: SyncFlags): string {
	if (spec.type !== "exec") return "";
	const exec = getExec(config, spec.name);
	return `fp|${exec?.fileExt || "*"}|${flags.staged ? "staged" : "all"}`;
}

/**
 * Precompute fingerprints for each unique `{fileExt, staged}` combination
 * across exec specs that actually have changes. Specs with no changes are
 * skipped — their sentinels are auto-passed and never reach fingerprint validation.
 */
async function precomputeFingerprints(
	config: ResolvedConfig,
	flags: SyncFlags,
	changeCache: Map<string, boolean>,
): Promise<Map<string, string>> {
	const cache = new Map<string, string>();
	const seen = new Set<string>();

	for (const spec of flags.specs) {
		if (spec.type !== "exec") continue;
		const exec = getExec(config, spec.name);
		if (!exec) continue;

		const key = fingerprintCacheKey(config, spec, flags);
		if (seen.has(key)) continue;
		seen.add(key);

		// Only hash specs that have changes — no-change specs are auto-passed.
		const changeKey = changeCacheKey(exec.fileExt, flags.staged);
		if (!exec.always && !flags.always && !(changeCache.get(changeKey) ?? false)) continue;

		const hash = await computeFingerprint({
			cwd: config.projectDir,
			fileExt: exec.fileExt,
			staged: flags.staged,
		});
		cache.set(key, hash);
		log(TAG, `fingerprint [${key}]: ${hash.slice(0, 12)}…`);
	}

	return cache;
}

// ---------------------------------------------------------------------------
// Task-level change detection
// ---------------------------------------------------------------------------

/**
 * Determine whether any code changes occurred since the baseline.
 *
 * Compares the composite fingerprint (HEAD + working tree diff hash)
 * saved on the first `state save`/`append` for `UserPromptSubmit`
 * against the current fingerprint. A single comparison covers both
 * commit-level and file-level changes.
 *
 * Returns `true` when no code has changed — the agent only asked
 * questions or no tool calls modified files.
 *
 * Returns `false` (= changes detected / unknown) when:
 * - No baseline is saved (first interaction or state was cleared)
 * - Fingerprint differs (HEAD moved or files changed)
 */
export async function precomputeTaskNoChanges(config: ResolvedConfig): Promise<boolean> {
	const baselineFingerprint = getBaselineFingerprint(
		config.sentinelDir,
		config.projectDir,
		"UserPromptSubmit",
	);

	if (!baselineFingerprint) {
		log(TAG, "task skip: no baseline fingerprint in state → cannot skip");
		return false;
	}

	const currentFingerprint = await computeFingerprint({ cwd: config.projectDir });
	if (!currentFingerprint || currentFingerprint !== baselineFingerprint) {
		log(TAG, "task skip: fingerprint changed → cannot skip");
		return false;
	}

	log(TAG, "task skip: fingerprint unchanged → tasks eligible for skip");
	return true;
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
