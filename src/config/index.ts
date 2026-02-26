/**
 * Centralized configuration: models, env, paths
 */
import * as os from "node:os";
import * as path from "node:path";

// --- Models ---
export const DEFAULT_MODEL = "claude-sonnet-4-5-20250929";
export const VALIDATION_MODEL = "claude-haiku-4-5-20251001";

// --- Environment ---
export const ENV = {
	ANTHROPIC_API_KEY: "ANTHROPIC_API_KEY",
	MODEL: "CODE_REVIEW_CLI_MODEL",
} as const;

// --- Paths ---
export const USER_CONFIG_DIR = ".chunk";
export const USER_CONFIG_FILENAME = "config.json";

export function getUserConfigDir() {
	return path.join(os.homedir(), USER_CONFIG_DIR);
}

export function getConfigFile() {
	return path.join(getUserConfigDir(), USER_CONFIG_FILENAME);
}
