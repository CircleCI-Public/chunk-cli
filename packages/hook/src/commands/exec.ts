/**
 * `chunk hook exec` command.
 *
 * Executes a shell command and enforces the result.
 *
 * Subcommands:
 *   - `run`       — Run command, save result, check result → fail on failure.
 *   - `run --no-check` — Run command, save result, skip the check. Always exits 0.
 *   - `check`     — Deferred check: read a saved result and fail on failure.
 *
 * Exit codes:
 *   0 — Pass / allow
 *   2 — Block / fail
 *   1 — Infra error
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
import type { ResolvedConfig, ResolvedExec } from "../lib/config";
import { getExec } from "../lib/config";
import { detectChanges, getChangedFiles, substitutePlaceholders } from "../lib/git";
import { log } from "../lib/log";
import { runCommand } from "../lib/proc";
import type { SentinelData } from "../lib/sentinel";
import { readSentinel, removeSentinel, resetBlockCount, writeSentinel } from "../lib/sentinel";
import { shellQuote } from "../lib/shell-env";
import { readMarker } from "./scope";

/** Build a name-qualified tag for log messages. */
function ntag(name: string): string {
	return `exec:${name}`;
}

/** CLI flags parsed from argv for the exec command. */
export type ExecFlags = {
	subcommand: "check" | "run";
	name: string;
	cmd?: string;
	timeout?: number;
	fileExt?: string;
	staged?: boolean;
	always?: boolean;
	noCheck?: boolean;
	on?: string;
	trigger?: string;
	limit?: number;
};

/**
 * Execute the exec command.
 */
export async function runExec(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	flags: ExecFlags,
): Promise<void> {
	const t = ntag(flags.name);
	log(
		t,
		`subcommand=${flags.subcommand} event=${event.eventName || "(none)"} tool=${event.toolName || "(none)"}`,
	);

	// Resolve exec config (from YAML or CLI flags)
	const exec = resolveExecFromFlags(config, flags);

	switch (flags.subcommand) {
		case "check":
			return runCheck(config, adapter, event, flags, exec, t);
		case "run":
			if (flags.noCheck) {
				return runNoCheck(config, flags, exec, t);
			}
			return runFull(config, adapter, event, flags, exec, t);
	}
}

// ---------------------------------------------------------------------------
// Resolve exec config from flags + YAML
// ---------------------------------------------------------------------------

function resolveExecFromFlags(config: ResolvedConfig, flags: ExecFlags): ResolvedExec {
	const yamlExec = getExec(config, flags.name);
	return {
		command:
			flags.cmd ?? yamlExec?.command ?? `echo 'No command configured for exec: ${flags.name}'`,
		fileExt: flags.fileExt ?? yamlExec?.fileExt ?? "",
		always: flags.always ?? yamlExec?.always ?? false,
		timeout: flags.timeout ?? yamlExec?.timeout ?? 300,
		limit: flags.limit ?? yamlExec?.limit ?? 0,
	};
}

// ---------------------------------------------------------------------------
// check — read result file, emit decision (hook-facing)
// ---------------------------------------------------------------------------

async function runCheck(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	flags: ExecFlags,
	exec: ResolvedExec,
	t: string,
): Promise<void> {
	const limit = flags.limit ?? exec.limit;
	guardStopEvent(t, adapter, event, limit);

	// Trigger matching
	const triggerPatterns = resolveTriggerPatterns(t, config, flags);
	if (triggerPatterns.length > 0 && !matchesTrigger(adapter, event, triggerPatterns)) {
		const cmd = adapter.commandSummary(event);
		log(t, `Trigger ${JSON.stringify(triggerPatterns)} did not match${cmd}, allowing`);
		adapter.allow();
	}

	// Skip-if-no-changes
	if (!exec.always) {
		const hasChanges = await detectChanges({
			cwd: config.projectDir,
			fileExt: exec.fileExt,
			staged: flags.staged,
		});
		if (!hasChanges) {
			log(t, "No changed files, allowing");
			adapter.allow();
		}
	}

	const sentinel = readSentinel(config.sentinelDir, config.projectDir, flags.name);

	// Session-aware staleness: sentinels from a different session are
	// treated as missing so the command re-runs with fresh context.
	const marker = readMarker(config.projectDir);
	const currentSessionId = marker?.sessionId;

	emitCheckResult(config, adapter, flags, exec, sentinel, t, currentSessionId);
}

/**
 * Evaluate a consumed sentinel and emit the check result (self-consuming).
 */
function emitCheckResult(
	config: ResolvedConfig,
	adapter: HookAdapter,
	flags: ExecFlags,
	exec: ResolvedExec,
	sentinel: SentinelData | undefined,
	t: string,
	currentSessionId?: string,
): never {
	const result = evaluateSentinel(sentinel, currentSessionId);
	const limit = flags.limit ?? exec.limit;
	const name = flags.name;

	switch (result.kind) {
		case "missing": {
			const runnerCmd = buildRunnerCommand(flags);
			const reason =
				`Exec "${name}" has no results. Run it first:\n\n` +
				`  ${runnerCmd}\n\n` +
				`Retry after the command completes.`;
			log(t, "Result: missing → action: block (agent must run command first)");
			blockNoCount(t, adapter, reason, config.projectDir);
			break;
		}
		case "pending": {
			const timeout = flags.timeout ?? exec.timeout;
			if (sentinel?.startedAt && timeout > 0) {
				const elapsed = (Date.now() - new Date(sentinel.startedAt).getTime()) / 1000;
				if (elapsed > timeout) {
					log(
						t,
						`Result: pending (timed out after ${Math.round(elapsed)}s, limit: ${timeout}s) → action: block`,
					);
					removeSentinel(config.sentinelDir, config.projectDir, name);
					const runnerCmd = buildRunnerCommand(flags);
					const reason =
						`Exec "${name}" timed out after ${Math.round(elapsed)}s ` +
						`(configured timeout: ${timeout}s).\n\n` +
						`The previous run may have an issue (infinite loop, deadlock, etc.). ` +
						`Investigate and re-run:\n\n` +
						`  ${runnerCmd}`;
					blockWithLimit(t, adapter, config, name, limit, reason);
					break;
				}
			}
			log(t, "Result: pending → action: block (waiting for command to complete)");
			blockNoCount(
				t,
				adapter,
				`Exec "${name}" is still running. Wait for completion before retrying.`,
				config.projectDir,
			);
			break;
		}
		case "pass":
			log(t, "Result: pass → action: allow");
			resetBlockCount(config.sentinelDir, config.projectDir, name);
			removeSentinel(config.sentinelDir, config.projectDir, name);
			adapter.allow();
			break;
		case "fail": {
			const reason = formatFailureReason(
				name,
				result.sentinel.command ?? exec.command,
				result.sentinel.exitCode ?? 1,
				result.sentinel.output ?? "(no output)",
			);
			log(
				t,
				`Result: fail (exit ${result.sentinel.exitCode}) → action: block (agent must fix and re-run)`,
			);
			blockWithLimit(t, adapter, config, name, limit, reason);
			break;
		}
	}
}

/**
 * Evaluate a sentinel and emit the result (direct, no gate).
 * Used by `runFull` after executing a command.
 */
function emitSentinelResult(
	config: ResolvedConfig,
	adapter: HookAdapter,
	flags: ExecFlags,
	exec: ResolvedExec,
	sentinel: SentinelData | undefined,
	t: string,
): never {
	const result = evaluateSentinel(sentinel);
	const limit = flags.limit ?? exec.limit;
	const name = flags.name;

	switch (result.kind) {
		case "missing": {
			const runnerCmd = buildRunnerCommand(flags);
			const reason =
				`Exec "${name}" has no results. Run it first:\n\n` +
				`  ${runnerCmd}\n\n` +
				`Retry after the command completes.`;
			log(t, "Result: missing (unexpected in direct path) → action: block");
			blockNoCount(t, adapter, reason, config.projectDir);
			break;
		}
		case "pending":
			log(t, "Result: pending (unexpected in direct path) → action: block");
			blockNoCount(
				t,
				adapter,
				`Exec "${name}" is still running. Wait for completion before retrying.`,
				config.projectDir,
			);
			break;
		case "pass":
			log(t, "Result: pass → action: allow");
			resetBlockCount(config.sentinelDir, config.projectDir, name);
			adapter.allow();
			break;
		case "fail": {
			const reason = formatFailureReason(
				name,
				result.sentinel.command ?? exec.command,
				result.sentinel.exitCode ?? 1,
				result.sentinel.output ?? "(no output)",
			);
			log(
				t,
				`Result: fail (exit ${result.sentinel.exitCode}) → action: block (agent must fix and re-run)`,
			);
			blockWithLimit(t, adapter, config, name, limit, reason);
			break;
		}
	}
}

// ---------------------------------------------------------------------------
// run --no-check
// ---------------------------------------------------------------------------

async function runNoCheck(
	config: ResolvedConfig,
	flags: ExecFlags,
	exec: ResolvedExec,
	t: string,
): Promise<void> {
	const { sentinel } = await executeExec(config, flags, exec, t);

	if (sentinel.skipped) {
		log(t, "No changed files, skipped");
	} else if (sentinel.status === "pass") {
		log(t, "Result: pass (no-check mode)");
	} else {
		log(t, `Result: fail (exit ${sentinel.exitCode}, no-check mode)`);
	}
	process.exit(0);
}

// ---------------------------------------------------------------------------
// run (default) — run command + write result + output decision
// ---------------------------------------------------------------------------

async function runFull(
	config: ResolvedConfig,
	adapter: HookAdapter,
	event: AgentEvent,
	flags: ExecFlags,
	exec: ResolvedExec,
	t: string,
): Promise<void> {
	const limit = flags.limit ?? exec.limit;
	guardStopEvent(t, adapter, event, limit);

	// Trigger matching (align with check semantics)
	const triggerPatterns = resolveTriggerPatterns(t, config, flags);
	if (triggerPatterns.length > 0 && !matchesTrigger(adapter, event, triggerPatterns)) {
		const cmd = adapter.commandSummary(event);
		log(t, `Trigger ${JSON.stringify(triggerPatterns)} did not match${cmd}, allowing`);
		adapter.allow();
	}

	const { sentinel } = await executeExec(config, flags, exec, t);
	emitSentinelResult(config, adapter, flags, exec, sentinel, t);
}

// ---------------------------------------------------------------------------
// Shared exec execution core
// ---------------------------------------------------------------------------

type ExecResult = {
	sentinel: SentinelData;
};

async function executeExec(
	config: ResolvedConfig,
	flags: ExecFlags,
	exec: ResolvedExec,
	t: string,
): Promise<ExecResult> {
	const startedAt = new Date().toISOString();

	// Read the current session ID from the scope marker so it can be
	// stored in sentinels for session-aware staleness detection.
	const sessionId = readMarker(config.projectDir)?.sessionId;

	// Skip-if-no-changes — return a synthetic sentinel without writing it to disk.
	// Writing a "pass" sentinel here would be dangerous: an agent could run the
	// command while the repo is clean to farm a passing sentinel, then introduce
	// bugs and commit — the stale "pass" sentinel would allow the commit.
	// The check and sync paths have their own independent `detectChanges`
	// short-circuits, so they never read a skipped sentinel.
	if (!exec.always) {
		const hasChanges = await detectChanges({
			cwd: config.projectDir,
			fileExt: exec.fileExt,
			staged: flags.staged,
		});
		if (!hasChanges) {
			const sentinel: SentinelData = {
				status: "pass",
				startedAt,
				finishedAt: new Date().toISOString(),
				exitCode: 0,
				output: "No changed files. Skipped.",
				skipped: true,
				project: config.projectDir,
				sessionId,
			};
			return { sentinel };
		}
	}

	// Write pending result
	writeSentinel(config.sentinelDir, config.projectDir, flags.name, {
		status: "pending",
		startedAt,
		project: config.projectDir,
		sessionId,
	});

	const command = await buildCommand(config, exec, flags);
	log(t, `Running: ${command}`);

	const runStart = Date.now();
	const result = await runCommand({
		command,
		cwd: config.projectDir,
		timeout: exec.timeout,
	});
	const elapsedMs = Date.now() - runStart;

	const status = result.exitCode === 0 ? "pass" : "fail";
	const sentinel: SentinelData = {
		status,
		startedAt,
		finishedAt: new Date().toISOString(),
		exitCode: result.exitCode,
		command: result.command,
		output: result.output,
		project: config.projectDir,
		sessionId,
	};
	writeSentinel(config.sentinelDir, config.projectDir, flags.name, sentinel);
	log(t, `Completed: ${status} (exit ${result.exitCode}, ${elapsedMs}ms)`);

	return { sentinel };
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Build the shell command with placeholder substitution. */
async function buildCommand(
	config: ResolvedConfig,
	exec: ResolvedExec,
	flags: ExecFlags,
): Promise<string> {
	let command = flags.cmd ?? exec.command;

	if (command.includes("{{CHANGED_FILES}}") || command.includes("{{CHANGED_PACKAGES}}")) {
		const files = await getChangedFiles({
			cwd: config.projectDir,
			stagedOnly: flags.staged ?? false,
			fileExt: exec.fileExt,
		});
		command = substitutePlaceholders(command, files);
	}

	return command;
}

/**
 * Build the `run --no-check` command string for the "missing" block message.
 */
export function buildRunnerCommand(flags: ExecFlags): string {
	const parts = ["chunk hook exec run", flags.name, "--no-check"];
	if (flags.cmd) parts.push(`--cmd '${shellQuote(flags.cmd)}'`);
	if (flags.timeout !== undefined) parts.push(`--timeout ${flags.timeout}`);
	if (flags.fileExt) parts.push(`--file-ext '${shellQuote(flags.fileExt)}'`);
	if (flags.staged) parts.push("--staged");
	if (flags.always) parts.push("--always");
	return parts.join(" ");
}

/** Format a concise failure reason for the agent. */
export function formatFailureReason(
	name: string,
	command: string,
	exitCode: number,
	output: string,
): string {
	const header =
		exitCode === 124
			? `Exec "${name}" timed out (command: ${command}).`
			: `Exec "${name}" failed (exit ${exitCode}, command: ${command}).`;
	return `${header} Fix the issues and retry.\n\nOutput:\n${output}`;
}
