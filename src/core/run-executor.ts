/**
 * Command execution with caching keyed to git state.
 *
 * Sentinel files live in `$TMPDIR/chunk-run/cache/<project-hash>/`.
 * Each sentinel is `<name>.json` containing status, exitCode, output,
 * contentHash, and timestamp.
 *
 * Caching strategy matches the hook package's runner:
 * - Content hash = SHA-256 of `{HEAD_SHA}\n{git diff HEAD}`
 * - `git diff HEAD` captures both staged and unstaged changes
 * - Output is combined stdout+stderr, truncated to last 50 KB
 */

import { execFileSync, execSync } from "node:child_process";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const MAX_OUTPUT_BYTES = 50 * 1024;

export type RunResult = {
	status: "pass" | "fail" | "cached";
	exitCode: number;
	output: string;
};

type SentinelData = {
	status: "pass" | "fail";
	exitCode: number;
	output: string;
	contentHash: string;
	timestamp: number;
};

function safeName(name: string): string {
	return name.replace(/[^a-zA-Z0-9]/g, "-");
}

function projectHash(projectDir: string): string {
	return createHash("sha256").update(resolve(projectDir)).digest("hex").slice(0, 16);
}

function cacheDir(projectDir: string): string {
	return join(tmpdir(), "chunk-run", "cache", projectHash(projectDir));
}

function sentinelPath(projectDir: string, name: string): string {
	return join(cacheDir(projectDir), `${safeName(name)}.json`);
}

function computeContentHash(projectDir: string): string {
	const hash = createHash("sha256");

	try {
		const head = execFileSync("git", ["rev-parse", "HEAD"], {
			cwd: projectDir,
			encoding: "utf-8",
			stdio: ["ignore", "pipe", "ignore"],
		}).trim();
		hash.update(head);
	} catch {
		hash.update("no-head");
	}

	hash.update("\n");

	try {
		const diff = execFileSync("git", ["diff", "HEAD"], {
			cwd: projectDir,
			encoding: "utf-8",
			stdio: ["ignore", "pipe", "ignore"],
		});
		hash.update(diff);
	} catch {
		// no diff available
	}

	return hash.digest("hex");
}

function truncateOutput(output: string): string {
	if (Buffer.byteLength(output, "utf-8") <= MAX_OUTPUT_BYTES) return output;
	const buf = Buffer.from(output, "utf-8");
	return buf.subarray(buf.length - MAX_OUTPUT_BYTES).toString("utf-8");
}

function readSentinel(projectDir: string, name: string): SentinelData | undefined {
	const path = sentinelPath(projectDir, name);
	if (!existsSync(path)) return undefined;
	try {
		return JSON.parse(readFileSync(path, "utf-8")) as SentinelData;
	} catch {
		return undefined;
	}
}

function writeSentinel(projectDir: string, name: string, data: SentinelData): void {
	const dir = cacheDir(projectDir);
	if (!existsSync(dir)) mkdirSync(dir, { recursive: true });
	writeFileSync(sentinelPath(projectDir, name), JSON.stringify(data));
}

export function checkCache(projectDir: string, name: string): RunResult | undefined {
	const sentinel = readSentinel(projectDir, name);
	if (!sentinel) return undefined;

	const currentHash = computeContentHash(projectDir);
	if (sentinel.contentHash !== currentHash) return undefined;

	return {
		status: "cached",
		exitCode: sentinel.exitCode,
		output: sentinel.output,
	};
}

export function executeCommand(
	projectDir: string,
	name: string,
	command: string,
	opts: { force?: boolean; timeout?: number } = {},
): RunResult {
	if (!opts.force) {
		const cached = checkCache(projectDir, name);
		if (cached) return cached;
	}

	const timeout = (opts.timeout ?? 300) * 1000;
	let output: string;
	let exitCode: number;

	try {
		output = execSync(command, {
			cwd: projectDir,
			encoding: "utf-8",
			timeout,
			stdio: ["ignore", "pipe", "pipe"],
			maxBuffer: 10 * 1024 * 1024,
		});
		exitCode = 0;
	} catch (err: unknown) {
		const execErr = err as { status?: number; stdout?: string; stderr?: string };
		exitCode = execErr.status ?? 1;
		output = (execErr.stdout ?? "") + (execErr.stderr ?? "");
	}

	output = truncateOutput(output);

	const contentHash = computeContentHash(projectDir);
	const status = exitCode === 0 ? "pass" : "fail";

	writeSentinel(projectDir, name, {
		status,
		exitCode,
		output,
		contentHash,
		timestamp: Date.now(),
	});

	return { status, exitCode, output };
}
