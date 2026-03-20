/**
 * `chunk hook run` — unified hook event dispatch.
 *
 * Single entry point for all hook events. Reads the event from stdin,
 * routes to the appropriate handler based on event name and config.
 *
 * Infrastructure events (SessionStart, UserPromptSubmit, SessionEnd) are
 * always handled automatically — no config needed.
 *
 * Check events (PreToolUse, Stop) read the `hooks:` config section to
 * determine matcher, trigger, and checks to run.
 *
 * Exit codes:
 *   0 — Allow
 *   2 — Block
 *   1 — Infra error
 */

import type { AgentEvent, HookAdapter } from "../lib/adapter";
import { blockNoCount, blockWithLimit, guardStopEvent, matchesTrigger, resolveTriggerPatterns } from "../lib/check";
import { extractSessionId, normalizeEventName } from "../lib/compat";
import type { HookEventConfig, ResolvedConfig, ResolvedExec } from "../lib/config";
import { getExec, getTask, getTriggerPatterns } from "../lib/config";
import { computeFingerprint, getHeadSha } from "../lib/git";
import { log } from "../lib/log";
import { readSentinel, resetBlockCount } from "../lib/sentinel";
import { appendEvent, clearState } from "../lib/state";
import { loadInstructions, readTaskResult, resolveTaskSchemaContent } from "../lib/task-result";
import { buildRunnerCommand, formatFailureReason } from "./exec";
import { activateScope, deactivateScope, readMarker } from "./scope";
import { preEvaluateExec } from "../lib/check";
import { sentinelPath } from "../lib/sentinel";

const TAG = "dispatch";

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Dispatch a hook event to the appropriate handler.
 *
 * Called by `chunk hook run` — reads event from stdin, routes based on
 * event name and hooks config.
 */
export async function dispatchHookEvent(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
): Promise<void> {
	const canonical = normalizeEventName(event.eventName);
	const sessionId = extractSessionId(event.raw);
	log(TAG, `event=${canonical} tool=${event.toolName ?? "(none)"} session=${sessionId?.slice(0, 8) ?? "none"}`);

	switch (canonical) {
		case "SessionStart":
			return handleSessionStart(config, adapter, event, sessionId);
		case "UserPromptSubmit":
			return handleUserPromptSubmit(config, adapter, event, sessionId);
		case "PreToolUse":
			return handleCheckEvent(config, adapter, event, canonical, sessionId);
		case "Stop":
			return handleCheckEvent(config, adapter, event, canonical, sessionId);
		case "SessionEnd":
			return handleSessionEnd(config, adapter, event, sessionId);
		default:
			log(TAG, `Unknown event "${canonical}", allowing`);
			adapter.allow();
	}
}

// ---------------------------------------------------------------------------
// Infrastructure events
// ---------------------------------------------------------------------------

function handleSessionStart(
	config: ResolvedConfig,
	adapter: HookAdapter,
	_event: AgentEvent,
	sessionId: string | undefined,
): void {
	deactivateScope(config.projectDir, sessionId);
	log(TAG, "SessionStart: scope deactivated");
	adapter.allow();
}

async function handleUserPromptSubmit(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	sessionId: string | undefined,
): Promise<void> {
	if (!sessionId) {
		log(TAG, "UserPromptSubmit: no session ID, skipping state append");
		adapter.allow();
	}

	// Record state (prompt + fingerprint baseline) — same as `chunk hook state append`
	const key = adapter.stateKey(event);
	if (key) {
		const [head, fingerprint] = await Promise.all([
			getHeadSha(config.projectDir),
			computeFingerprint({ cwd: config.projectDir }),
		]);
		const data: Record<string, unknown> = { ...event.raw };
		if (head) data.head = head;
		if (fingerprint) data.fingerprint = fingerprint;

		appendEvent(config.sentinelDir, config.projectDir, key, data, sessionId);
		log(TAG, `UserPromptSubmit: state appended for ${key}`);
	}

	adapter.allow();
}

function handleSessionEnd(
	config: ResolvedConfig,
	adapter: HookAdapter,
	_event: AgentEvent,
	sessionId: string | undefined,
): void {
	deactivateScope(config.projectDir, sessionId);
	if (sessionId) {
		clearState(config.sentinelDir, config.projectDir, sessionId);
	}
	log(TAG, "SessionEnd: scope deactivated, state cleared");
	adapter.allow();
}

// ---------------------------------------------------------------------------
// Check events (PreToolUse, Stop)
// ---------------------------------------------------------------------------

async function handleCheckEvent(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	canonical: string,
	sessionId: string | undefined,
): Promise<void> {
	// Scope gate — activate/check scope for multi-repo workspaces
	if (!activateScope(config.projectDir, event.raw, sessionId)) {
		log(TAG, `Scope not active for "${config.projectDir}", allowing`);
		adapter.allow();
	}

	// Look up hooks config for this event
	const hookConfig = config.hooks[canonical];
	if (!hookConfig || !hookConfig.checks || hookConfig.checks.length === 0) {
		log(TAG, `No hooks configured for ${canonical}, allowing`);
		adapter.allow();
	}

	const checks = hookConfig!.checks!;

	// Matcher filter (from config, not CLI flag)
	if (hookConfig!.matcher && event.toolName) {
		const re = new RegExp(hookConfig!.matcher);
		if (!re.test(event.toolName)) {
			log(TAG, `Tool "${event.toolName}" does not match matcher "${hookConfig!.matcher}", allowing`);
			adapter.allow();
		}
	}

	// Trigger filter
	if (hookConfig!.trigger) {
		const patterns = getTriggerPatterns(config, hookConfig!.trigger);
		if (patterns && patterns.length > 0 && !matchesTrigger(adapter, event, patterns)) {
			log(TAG, `Event does not match trigger "${hookConfig!.trigger}", allowing`);
			adapter.allow();
		}
	}

	// Stop-event recursion guard — use the first check's limit
	if (canonical === "Stop") {
		const limit = resolveFirstLimit(config, checks);
		guardStopEvent(TAG, adapter, event, limit);
	}

	// Run checks
	if (checks.length === 1) {
		return runSingleCheck(config, adapter, event, checks[0] as string);
	}
	return runMultipleChecks(config, adapter, event, checks);
}

// ---------------------------------------------------------------------------
// Single check — delegate to preEvaluateExec or task check
// ---------------------------------------------------------------------------

async function runSingleCheck(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	checkName: string,
): Promise<void> {
	const exec = getExec(config, checkName);
	if (exec) {
		return runExecCheck(config, adapter, event, checkName, exec);
	}

	const task = getTask(config, checkName);
	if (task) {
		return runTaskCheck(config, adapter, event, checkName);
	}

	log(TAG, `Check "${checkName}" not found in commands or tasks, allowing`);
	adapter.allow();
}

async function runExecCheck(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	name: string,
	exec: ResolvedExec,
): Promise<void> {
	const verdict = await preEvaluateExec(config, adapter, event, exec, { name });

	switch (verdict.kind) {
		case "skip-trigger":
		case "skip-no-changes":
			log(TAG, `${name}: ${verdict.kind}, allowing`);
			adapter.allow();
			break;
		case "pass":
			log(TAG, `${name}: pass, allowing`);
			resetBlockCount(config.sentinelDir, config.projectDir, name);
			adapter.allow();
			break;
		case "missing": {
			const flags = { subcommand: "run" as const, name, noCheck: true };
			const runnerCmd = buildRunnerCommand(flags);
			const reason =
				`Exec "${name}" has no results. Run it first:\n\n` +
				`  ${runnerCmd}\n\n` +
				`Retry after the command completes.`;
			log(TAG, `${name}: missing → block`);
			blockNoCount(TAG, adapter, reason, config.projectDir);
			break;
		}
		case "pending":
			log(TAG, `${name}: pending → block`);
			blockNoCount(
				TAG,
				adapter,
				`Exec "${name}" is still running. Wait for completion before retrying.`,
				config.projectDir,
			);
			break;
		case "fail": {
			const reason = formatFailureReason(
				name,
				verdict.sentinel.command ?? exec.command,
				verdict.sentinel.exitCode ?? 1,
				verdict.sentinel.output ?? "(no output)",
			);
			log(TAG, `${name}: fail → block`);
			blockWithLimit(TAG, adapter, config, name, exec.limit, reason);
			break;
		}
	}
}

async function runTaskCheck(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	name: string,
): Promise<void> {
	const task = getTask(config, name);
	if (!task) {
		adapter.allow();
		return;
	}

	const marker = readMarker(config.projectDir);
	const currentSessionId = marker?.sessionId;
	const sentinel = readTaskResult(config.sentinelDir, config.projectDir, name, currentSessionId);

	if (!sentinel) {
		// Build missing task message
		const resultPath = sentinelPath(config.sentinelDir, config.projectDir, name);
		const instructions = await loadInstructions(
			task.instructions || undefined,
			config.projectDir,
			config.sentinelDir,
			false,
			event,
		);
		const schema = resolveTaskSchemaContent(config.projectDir, task.schema);

		const parts = [`Task "${name}" has no result. Spawn a subagent to complete the task.`, ""];
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
		blockNoCount(TAG, adapter, parts.join("\n"), config.projectDir);
		return;
	}

	if (sentinel.status === "pending") {
		blockNoCount(TAG, adapter, `Task "${name}" is still running. Wait for completion.`, config.projectDir);
		return;
	}

	if (sentinel.status === "pass") {
		resetBlockCount(config.sentinelDir, config.projectDir, name);
		adapter.allow();
		return;
	}

	// fail
	const reason = sentinel.details ?? sentinel.rawResult ?? "(no reason provided)";
	blockWithLimit(TAG, adapter, config, name, task.limit, `Task blocked: ${reason}`);
}

// ---------------------------------------------------------------------------
// Multiple checks — sequential walk with collected verdicts
// ---------------------------------------------------------------------------

async function runMultipleChecks(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	checkNames: string[],
): Promise<void> {
	const issues: { name: string; reason: string; counted: boolean }[] = [];

	for (const name of checkNames) {
		const exec = getExec(config, name);
		if (exec) {
			const verdict = await preEvaluateExec(config, adapter, event, exec, { name });
			const issue = execVerdictToIssue(config, name, exec, verdict);
			if (issue) {
				issues.push(issue);
			} else if (verdict.kind === "pass") {
				resetBlockCount(config.sentinelDir, config.projectDir, name);
			}
			continue;
		}

		const task = getTask(config, name);
		if (task) {
			const marker = readMarker(config.projectDir);
			const sentinel = readTaskResult(config.sentinelDir, config.projectDir, name, marker?.sessionId);
			if (!sentinel) {
				issues.push({ name, reason: `Task "${name}" has no result. Spawn a subagent to complete it.`, counted: false });
			} else if (sentinel.status === "pending") {
				issues.push({ name, reason: `Task "${name}" is still running.`, counted: false });
			} else if (sentinel.status === "fail") {
				const reason = sentinel.details ?? sentinel.rawResult ?? "(no reason)";
				issues.push({ name, reason: `Task "${name}" failed: ${reason}`, counted: true });
			} else {
				resetBlockCount(config.sentinelDir, config.projectDir, name);
			}
			continue;
		}

		log(TAG, `Check "${name}" not found in config, skipping`);
	}

	if (issues.length === 0) {
		log(TAG, "All checks passed → allow");
		adapter.allow();
		return;
	}

	// Build combined block message
	const sections = [`## ${issues.length} check(s) need attention\n`];
	for (const issue of issues) {
		sections.push(`### ${issue.name}\n`, issue.reason, "");
	}
	sections.push("---", "Address all issues above, then retry.");
	const message = sections.join("\n");

	const hasCounted = issues.some((i) => i.counted);
	if (hasCounted) {
		const first = issues.find((i) => i.counted)!;
		const exec = getExec(config, first.name);
		const limit = exec?.limit ?? 0;
		blockWithLimit(TAG, adapter, config, first.name, limit, message);
	} else {
		blockNoCount(TAG, adapter, message, config.projectDir);
	}
}

function execVerdictToIssue(
	config: ResolvedConfig,
	name: string,
	exec: ResolvedExec,
	verdict: Awaited<ReturnType<typeof preEvaluateExec>>,
): { name: string; reason: string; counted: boolean } | undefined {
	switch (verdict.kind) {
		case "skip-trigger":
		case "skip-no-changes":
		case "pass":
			return undefined;
		case "missing": {
			const flags = { subcommand: "run" as const, name, noCheck: true };
			const runnerCmd = buildRunnerCommand(flags);
			return {
				name,
				reason: `Exec "${name}" has no results. Run it first:\n\n  ${runnerCmd}`,
				counted: false,
			};
		}
		case "pending":
			return {
				name,
				reason: `Exec "${name}" is still running. Wait for completion.`,
				counted: false,
			};
		case "fail":
			return {
				name,
				reason: formatFailureReason(
					name,
					verdict.sentinel.command ?? exec.command,
					verdict.sentinel.exitCode ?? 1,
					verdict.sentinel.output ?? "(no output)",
				),
				counted: true,
			};
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function resolveFirstLimit(config: ResolvedConfig, checkNames: string[]): number {
	for (const name of checkNames) {
		const exec = getExec(config, name);
		if (exec && exec.limit > 0) return exec.limit;
		const task = getTask(config, name);
		if (task && task.limit > 0) return task.limit;
	}
	return 0;
}
