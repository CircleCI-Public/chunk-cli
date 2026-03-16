/**
 * Git helpers for detecting changed files.
 *
 * Used by exec and task commands for placeholder substitution
 * (`{{CHANGED_FILES}}`, `{{CHANGED_PACKAGES}}`) and skip-if-no-changes logic.
 */

import { createHash } from "node:crypto";
import type { RunOptions, RunResult } from "./proc";
import { runCommand as _defaultRunCommand } from "./proc";

// Module-level function reference — replaceable for testing without
// polluting the shared module registry via mock.module().
let _run: (opts: RunOptions) => Promise<RunResult> = _defaultRunCommand;

/** @internal For testing only. Override the runCommand implementation. */
export function _setRunCommand(fn: (opts: RunOptions) => Promise<RunResult>): void {
	_run = fn;
}

/** @internal For testing only. Reset to the real runCommand. */
export function _resetRunCommand(): void {
	_run = _defaultRunCommand;
}

/** Options for `getChangedFiles`. */
export type ChangedFilesOptions = {
	/** Working directory (repo root). */
	cwd?: string;
	/** Only consider staged files (for pre-commit checks). */
	stagedOnly?: boolean;
	/** Filter to files matching this extension (e.g., `.go`, `.ts`). */
	fileExt?: string;
};

/**
 * Get the list of changed files in the current repository.
 *
 * When `stagedOnly` is true, uses `git diff --cached --diff-filter=ACMR`
 * to return only added, copied, modified, or renamed files (excludes
 * deletions — deleted paths don't exist on disk).
 *
 * When `stagedOnly` is false, uses `git status --porcelain` to catch
 * staged, unstaged, and untracked files, filtering out deletions.
 */
export async function getChangedFiles(opts: ChangedFilesOptions = {}): Promise<string[]> {
	const { cwd = process.cwd(), stagedOnly = false, fileExt = "" } = opts;

	const command = stagedOnly
		? "git diff --cached --name-only --diff-filter=ACMR"
		: "git status --porcelain -uall" +
			" | grep -vE '^D.|^.D'" +
			" | cut -c4-" +
			" | sed 's/.* -> //'";

	const result = await _run({ command, cwd, timeout: 30 });
	if (result.exitCode !== 0) return [];

	let files = result.output
		.split("\n")
		.map((f) => f.trim())
		.map((f) => (f.startsWith('"') && f.endsWith('"') ? f.slice(1, -1) : f))
		.filter(Boolean);

	if (fileExt) {
		const ext = fileExt.startsWith(".") ? fileExt : `.${fileExt}`;
		files = files.filter((f) => f.endsWith(ext));
	}

	return files;
}

/**
 * Deduplicate parent directories from a list of file paths.
 * Useful for Go-style `./pkg/...` test targeting.
 */
export function getChangedPackages(files: string[]): string[] {
	const dirs = new Set<string>();
	for (const file of files) {
		const parts = file.split("/");
		parts.pop(); // remove filename
		const dir = parts.length === 0 ? "./" : `./${parts.join("/")}`;
		dirs.add(dir);
	}
	return [...dirs].sort();
}

/**
 * Shell-quote a single token for safe inclusion in `sh -c` commands.
 * Wraps in single quotes and escapes embedded single quotes.
 */
function shellQuote(s: string): string {
	return `'${s.replace(/'/g, "'\\''")}'`;
}

/**
 * Replace `{{CHANGED_FILES}}` and `{{CHANGED_PACKAGES}}` placeholders in a
 * command string with the actual values.
 *
 * Each file path and package path is shell-quoted to prevent command
 * injection from repository-controlled file names containing metacharacters.
 */
export function substitutePlaceholders(command: string, files: string[]): string {
	let result = command;
	if (result.includes("{{CHANGED_FILES}}")) {
		result = result.replace("{{CHANGED_FILES}}", files.map(shellQuote).join(" "));
	}
	if (result.includes("{{CHANGED_PACKAGES}}")) {
		const pkgs = getChangedPackages(files);
		result = result.replace("{{CHANGED_PACKAGES}}", pkgs.map(shellQuote).join(" "));
	}
	return result;
}

/**
 * Check whether there are uncommitted changes in the repository.
 * Uses `git status --porcelain` which covers staged, unstaged, and untracked files.
 */
export async function hasUncommittedChanges(cwd?: string): Promise<boolean> {
	const result = await _run({
		command: "git status --porcelain -uall",
		cwd: cwd ?? process.cwd(),
		timeout: 15,
	});
	return result.output.trim().length > 0;
}

/**
 * Check whether there are staged changes in the repository.
 * Useful for pre-commit checks where only staged files matter.
 */
export async function hasStagedChanges(cwd?: string): Promise<boolean> {
	const result = await _run({
		command: "git diff --cached --stat",
		cwd: cwd ?? process.cwd(),
		timeout: 15,
	});
	return result.output.trim().length > 0;
}

/** Options for `detectChanges`. */
export type DetectChangesOptions = {
	/** Working directory (repo root). */
	cwd: string;
	/** File extension filter (e.g., `.go`, `.ts`). */
	fileExt?: string;
	/** Only consider staged files. */
	staged?: boolean;
};

/**
 * Detect whether there are relevant changes in the repository.
 *
 * Used by exec and sync commands for skip-if-no-changes logic.
 */
export async function detectChanges(opts: DetectChangesOptions): Promise<boolean> {
	if (opts.fileExt) {
		const files = await getChangedFiles({
			cwd: opts.cwd,
			stagedOnly: opts.staged ?? false,
			fileExt: opts.fileExt,
		});
		return files.length > 0;
	}
	return opts.staged ? hasStagedChanges(opts.cwd) : hasUncommittedChanges(opts.cwd);
}

/**
 * Get the current HEAD commit SHA.
 *
 * Returns the full 40-character hex SHA, or an empty string if git is
 * unavailable or the directory is not a repository.
 */
export async function getHeadSha(cwd?: string): Promise<string> {
	try {
		const result = await _run({
			command: "git rev-parse HEAD",
			cwd: cwd ?? process.cwd(),
			timeout: 15,
		});
		return result.output.trim();
	} catch {
		return "";
	}
}

/** Options for `computeFingerprint`. */
export type FingerprintOptions = {
	/** Working directory (repo root). */
	cwd: string;
	/** Only consider staged changes (for pre-commit checks). */
	staged?: boolean;
	/** Scope diff to files matching this extension (e.g., `.go`, `.ts`). */
	fileExt?: string;
};

/**
 * Compute a composite fingerprint of the repository state: HEAD + working tree.
 *
 * Returns `sha256(HEAD_SHA + "\n" + diff_output)`. This captures both:
 *   1. The current commit (HEAD changes → fingerprint changes)
 *   2. Uncommitted modifications (any edit → fingerprint changes)
 *
 * When `staged` is true, uses `git diff --cached` instead of `git diff HEAD`.
 * When `fileExt` is provided, the diff is scoped to files matching that
 * extension via a pathspec filter.
 *
 * Used by:
 *   - state save/append — records a baseline fingerprint for session change
 *     detection (no scoping, full-repo diff).
 *   - exec run/check — records and validates sentinel fingerprints scoped
 *     to the exec's file extension and staged flag.
 *
 * Returns an empty string when git is unavailable (treated as "unknown").
 */
export async function computeFingerprint(opts: FingerprintOptions): Promise<string> {
	const head = await getHeadSha(opts.cwd);
	if (!head) return "";

	const parts = opts.staged ? ["git", "diff", "--cached"] : ["git", "diff", "HEAD"];

	if (opts.fileExt) {
		const ext = opts.fileExt.startsWith(".") ? opts.fileExt : `.${opts.fileExt}`;
		parts.push("--", `'*${ext}'`);
	}

	let diff: string;
	try {
		const result = await _run({
			command: parts.join(" "),
			cwd: opts.cwd,
			timeout: 30,
		});
		diff = result.output;
	} catch {
		return "";
	}

	return createHash("sha256").update(`${head}\n${diff}`).digest("hex");
}
