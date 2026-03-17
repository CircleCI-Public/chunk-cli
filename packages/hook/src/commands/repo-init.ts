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

// Re-export pure step function and types from lib layer
export {
	type CopyResult,
	type RepoInitOptions,
	runRepoInit,
} from "../lib/repo-init";
