/**
 * `chunk hook env update` — Configure the user's shell environment.
 *
 * Performs the following steps:
 *   1. Creates the log directory
 *   2. Writes the ENV file with profile-based CHUNK_HOOK_* exports
 *   3. Ensures shell startup files source the ENV file
 *
 * All changes to shell startup files are idempotent — re-running the
 * command updates existing blocks in place rather than appending duplicates.
 */

import { mkdirSync } from "node:fs";

import {
	defaultEnvFile,
	defaultLogDir,
	ensureLoginSourcing,
	generateEnvContent,
	type Profile,
	PROFILES,
	writeEnvFile,
} from "../lib/shell-env";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Options for env update. */
export type EnvUpdateOptions = {
	profile: Profile;
	envFile: string;
	logDir: string;
	verbose: boolean;
	projectRoot?: string;
	/** Override shell startup files (for testing). When omitted, uses auto-detected defaults. */
	startupFiles?: string[];
};

/** Result of the env update operation. */
export type EnvUpdateResult = {
	envFile: string;
	profile: Profile;
	logDir: string;
	/** Whether the ENV file already existed (was overwritten). */
	overwritten: boolean;
	/** Shell startup files that were updated. */
	startupFiles: string[];
};

// ---------------------------------------------------------------------------
// Default options
// ---------------------------------------------------------------------------

/** Build default options, applying overrides from flags. */
export function buildEnvUpdateOptions(flags: {
	profile?: string;
	envFile?: string;
	logDir?: string;
	verbose?: boolean;
	projectRoot?: string;
}): EnvUpdateOptions {
	const profile = (flags.profile ?? "enable") as Profile;
	if (!PROFILES.includes(profile)) {
		throw new Error(`Unknown profile: "${profile}". Valid profiles: ${PROFILES.join(", ")}`);
	}
	return {
		profile,
		envFile: flags.envFile ?? defaultEnvFile(),
		logDir: flags.logDir ?? defaultLogDir(),
		verbose: flags.verbose ?? false,
		projectRoot: flags.projectRoot,
	};
}

// ---------------------------------------------------------------------------
// Core logic
// ---------------------------------------------------------------------------

/**
 * Configure the user's shell environment for chunk hook.
 *
 * 1. Creates log directory
 * 2. Writes ENV file with profile-based exports
 * 3. Ensures shell startup files source the ENV file
 */
export function runEnvUpdate(opts: EnvUpdateOptions): EnvUpdateResult {
	// 1. Create log directory
	mkdirSync(opts.logDir, { recursive: true });

	// 2. Generate and write ENV file
	const content = generateEnvContent({
		profile: opts.profile,
		logDir: opts.logDir,
		verbose: opts.verbose,
		projectRoot: opts.projectRoot,
		envFile: opts.envFile,
	});
	const overwritten = writeEnvFile(opts.envFile, content);

	// 3. Ensure login sourcing
	const startupFiles = ensureLoginSourcing(opts.envFile, opts.startupFiles);

	return {
		envFile: opts.envFile,
		profile: opts.profile,
		logDir: opts.logDir,
		overwritten,
		startupFiles,
	};
}
