import * as fs from "node:fs";
import { DEFAULT_MODEL, ENV, getConfigFile, getUserConfigDir } from "../config";

export interface UserConfig {
	apiKey?: string;
	model?: string;
}

export interface ResolvedConfig {
	apiKey?: string;
	model: string;
	sources: {
		apiKey?: "env" | "config" | "flag";
		model: ConfigSource;
	};
}

type ConfigSource = "default" | "env" | "user-config" | "flag";

export function loadUserConfig(): UserConfig {
	const configPath = getConfigFile();

	try {
		if (!fs.existsSync(configPath)) {
			return {};
		}

		const content = fs.readFileSync(configPath, "utf-8");
		const parsed = JSON.parse(content) as Record<string, unknown>;

		return {
			apiKey: typeof parsed.apiKey === "string" ? parsed.apiKey : undefined,
			model: typeof parsed.model === "string" ? parsed.model : undefined,
		};
	} catch {
		return {};
	}
}

export interface ResolveConfigOptions {
	flagModel?: string;
	flagApiKey?: string;
}

function resolveValue<T>(
	flag: T | undefined,
	env: T | undefined,
	user: T | undefined,
	defaultVal: T,
): { value: T; source: ConfigSource } {
	if (flag !== undefined) return { value: flag, source: "flag" };
	if (env !== undefined) return { value: env, source: "env" };
	if (user !== undefined) return { value: user, source: "user-config" };
	return { value: defaultVal, source: "default" };
}

/**
 * Resolve configuration with proper precedence:
 * CLI flags > env vars > user config > defaults
 */
export function resolveConfig(options: ResolveConfigOptions = {}): ResolvedConfig {
	const userConfig = loadUserConfig();

	// API key has special precedence (no default)
	let apiKey: string | undefined;
	let apiKeySource: "env" | "config" | "flag" | undefined;
	if (options.flagApiKey) {
		apiKey = options.flagApiKey;
		apiKeySource = "flag";
	} else if (process.env[ENV.ANTHROPIC_API_KEY]) {
		apiKey = process.env[ENV.ANTHROPIC_API_KEY];
		apiKeySource = "env";
	} else if (userConfig.apiKey) {
		apiKey = userConfig.apiKey;
		apiKeySource = "config";
	}

	const model = resolveValue(
		options.flagModel,
		process.env[ENV.MODEL],
		userConfig.model,
		DEFAULT_MODEL,
	);

	return {
		apiKey,
		model: model.value,
		sources: {
			apiKey: apiKeySource,
			model: model.source,
		},
	};
}

export function saveUserConfig(config: UserConfig): void {
	const configDir = getUserConfigDir();
	const configPath = getConfigFile();

	if (!fs.existsSync(configDir)) {
		fs.mkdirSync(configDir, { recursive: true, mode: 0o700 });
	}

	const existing = loadUserConfig();
	const merged = { ...existing, ...config };

	const content = JSON.stringify(merged, null, 2);
	fs.writeFileSync(configPath, content, { mode: 0o600 });
}

export function clearApiKey(): boolean {
	const configPath = getConfigFile();

	try {
		if (!fs.existsSync(configPath)) {
			return false;
		}

		const existing = loadUserConfig();

		if (!existing.apiKey) {
			return false;
		}

		const rest: UserConfig = {};
		if (existing.model) {
			rest.model = existing.model;
		}

		if (Object.keys(rest).length === 0) {
			fs.unlinkSync(configPath);
		} else {
			const content = JSON.stringify(rest, null, 2);
			fs.writeFileSync(configPath, content, { mode: 0o600 });
		}

		return true;
	} catch {
		return false;
	}
}
