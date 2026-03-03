/**
 * Sentinel file management.
 *
 * Sentinels are JSON files stored in a temp directory (outside the repo by
 * default) that record the outcome of command executions and tasks.
 *
 * IDs are deterministic: a hash of `{projectDir, commandName}` so the same
 * command for the same project always resolves to the same sentinel path.
 *
 * ## Coordinated consumption
 *
 * When multiple hooks fire on the same event, each check records its result
 * in a shared coordination file. Sentinels are consumed only when ALL
 * registered commands have passed. The coordination file is protected by a
 * mkdir-based spinlock for concurrency safety.
 */

import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";

import type { CommandName } from "./env";
import { log } from "./log";

const TAG = "sentinel";

/** Status of a sentinel result. */
export type SentinelStatus = "pending" | "pass" | "fail";

/** Shape of the sentinel JSON file. */
export type SentinelData = {
	status: SentinelStatus;
	startedAt: string;
	finishedAt?: string;
	exitCode?: number;
	command?: string;
	output?: string;
	details?: string;
	project?: string;
	skipped?: boolean;
	/** Raw task result JSON — preserved for pass-through to the agent. */
	rawResult?: string;
	/**
	 * Session ID from the scope marker at the time the sentinel was written.
	 *
	 * Used for session-aware staleness: sentinels from a previous session
	 * (different `sessionId`) are treated as stale and ignored.
	 */
	sessionId?: string;
};

/** Compute a deterministic sentinel ID for a project + command combination. */
export function sentinelId(projectDir: string, name: CommandName): string {
	const hash = createHash("sha256").update(`${projectDir}:${name}`).digest("hex").slice(0, 16);
	// Whitelist to alphanumerics, underscores, and dashes to prevent path traversal
	const safeName = name.replace(/[^a-zA-Z0-9_-]/g, "-");
	return `${safeName}-${hash}`;
}

/** Full path to a sentinel file. */
export function sentinelPath(sentinelDir: string, projectDir: string, name: CommandName): string {
	return join(sentinelDir, `${sentinelId(projectDir, name)}.json`);
}

/** Ensure the sentinel directory exists. */
export function ensureSentinelDir(dir: string): void {
	mkdirSync(dir, { recursive: true });
}

/** Write a sentinel file atomically. */
export function writeSentinel(
	sentinelDir: string,
	projectDir: string,
	name: CommandName,
	data: SentinelData,
): string {
	ensureSentinelDir(sentinelDir);
	const path = sentinelPath(sentinelDir, projectDir, name);
	writeFileSync(path, `${JSON.stringify(data, null, 2)}\n`, "utf-8");
	return path;
}

/**
 * Read a sentinel file, returning `undefined` if it doesn't exist or is malformed.
 */
export function readSentinel(
	sentinelDir: string,
	projectDir: string,
	name: CommandName,
): SentinelData | undefined {
	const path = sentinelPath(sentinelDir, projectDir, name);
	if (!existsSync(path)) return undefined;
	try {
		const content = readFileSync(path, "utf-8");
		return JSON.parse(content) as SentinelData;
	} catch {
		return undefined;
	}
}

/** Remove a sentinel file if it exists. */
export function removeSentinel(sentinelDir: string, projectDir: string, name: CommandName): void {
	const path = sentinelPath(sentinelDir, projectDir, name);
	if (existsSync(path)) rmSync(path);
}

// ---------------------------------------------------------------------------
// Block counter — tracks consecutive check-blocks for --limit
// ---------------------------------------------------------------------------

/** Path to the block-counter file for a command. */
function blockCountPath(sentinelDir: string, projectDir: string, name: CommandName): string {
	return join(sentinelDir, `${sentinelId(projectDir, name)}.blocks`);
}

/** Read the current block count (0 if no counter file exists). */
export function readBlockCount(sentinelDir: string, projectDir: string, name: CommandName): number {
	const path = blockCountPath(sentinelDir, projectDir, name);
	if (!existsSync(path)) return 0;
	try {
		return parseInt(readFileSync(path, "utf-8").trim(), 10) || 0;
	} catch {
		return 0;
	}
}

/** Increment and persist the block counter. Returns the new count. */
export function incrementBlockCount(
	sentinelDir: string,
	projectDir: string,
	name: CommandName,
): number {
	ensureSentinelDir(sentinelDir);
	const count = readBlockCount(sentinelDir, projectDir, name) + 1;
	writeFileSync(blockCountPath(sentinelDir, projectDir, name), String(count), "utf-8");
	return count;
}

/** Reset (remove) the block counter. Call on pass or when re-run. */
export function resetBlockCount(sentinelDir: string, projectDir: string, name: CommandName): void {
	const path = blockCountPath(sentinelDir, projectDir, name);
	if (existsSync(path)) rmSync(path);
}

// ---------------------------------------------------------------------------
// Coordinated consumption — multi-command sentinel coordination
// ---------------------------------------------------------------------------

/** Per-command check result stored in the coordination file. */
export type CheckResult = "pass" | "fail" | "missing" | "pending";

/** Shape of the coordination JSON file. */
export type CoordinationData = {
	results: Record<CommandName, CheckResult>;
	/**
	 * ISO timestamp when all commands first passed.
	 * Consumption is delayed until this timestamp is older than the delay threshold.
	 */
	readyAt?: string;
};

/** Compute a deterministic coordination file ID for a project. */
export function coordinationId(projectDir: string): string {
	const hash = createHash("sha256").update(`${projectDir}:coordination`).digest("hex").slice(0, 16);
	return `coord-${hash}`;
}

/** Full path to a project's coordination file. */
export function coordinationPath(sentinelDir: string, projectDir: string): string {
	return join(sentinelDir, `${coordinationId(projectDir)}.json`);
}

// ---------------------------------------------------------------------------
// Lock — simple spinlock using mkdir atomicity
// ---------------------------------------------------------------------------

const LOCK_TIMEOUT_MS = 5000;
const LOCK_RETRY_MS = 10;

/** Path to the lock directory for a given file. */
function lockPath(filePath: string): string {
	return `${filePath}.lock`;
}

/**
 * Acquire an exclusive lock on a file.
 * Uses `mkdirSync` with `{ recursive: false }` as an atomic test-and-set.
 * Returns a release function.
 */
export function acquireLock(filePath: string): () => void {
	const lock = lockPath(filePath);
	const deadline = Date.now() + LOCK_TIMEOUT_MS;

	while (Date.now() < deadline) {
		try {
			mkdirSync(lock, { recursive: false });
			return () => {
				try {
					rmSync(lock, { recursive: true, force: true });
				} catch {
					// Best-effort cleanup
				}
			};
		} catch {
			// Lock held by another process — spin
			Bun.sleepSync(LOCK_RETRY_MS);
		}
	}

	// Timeout — force-break the stale lock and retry once
	log(TAG, `Lock timeout, breaking stale lock: ${lock}`);
	try {
		rmSync(lock, { recursive: true, force: true });
		mkdirSync(lock, { recursive: false });
		return () => {
			try {
				rmSync(lock, { recursive: true, force: true });
			} catch {
				// Best-effort cleanup
			}
		};
	} catch {
		// Last resort: proceed without lock (better than deadlock)
		log(TAG, "Failed to acquire lock, proceeding unlocked");
		return () => {
			/* no-op: lock not held */
		};
	}
}

// ---------------------------------------------------------------------------
// Coordination file CRUD
// ---------------------------------------------------------------------------

/** Read the coordination file, returning empty data if missing/malformed. */
export function readCoordination(sentinelDir: string, projectDir: string): CoordinationData {
	const path = coordinationPath(sentinelDir, projectDir);
	if (!existsSync(path)) return { results: {} };
	try {
		const content = readFileSync(path, "utf-8");
		const data = JSON.parse(content) as CoordinationData;
		if (!data.results || typeof data.results !== "object") {
			return { results: {} };
		}
		return data;
	} catch {
		return { results: {} };
	}
}

/** Write the coordination file. */
export function writeCoordination(
	sentinelDir: string,
	projectDir: string,
	data: CoordinationData,
): void {
	ensureSentinelDir(sentinelDir);
	const path = coordinationPath(sentinelDir, projectDir);
	writeFileSync(path, `${JSON.stringify(data, null, 2)}\n`, "utf-8");
}

/** Remove the coordination file. */
export function clearCoordination(sentinelDir: string, projectDir: string): void {
	const path = coordinationPath(sentinelDir, projectDir);
	if (existsSync(path)) rmSync(path, { force: true });
}

// ---------------------------------------------------------------------------
// Core coordination logic
// ---------------------------------------------------------------------------

/** Default delay before consuming sentinels (ms). */
const DEFAULT_CONSUME_DELAY_MS = 1000;

/**
 * Get the effective consume delay from env or default.
 * Allows `CHUNK_HOOK_CONSUME_DELAY_MS=0` for immediate consumption in tests.
 */
function getConsumeDelayMs(): number {
	const envVal = process.env.CHUNK_HOOK_CONSUME_DELAY_MS;
	if (envVal !== undefined) {
		const parsed = parseInt(envVal, 10);
		if (!Number.isNaN(parsed) && parsed >= 0) {
			return parsed;
		}
	}
	return DEFAULT_CONSUME_DELAY_MS;
}

/** Options for recordAndTryConsume. */
export type CoordinationOptions = {
	/**
	 * Delay in milliseconds before consuming sentinels after all pass.
	 * Default: 1000ms (or CHUNK_HOOK_CONSUME_DELAY_MS env var).
	 */
	consumeDelayMs?: number;
};

/**
 * Record a command's check result and consume all sentinels if every
 * command has passed.
 */
export function recordAndTryConsume(
	sentinelDir: string,
	projectDir: string,
	commandName: CommandName,
	result: CheckResult,
	options: CoordinationOptions = {},
): boolean {
	const { consumeDelayMs = getConsumeDelayMs() } = options;
	const coordFile = coordinationPath(sentinelDir, projectDir);
	const release = acquireLock(coordFile);

	try {
		const coord = readCoordination(sentinelDir, projectDir);

		// Record this command's result
		coord.results[commandName] = result;

		// Check if all entries are "pass"
		const entries = Object.entries(coord.results);
		const allPass = entries.length > 0 && entries.every(([, v]) => v === "pass");

		if (!allPass) {
			// Not all pass — clear readyAt and save
			delete coord.readyAt;
			log(
				TAG,
				`Coordination update: ${commandName}=${result}, ` +
					`waiting (${JSON.stringify(coord.results)})`,
			);
			writeCoordination(sentinelDir, projectDir, coord);
			return false;
		}

		// All pass — check if we should consume or wait
		const now = Date.now();

		// If delay is 0, consume immediately (used in tests)
		if (consumeDelayMs <= 0) {
			log(
				TAG,
				`Coordination: all passed (immediate), consuming (${JSON.stringify(coord.results)})`,
			);
			// Fall through to consumption
		} else if (!coord.readyAt) {
			// First time all pass — set readyAt and wait for delay
			coord.readyAt = new Date(now).toISOString();
			log(
				TAG,
				`Coordination: all passed, starting ${consumeDelayMs}ms delay ` +
					`(${JSON.stringify(coord.results)})`,
			);
			writeCoordination(sentinelDir, projectDir, coord);
			return false;
		} else {
			// Check if delay has elapsed
			const readyTime = new Date(coord.readyAt).getTime();
			const elapsed = now - readyTime;

			if (elapsed < consumeDelayMs) {
				log(
					TAG,
					`Coordination: all passed, waiting ${consumeDelayMs - elapsed}ms more ` +
						`(${JSON.stringify(coord.results)})`,
				);
				return false;
			}

			log(
				TAG,
				`Coordination: delay elapsed (${elapsed}ms), consuming ` +
					`(${JSON.stringify(coord.results)})`,
			);
		}

		// Ready to consume all sentinels
		for (const [name] of entries) {
			removeSentinel(sentinelDir, projectDir, name);
			log(TAG, `Consumed sentinel: ${name}`);
		}

		// Clear the coordination file for the next cycle
		clearCoordination(sentinelDir, projectDir);

		return true;
	} finally {
		release();
	}
}

/**
 * Clear a single command's entry from the coordination file.
 * Called when a command is re-run to invalidate the previous cycle's result.
 */
export function clearCoordinationEntry(
	sentinelDir: string,
	projectDir: string,
	commandName: CommandName,
): void {
	const coordFile = coordinationPath(sentinelDir, projectDir);
	const release = acquireLock(coordFile);

	try {
		const coord = readCoordination(sentinelDir, projectDir);
		if (commandName in coord.results) {
			delete coord.results[commandName];
			if (Object.keys(coord.results).length === 0) {
				clearCoordination(sentinelDir, projectDir);
			} else {
				writeCoordination(sentinelDir, projectDir, coord);
			}
		}
	} finally {
		release();
	}
}
