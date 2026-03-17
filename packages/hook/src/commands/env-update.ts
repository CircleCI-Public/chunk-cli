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

// Re-export pure step function and types from lib layer
export {
	buildEnvUpdateOptions,
	type EnvUpdateOptions,
	type EnvUpdateResult,
	migrateEnvFile,
	runEnvUpdate,
} from "../lib/env-update";
