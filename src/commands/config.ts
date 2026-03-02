import { resolveConfig, saveUserConfig } from "../storage/config";
import type { CommandResult } from "../types";
import { bold, cyan, dim, gray, green, yellow } from "../ui/colors";
import { label, printSuccess } from "../ui/format";
import { printError } from "../utils/errors";

/** Known configuration keys that can be set */
const VALID_CONFIG_KEYS = ["model", "apiKey"] as const;
type ConfigKey = (typeof VALID_CONFIG_KEYS)[number];

/**
 * Mask API key, showing only last 4 characters
 */
function maskApiKey(apiKey: string): string {
	if (apiKey.length <= 4) {
		return "****";
	}
	return "*".repeat(apiKey.length - 4) + apiKey.slice(-4);
}

/**
 * Format source label with color
 */
function formatSource(source: string): string {
	switch (source) {
		case "flag":
			return green("(flag)");
		case "env":
			return cyan("(env)");
		case "config":
		case "user-config":
			return yellow("(user config)");
		case "default":
			return gray("(default)");
		default:
			return dim(`(${source})`);
	}
}

/**
 * Check if a key is a valid config key
 */
function isValidConfigKey(key: string): key is ConfigKey {
	return VALID_CONFIG_KEYS.includes(key as ConfigKey);
}

/**
 * Display current configuration with sources
 */
export function runConfigShow(): CommandResult {
	const config = resolveConfig();

	console.log(`\n${bold("Configuration:")}\n`);

	// API Key
	const W = 7; // align to "apiKey:"
	if (config.apiKey) {
		const masked = maskApiKey(config.apiKey);
		const source = formatSource(config.sources.apiKey ?? "unknown");
		console.log(`  ${label("apiKey:", W)} ${masked} ${source}`);
	} else {
		console.log(`  ${label("apiKey:", W)} ${dim("not set")}`);
	}

	// Model
	const modelSource = formatSource(config.sources.model);
	console.log(`  ${label("model:", W)} ${config.model} ${modelSource}`);

	console.log("");

	return { exitCode: 0 };
}

/**
 * Set a configuration value
 */
export function runConfigSet(key: string, value: string): CommandResult {
	if (!key) {
		printError(
			"Missing config key",
			"Usage: chunk config set <key> <value>",
			`Valid keys: ${VALID_CONFIG_KEYS.join(", ")}`,
		);
		return { exitCode: 2 };
	}

	if (!value) {
		printError(
			`Missing value for key "${key}"`,
			"Usage: chunk config set <key> <value>",
			"Provide a value after the key name.",
		);
		return { exitCode: 2 };
	}

	if (!isValidConfigKey(key)) {
		printError(
			`Unknown config key "${key}"`,
			`The key "${key}" is not a recognized configuration option.`,
			`Valid keys: ${VALID_CONFIG_KEYS.join(", ")}`,
		);
		return { exitCode: 2 };
	}

	try {
		saveUserConfig({ [key]: value });
		printSuccess(`Set ${key} to ${value}`);
		return { exitCode: 0 };
	} catch (error) {
		const message = error instanceof Error ? error.message : "Unknown error";
		printError(
			"Failed to save config",
			message,
			"Check file permissions on ~/.config/chunk/config.json",
		);
		return { exitCode: 2 };
	}
}
