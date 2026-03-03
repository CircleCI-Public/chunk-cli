/**
 * Git helpers for detecting changed files.
 *
 * Used by exec and task commands for placeholder substitution
 * (`{{CHANGED_FILES}}`, `{{CHANGED_PACKAGES}}`) and skip-if-no-changes logic.
 */

import { runCommand } from "./proc";

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

	const result = await runCommand({ command, cwd, timeout: 30 });
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
	const result = await runCommand({
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
	const result = await runCommand({
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
