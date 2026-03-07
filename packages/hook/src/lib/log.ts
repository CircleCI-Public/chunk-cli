/**
 * Lightweight logger.
 *
 * Writes timestamped entries to a file under `$TMPDIR/chunk-hook/logs/`
 * by default. Override with `CHUNK_HOOK_LOG_DIR` env var.
 * Also emits to stderr when `CHUNK_HOOK_VERBOSE` is set.
 */

import { createHash } from "node:crypto";
import { appendFileSync, mkdirSync } from "node:fs";
import { join } from "node:path";

import { env, getProjectDir } from "./env";

let logFilePath: string | undefined;

/**
 * Initialise the log file path. Call once at startup.
 *
 * @param opts.projectDir – explicit project root (e.g. from hook payload
 *   `cwd`). Falls back to `CLAUDE_PROJECT_DIR` → `process.cwd()`.
 * @param opts.baseDir – log directory override (default: `$CHUNK_HOOK_LOG_DIR`
 *   or `$TMPDIR/chunk-hook/logs`).
 */
export function initLog(opts?: { projectDir?: string; baseDir?: string }): void {
	const dir =
		opts?.baseDir ??
		env("CHUNK_HOOK_LOG_DIR") ??
		join(process.env.TMPDIR ?? "/tmp", "chunk-hook", "logs");
	const resolvedProjectDir = opts?.projectDir ?? getProjectDir();
	const hash = createHash("sha256").update(resolvedProjectDir).digest("hex").slice(0, 16);
	mkdirSync(join(dir, hash), { recursive: true });
	logFilePath = join(dir, hash, "chunk-hook.log");
}

/** Append a timestamped log entry. */
export function log(tag: string, message: string): void {
	const ts = new Date().toISOString();
	const line = `[${ts}] [${tag}] ${message}\n`;
	if (logFilePath) {
		try {
			appendFileSync(logFilePath, line, "utf-8");
		} catch {
			// best-effort
		}
	}
	if (env("CHUNK_HOOK_VERBOSE") !== undefined) {
		process.stderr.write(line);
	}
}

/**
 * Log only when `CHUNK_HOOK_VERBOSE` is set.
 *
 * Use for large diagnostic payloads (e.g. full stdin JSON) that are
 * invaluable during debugging but too noisy for regular log files.
 */
export function logVerbose(tag: string, message: string): void {
	if (env("CHUNK_HOOK_VERBOSE") === undefined) return;
	log(tag, message);
}
