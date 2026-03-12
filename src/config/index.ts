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

// --- Paths ---
export const USER_CONFIG_FILENAME = "config.json";

function xdgConfigBase(): string {
	return process.env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config");
}

export function getUserConfigDir(): string {
	return path.join(xdgConfigBase(), "chunk");
}

export function getConfigFile(): string {
	return path.join(getUserConfigDir(), USER_CONFIG_FILENAME);
}

export function getLegacyConfigDir(): string {
	return path.join(os.homedir(), ".chunk");
}

export function getLegacyConfigFile(): string {
	return path.join(getLegacyConfigDir(), USER_CONFIG_FILENAME);
}
