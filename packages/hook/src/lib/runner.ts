/**
 * General-purpose cached command execution with content-hash invalidation.
 *
 * This module extracts the execute-and-cache logic from `exec.ts` into a
 * standalone function that can be used outside of the hook system — from
 * the terminal, git hooks, or CI.
 *
 * Usage:
 *   `chunk run <name>`          — Run if stale, report pass/fail
 *   `chunk run <name> --status` — Check cache only, don't execute
 *   `chunk run <name> --force`  — Ignore cache, always run
 *   `chunk run <name> --staged` — Only consider staged files
 */

import { getExec, loadConfig, type ResolvedConfig, type ResolvedExec } from "./config";
import { computeFingerprint, detectChanges, getChangedFiles, substitutePlaceholders } from "./git";
import { initLog, log } from "./log";
import { runCommand } from "./proc";
import type { SentinelData } from "./sentinel";
import { readSentinel, writeSentinel } from "./sentinel";
import { getCleanEnv } from "./shell-env";

const TAG = "runner";

// ---------------------------------------------------------------------------
// Public types
// ---------------------------------------------------------------------------

export type RunResult = {
	status: "pass" | "fail" | "skip-no-changes" | "fresh";
	exitCode: number;
	output: string;
};

export type RunOptions = {
	/** Ignore cache, always run. */
	force?: boolean;
	/** Check cache only, don't execute. */
	status?: boolean;
	/** Only consider staged files for change detection. */
	staged?: boolean;
};

// ---------------------------------------------------------------------------
// Public API
// ---------------------------------------------------------------------------

/**
 * Run a named command from config, caching the result keyed to git state.
 *
 * Flow:
 *   1. Load config (`.chunk/config.yml` → `.chunk/hook/config.yml`)
 *   2. Look up named command in `commands:` (or `execs:`)
 *   3. Compute content fingerprint (`sha256(HEAD + diff)`)
 *   4. Check sentinel: if fresh (hash matches) and not `--force`, return `{ status: "fresh" }`
 *   5. Change detection: if no relevant files changed, return `{ status: "skip-no-changes" }`
 *   6. Run command in clean shell env
 *   7. Write sentinel with content hash
 *   8. Return `{ status: "pass" | "fail", exitCode, output }`
 */
export async function runNamedCommand(
	projectDir: string,
	name: string,
	opts: RunOptions = {},
): Promise<RunResult> {
	const config = loadConfig(projectDir);
	initLog({ projectDir });

	const exec = getExec(config, name);
	if (!exec) {
		return {
			status: "fail",
			exitCode: 1,
			output: `No command configured for "${name}". Define it in .chunk/config.yml under commands:.`,
		};
	}

	log(TAG, `name=${name} force=${opts.force ?? false} status=${opts.status ?? false} staged=${opts.staged ?? false}`);

	// --status: check cache only
	if (opts.status) {
		return checkStatus(config, name, exec, opts);
	}

	// Compute current fingerprint for cache check
	const contentHash = await computeFingerprint({
		cwd: config.projectDir,
		staged: opts.staged,
		fileExt: exec.fileExt,
	});

	// Check if sentinel is fresh (unless --force)
	if (!opts.force) {
		const sentinel = readSentinel(config.sentinelDir, config.projectDir, name);
		if (sentinel && sentinel.status !== "pending" && sentinel.contentHash && sentinel.contentHash === contentHash) {
			log(TAG, `Sentinel is fresh (hash match), returning cached result`);
			return {
				status: sentinel.status === "pass" ? "fresh" : "fail",
				exitCode: sentinel.exitCode ?? (sentinel.status === "pass" ? 0 : 1),
				output: sentinel.output ?? "",
			};
		}
	}

	// Change detection: skip if no relevant files changed
	if (!exec.always && !opts.force) {
		const hasChanges = await detectChanges({
			cwd: config.projectDir,
			fileExt: exec.fileExt,
			staged: opts.staged,
		});
		if (!hasChanges) {
			log(TAG, `No changed files, skipping`);
			// Write a passing skipped sentinel so future checks see it
			const sentinel: SentinelData = {
				status: "pass",
				startedAt: new Date().toISOString(),
				finishedAt: new Date().toISOString(),
				exitCode: 0,
				output: "No changed files. Skipped.",
				skipped: true,
				project: config.projectDir,
				contentHash,
			};
			writeSentinel(config.sentinelDir, config.projectDir, name, sentinel);
			return { status: "skip-no-changes", exitCode: 0, output: "No changed files. Skipped." };
		}
	}

	// Execute the command
	return executeAndCache(config, name, exec, opts, contentHash);
}

// ---------------------------------------------------------------------------
// Internals
// ---------------------------------------------------------------------------

async function checkStatus(
	config: ResolvedConfig,
	name: string,
	exec: ResolvedExec,
	opts: RunOptions,
): Promise<RunResult> {
	const contentHash = await computeFingerprint({
		cwd: config.projectDir,
		staged: opts.staged,
		fileExt: exec.fileExt,
	});

	const sentinel = readSentinel(config.sentinelDir, config.projectDir, name);
	if (!sentinel) {
		return { status: "fail", exitCode: 1, output: "No cached result." };
	}
	if (sentinel.status === "pending") {
		return { status: "fail", exitCode: 1, output: "Command is still running." };
	}
	if (contentHash && sentinel.contentHash && sentinel.contentHash !== contentHash) {
		return { status: "fail", exitCode: 1, output: "Cached result is stale (content changed)." };
	}

	return {
		status: sentinel.status === "pass" ? "pass" : "fail",
		exitCode: sentinel.exitCode ?? (sentinel.status === "pass" ? 0 : 1),
		output: sentinel.output ?? "",
	};
}

async function executeAndCache(
	config: ResolvedConfig,
	name: string,
	exec: ResolvedExec,
	opts: RunOptions,
	contentHash: string,
): Promise<RunResult> {
	const startedAt = new Date().toISOString();

	// Write pending sentinel
	writeSentinel(config.sentinelDir, config.projectDir, name, {
		status: "pending",
		startedAt,
		project: config.projectDir,
	});

	// Build command with placeholder substitution
	let command = exec.command;
	if (command.includes("{{CHANGED_FILES}}") || command.includes("{{CHANGED_PACKAGES}}")) {
		const files = await getChangedFiles({
			cwd: config.projectDir,
			stagedOnly: opts.staged ?? false,
			fileExt: exec.fileExt,
		});
		command = substitutePlaceholders(command, files);
	}

	log(TAG, `Running: ${command}`);

	const cleanEnv = await getCleanEnv();
	const runStart = Date.now();
	const result = await runCommand({
		command,
		cwd: config.projectDir,
		timeout: exec.timeout,
		env: cleanEnv,
		extendEnv: false,
	});
	const elapsedMs = Date.now() - runStart;

	const status = result.exitCode === 0 ? "pass" : "fail";

	// Recompute fingerprint after execution (working tree may have changed)
	const finalHash = await computeFingerprint({
		cwd: config.projectDir,
		staged: opts.staged,
		fileExt: exec.fileExt,
	});

	const sentinel: SentinelData = {
		status,
		startedAt,
		finishedAt: new Date().toISOString(),
		exitCode: result.exitCode,
		command: result.command,
		configuredCommand: exec.command,
		output: result.output,
		project: config.projectDir,
		contentHash: finalHash || contentHash,
	};
	writeSentinel(config.sentinelDir, config.projectDir, name, sentinel);
	log(TAG, `Completed: ${status} (exit ${result.exitCode}, ${elapsedMs}ms)`);

	return {
		status: status as "pass" | "fail",
		exitCode: result.exitCode,
		output: result.output,
	};
}
