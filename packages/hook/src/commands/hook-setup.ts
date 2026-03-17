/**
 * `chunk hook setup` — One-shot setup for a repository.
 *
 * Orchestrates the two setup steps in order:
 *   1. `runEnvUpdate()` — shell env vars (skippable with skipEnv)
 *   2. `runRepoInit()` — creates `.chunk/hook/` and `.claude/settings.json`
 *
 * Returns a combined result for the caller to format and display.
 */

import type { Profile } from "../lib/shell-env";
import { buildEnvUpdateOptions, type EnvUpdateResult, runEnvUpdate } from "./env-update";
import { type CopyResult, runRepoInit } from "./repo-init";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Options for hook setup. */
export type HookSetupOptions = {
	/** Target directory to initialize. Defaults to cwd. */
	targetDir: string;
	/** Shell profile to configure. */
	profile: Profile;
	/** If true, overwrite existing files without creating .example copies. */
	force: boolean;
	/** If true, skip the env update step. */
	skipEnv: boolean;
	/** Override shell startup files (for testing). */
	startupFiles?: string[];
};

/** Combined result from both setup steps. */
export type HookSetupResult = {
	copyResults: CopyResult[];
	/** null when skipEnv is true */
	envResult: EnvUpdateResult | null;
};

// ---------------------------------------------------------------------------
// Core logic
// ---------------------------------------------------------------------------

/**
 * Run the full hook setup: env update (optional) + repo init.
 */
export function runHookSetup(opts: HookSetupOptions): HookSetupResult {
	let envResult: EnvUpdateResult | null = null;

	if (!opts.skipEnv) {
		const envOptions = buildEnvUpdateOptions({ profile: opts.profile });
		if (opts.startupFiles) {
			envOptions.startupFiles = opts.startupFiles;
		}
		envResult = runEnvUpdate(envOptions);
	}

	const copyResults = runRepoInit({
		targetDir: opts.targetDir,
		force: opts.force,
	});

	return { copyResults, envResult };
}
