/**
 * Config loading and writing for `.chunk/commands.json`.
 *
 * Supports string shorthand ("npm test") and expanded form
 * ({ run, description, timeout }).
 *
 * The `sequence` key defines the ordered list of commands that
 * `chunk validate` (bare) runs. Individual commands can be invoked
 * via `chunk validate:<name>`.
 */

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const CONFIG_DIR = ".chunk";
const CONFIG_FILE = "commands.json";
const LEGACY_CONFIG_FILE = "config.json";

export type CommandConfig =
	| string
	| { run: string; description?: string; timeout?: number; fileExt?: string };

export type RunConfig = {
	sequence?: string[];
	commands?: Record<string, CommandConfig>;
};

export type ResolvedCommand = {
	run: string;
	description: string;
	timeout: number;
	fileExt: string;
};

const DEFAULT_TIMEOUT = 300;

function configPath(projectDir: string): string {
	return join(projectDir, CONFIG_DIR, CONFIG_FILE);
}

function legacyConfigPath(projectDir: string): string {
	return join(projectDir, CONFIG_DIR, LEGACY_CONFIG_FILE);
}

export function configExists(projectDir: string): boolean {
	return existsSync(configPath(projectDir));
}

/**
 * Migrate legacy `.chunk/config.json` (installCommand/testCommand)
 * into the unified `.chunk/commands.json` format with a `sequence` key.
 * Returns the migrated config, or undefined if no legacy config exists.
 */
export function migrateLegacyConfig(projectDir: string): RunConfig | undefined {
	const legacy = legacyConfigPath(projectDir);
	if (!existsSync(legacy)) return undefined;

	try {
		const content = readFileSync(legacy, "utf-8");
		const parsed = JSON.parse(content) as Record<string, unknown>;
		const installCommand =
			typeof parsed.installCommand === "string" ? parsed.installCommand : undefined;
		const testCommand = typeof parsed.testCommand === "string" ? parsed.testCommand : undefined;

		if (!installCommand && !testCommand) return undefined;

		const commands: Record<string, CommandConfig> = {};
		const sequence: string[] = [];

		if (installCommand) {
			commands.install = installCommand;
			sequence.push("install");
		}
		if (testCommand) {
			commands.test = testCommand;
			sequence.push("test");
		}

		return { sequence, commands };
	} catch {
		return undefined;
	}
}

export function loadRunConfig(projectDir: string): RunConfig {
	const path = configPath(projectDir);
	if (existsSync(path)) {
		try {
			const content = readFileSync(path, "utf-8");
			const config = JSON.parse(content) as RunConfig;

			// If commands.json exists but has no sequence, check for legacy config to migrate
			if (!config.sequence) {
				const migrated = migrateLegacyConfig(projectDir);
				if (migrated) {
					const merged: RunConfig = {
						sequence: migrated.sequence,
						commands: { ...migrated.commands, ...config.commands },
					};
					writeRunConfig(projectDir, merged);
					return merged;
				}
			}

			return config;
		} catch {
			return {};
		}
	}

	// No commands.json — try migrating legacy config.json
	const migrated = migrateLegacyConfig(projectDir);
	if (migrated) {
		writeRunConfig(projectDir, migrated);
		return migrated;
	}

	return {};
}

export function resolveCommand(name: string, config: RunConfig): ResolvedCommand | undefined {
	const entry = config.commands?.[name];
	if (entry === undefined) return undefined;

	if (typeof entry === "string") {
		return { run: entry, description: "", timeout: DEFAULT_TIMEOUT, fileExt: "" };
	}

	return {
		run: entry.run,
		description: entry.description ?? "",
		timeout: entry.timeout ?? DEFAULT_TIMEOUT,
		fileExt: entry.fileExt ?? "",
	};
}

/**
 * Resolve the `sequence` entries to their command strings.
 * Returns the ordered command strings for pipeline execution.
 */
export function loadSequenceCommands(
	projectDir: string,
): { commands: string[] } | { ok: false; error: string; hint?: string } {
	const config = loadRunConfig(projectDir);
	const sequence = config.sequence ?? [];

	if (sequence.length === 0) {
		return {
			ok: false,
			error: "No validate commands configured",
			hint: "Run `chunk validate:init` to detect your install and test commands.",
		};
	}

	const commands: string[] = [];
	for (const name of sequence) {
		const resolved = resolveCommand(name, config);
		if (!resolved) {
			return {
				ok: false,
				error: `Sequence references unknown command "${name}"`,
				hint: `Add "${name}" to the commands map in .chunk/commands.json`,
			};
		}
		commands.push(resolved.run);
	}

	return { commands };
}

export function listCommands(
	projectDir: string,
): Array<{ name: string; run: string; description: string; timeout: number }> {
	const config = loadRunConfig(projectDir);
	if (!config.commands) return [];

	return Object.entries(config.commands).map(([name]) => {
		// resolveCommand is guaranteed to return a value here since we're iterating config.commands
		const resolved = resolveCommand(name, config) as ResolvedCommand;
		return { name, ...resolved };
	});
}

function writeRunConfig(projectDir: string, config: RunConfig): void {
	const dir = join(projectDir, CONFIG_DIR);
	if (!existsSync(dir)) mkdirSync(dir, { recursive: true });
	writeFileSync(configPath(projectDir), `${JSON.stringify(config, null, 2)}\n`);
}

export function saveCommand(projectDir: string, name: string, command: string): void {
	const config = loadRunConfig(projectDir);
	if (!config.commands) config.commands = {};
	config.commands[name] = command;
	writeRunConfig(projectDir, config);
}

export function saveSequenceConfig(
	projectDir: string,
	sequence: string[],
	commands: Record<string, CommandConfig>,
): void {
	const existing = loadRunConfig(projectDir);
	const merged: RunConfig = {
		sequence,
		commands: { ...existing.commands, ...commands },
	};
	writeRunConfig(projectDir, merged);
}
