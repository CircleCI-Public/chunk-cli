/**
 * `chunk hook repo init` — Initialize a repository with hook configuration files.
 *
 * Copies template files into the target directory:
 *   - `.chunk/hook/` config files (gitignore, config.yml, review instructions, schema)
 *   - `.claude/settings.json` with hook wiring (substitutes __PROJECT__ placeholder)
 *
 * When a target file already exists, the template is saved as a `.example.<ext>`
 * variant instead of overwriting, so existing configuration is never lost.
 */

import { existsSync, mkdirSync, writeFileSync } from "node:fs";
import { basename, dirname, extname, join, resolve } from "node:path";

import { TEMPLATE_FILES } from "../lib/templates";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Options for repo init. */
export type RepoInitOptions = {
	/** Target directory to initialize. Defaults to cwd. */
	targetDir: string;
	/** If true, overwrite existing files without creating .example copies. */
	force: boolean;
};

/** Result of copying a single template file. */
export type CopyResult = {
	relativePath: string;
	action: "created" | "example" | "skipped";
	/** For "example" action, the path of the example file. */
	examplePath?: string;
};

// ---------------------------------------------------------------------------
// Core logic
// ---------------------------------------------------------------------------

/**
 * Copy a template file to the target directory.
 *
 * If the destination already exists and `force` is false, save the template
 * as `.example.<ext>` instead of overwriting.
 */
function copyTemplateFile(
	content: string,
	targetDir: string,
	relativePath: string,
	force: boolean,
): CopyResult {
	const dest = join(targetDir, relativePath);
	const destDir = dirname(dest);

	if (existsSync(dest) && !force) {
		// File exists — save as .example variant
		const base = basename(dest);
		const ext = extname(base);
		const name = base.slice(0, base.length - ext.length);

		let exampleDest: string;
		if (ext === "") {
			exampleDest = join(destDir, `${base}.example`);
		} else {
			exampleDest = join(destDir, `${name}.example${ext}`);
		}

		writeFileSync(exampleDest, content);
		return { relativePath, action: "example", examplePath: exampleDest };
	}

	mkdirSync(destDir, { recursive: true });
	writeFileSync(dest, content);
	return { relativePath, action: "created" };
}

/**
 * Initialize a repository with chunk hook configuration files.
 *
 * @returns Array of copy results describing what was done for each template.
 */
export function runRepoInit(opts: RepoInitOptions): CopyResult[] {
	const targetDir = resolve(opts.targetDir);
	const projectName = basename(targetDir);
	const results: CopyResult[] = [];

	for (const template of TEMPLATE_FILES) {
		let content = template.content;

		// Substitute __PROJECT__ placeholder with the repo's basename
		if (template.substituteProject) {
			content = content.replaceAll("__PROJECT__", projectName);
		}

		const result = copyTemplateFile(content, targetDir, template.relativePath, opts.force);
		results.push(result);
	}

	return results;
}
