/**
 * `chunk hook task` command.
 *
 * Delegates a named task to a subagent and enforces the result.
 *
 * Architecture:
 *   The `check` command blocks with task instructions and a configurable
 *   JSON result schema. The agent spawns a subagent that reads the diff,
 *   applies the instructions, and writes the result to the sentinel path.
 *   On the next check cycle, the CLI reads that result and allows or blocks.
 *
 *   The result contract is `{ "decision": "allow" | "block", "reason": "..." }`
 *   — aligned with the Claude Code hook response format. Everything beyond
 *   `decision` is opaque to the CLI; on a "block" result the full JSON is
 *   fed back to the agent so it can act on structured issues.
 *
 * Subcommands:
 *   - `check <name>` — Check a previously saved task result. On "missing",
 *                       blocks with instructions + schema + result path.
 *
 * Exit codes (aligned with Claude Code hook conventions):
 *   0 — Pass / allow (task succeeded or skipped)
 *   2 — Block / fail (task found issues)
 *   1 — Infra error (missing instructions, cannot write file, etc.)
 */

import type { AgentEvent, HookAdapter } from "../lib/adapter";
import {
	blockNoCount,
	blockWithLimit,
	evaluateSentinel,
	guardStopEvent,
	matchesTrigger,
	resolveTriggerPatterns,
} from "../lib/check";
import type { ResolvedConfig, TaskConfig } from "../lib/config";
import { getTask } from "../lib/config";
import type { Subcommand } from "../lib/env";
import { hasStagedChanges, hasUncommittedChanges } from "../lib/git";
import { log } from "../lib/log";
import type { SentinelData } from "../lib/sentinel";
import { removeSentinel, resetBlockCount, sentinelPath } from "../lib/sentinel";
import { loadInstructions, readTaskResult, resolveTaskSchemaContent } from "../lib/task-result";
import { readMarker } from "./scope";

/** Build a name-qualified tag for log messages. */
function ntag(name: string): string {
	return `task:${name}`;
}

/** CLI flags parsed from argv. */
export type TaskFlags = {
	subcommand: Subcommand;
	name: string;
	instructions?: string;
	schema?: string;
	limit?: number;
	always?: boolean;
	staged?: boolean;
	on?: string;
	trigger?: string;
};

/**
 * Execute the task command.
 *
 * @param config  Resolved config (YAML + env merged).
 * @param adapter Hook adapter for allow/block signaling.
 * @param event   Agent event from stdin.
 * @param flags   CLI flags from argv.
 */
export async function runTask(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	flags: TaskFlags,
): Promise<void> {
	const t = ntag(flags.name);
	log(
		t,
		`subcommand=${flags.subcommand} event=${event.eventName || "(none)"} tool=${event.toolName || "(none)"}`,
	);

	// Resolve task config (from YAML or CLI flags)
	const task = resolveTaskFromFlags(config, flags);

	if (flags.subcommand === "check") {
		return runCheck(t, config, adapter, event, flags, task);
	}
}

// ---------------------------------------------------------------------------
// Resolve task config from flags + YAML
// ---------------------------------------------------------------------------

function resolveTaskFromFlags(config: ResolvedConfig, flags: TaskFlags): Required<TaskConfig> {
	const yamlTask = getTask(config, flags.name);
	return {
		instructions: flags.instructions ?? yamlTask?.instructions ?? "",
		schema: flags.schema ?? yamlTask?.schema ?? "",
		limit: flags.limit ?? yamlTask?.limit ?? 3,
		always: flags.always ?? yamlTask?.always ?? false,
		timeout: yamlTask?.timeout ?? 600,
	};
}

// ---------------------------------------------------------------------------
// check — read result file, emit task decision (hook-facing)
// ---------------------------------------------------------------------------

async function runCheck(
	t: string,
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	flags: TaskFlags,
	task: Required<TaskConfig>,
): Promise<void> {
	const limit = task.limit;
	guardStopEvent(t, adapter, event, limit);

	// Trigger matching — only fire for matching commands
	const triggerPatterns = resolveTriggerPatterns(t, config, flags);
	if (triggerPatterns.length > 0 && !matchesTrigger(adapter, event, triggerPatterns)) {
		const cmd = adapter.commandSummary(event);
		log(t, `Trigger ${JSON.stringify(triggerPatterns)} did not match${cmd}, allowing`);
		adapter.allow();
	}

	// Skip-if-no-changes (default behavior unless --always)
	if (!task.always) {
		const hasChanges = flags.staged
			? await hasStagedChanges(config.projectDir)
			: await hasUncommittedChanges(config.projectDir);
		if (!hasChanges) {
			log(t, "No changed files, allowing");
			adapter.allow();
		}
	}

	const sentinel = readTaskResult(config.sentinelDir, config.projectDir, flags.name);

	// Session-aware staleness: sentinels from a different session are
	// treated as missing so the task re-runs with fresh context.
	const marker = readMarker(config.projectDir);
	const currentSessionId = marker?.sessionId;

	await emitCheckResult(t, config, adapter, event, flags, task, sentinel, currentSessionId);
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/**
 * Evaluate a sentinel and emit the check result (self-consuming).
 */
async function emitCheckResult(
	t: string,
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	flags: TaskFlags,
	task: Required<TaskConfig>,
	sentinel: SentinelData | undefined,
	currentSessionId?: string,
): Promise<never> {
	const result = evaluateSentinel(sentinel, currentSessionId);
	const limit = task.limit;

	// Emit allow/block based on the evaluation
	switch (result.kind) {
		case "missing": {
			const reason = await buildCheckBlockMessage(config, event, flags, task);
			log(t, "Result: missing → action: block (agent must run task first)");
			blockNoCount(t, adapter, reason, config.projectDir);
			break;
		}
		case "pending": {
			// Check whether the pending task has exceeded its timeout.
			const timeout = task.timeout;
			if (sentinel?.startedAt && timeout > 0) {
				const elapsed = (Date.now() - new Date(sentinel.startedAt).getTime()) / 1000;
				if (elapsed > timeout) {
					log(
						t,
						`Result: pending (timed out after ${Math.round(elapsed)}s, limit: ${timeout}s) → action: block`,
					);
					removeSentinel(config.sentinelDir, config.projectDir, flags.name);
					const reason =
						`Task "${flags.name}" timed out after ${Math.round(elapsed)}s ` +
						`(configured timeout: ${timeout}s).\n\n` +
						`The previous subagent may have stalled. Re-run the task.`;
					blockWithLimit(t, adapter, config, flags.name, limit, reason);
					break;
				}
			}
			log(t, "Result: pending → action: block (waiting for task to complete)");
			blockNoCount(
				t,
				adapter,
				`Task "${flags.name}" is still running. Wait for completion before retrying.`,
				config.projectDir,
			);
			break;
		}
		case "pass":
			log(t, "Result: pass → action: allow");
			resetBlockCount(config.sentinelDir, config.projectDir, flags.name);
			removeSentinel(config.sentinelDir, config.projectDir, flags.name);
			adapter.allow();
			break;
		case "fail": {
			const reason = result.sentinel.details ?? "(no reason provided)";
			log(t, `Result: fail — ${reason} → action: block (agent must fix issues)`);
			// Feed the full raw task JSON back so the agent can act on
			// structured issues (file paths, severity, etc.).
			const agentDetails = result.sentinel.rawResult ?? reason;
			blockWithLimit(
				t,
				adapter,
				config,
				flags.name,
				limit,
				`Task blocked: issues found. Fix them before stopping.\n\n${agentDetails}`,
			);
			break;
		}
	}
}

/**
 * Build the block message for the "missing" state.
 *
 * Composes: directive → instructions → output format → retry.
 */
async function buildCheckBlockMessage(
	config: ResolvedConfig,
	event: AgentEvent,
	flags: TaskFlags,
	task: Required<TaskConfig>,
): Promise<string> {
	// Load instructions (best-effort; if missing, fall back to generic prompt)
	const instructions = await loadInstructions(
		task.instructions || undefined,
		config.projectDir,
		config.sentinelDir,
		flags.staged,
		event,
	);

	// Load schema (custom from config/flag, or built-in default)
	const schema = resolveTaskSchemaContent(config.projectDir, task.schema);
	const resultPath = sentinelPath(config.sentinelDir, config.projectDir, flags.name);

	const parts: string[] = [
		"Spawn a subagent to perform the following task on the current changes.",
	];

	if (instructions) {
		parts.push(`Instructions:\n\n${instructions}`);
	} else {
		parts.push("Review the current git diff for correctness, style, and potential issues.");
	}

	parts.push(
		"Output format:\n\n" +
			`Write the result as a single JSON object to: ${resultPath}\n` +
			`Schema:\n${schema}\n\n` +
			"Write ONLY the JSON object — no markdown fences or surrounding text.",
	);

	parts.push("Retry after the subagent completes.");

	return parts.join("\n\n");
}
