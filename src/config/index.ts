/**
 * Centralized configuration: models, env, paths
 */
import * as os from "node:os";
import * as path from "node:path";

// --- Models ---
export const DEFAULT_MODEL = "claude-sonnet-4-5-20250929";
export const VALIDATION_MODEL = "claude-haiku-4-5-20251001";
export const DEFAULT_ANALYZE_MODEL = "claude-sonnet-4-5-20250929";
export const DEFAULT_PROMPT_MODEL = "claude-opus-4-5-20251101";

// --- Environment ---
export const ENV = {
	ANTHROPIC_API_KEY: "ANTHROPIC_API_KEY",
	MODEL: "CODE_REVIEW_CLI_MODEL",
} as const;

/**
 * Resolve the CircleCI API token from environment variables.
 * Prefers CIRCLECI_TOKEN but falls back to CIRCLE_TOKEN for backward compatibility.
 */
export function getCircleCIToken(): string | undefined {
	return process.env.CIRCLECI_TOKEN ?? process.env.CIRCLE_TOKEN;
}

// --- Paths ---
export const USER_CONFIG_DIR = ".chunk";
export const USER_CONFIG_FILENAME = "config.json";

export function getUserConfigDir() {
	return path.join(os.homedir(), USER_CONFIG_DIR);
}

export function getConfigFile() {
	return path.join(getUserConfigDir(), USER_CONFIG_FILENAME);
}
