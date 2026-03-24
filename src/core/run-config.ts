/**
 * Config loading and writing for `.chunk/config.json`.
 *
 * Commands are stored as an ordered array. The array defines both
 * the execution order for `chunk validate` and the named lookup
 * for `chunk validate:<name>`.
 */

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const CONFIG_DIR = ".chunk";
const CONFIG_FILE = "config.json";

export type CommandEntry = {
	name: string;
	run: string;
	description?: string;
	timeout?: number;
	fileExt?: string;
};

export type RunConfig = {
	commands?: CommandEntry[];
};

export type ResolvedCommand = {
	run: string;
	description: string;
	timeout: number;
	fileExt: string;
};

export const DEFAULT_TIMEOUT = 300;

function configPath(projectDir: string): string {
	return join(projectDir, CONFIG_DIR, CONFIG_FILE);
}

export function configExists(projectDir: string): boolean {
	return existsSync(configPath(projectDir));
}

/**
 * Detect legacy `.chunk/config.json` format (installCommand/testCommand)
 * and convert to the array format in place.
 * Returns the migrated config, or undefined if the content isn't legacy format.
 */
export function migrateLegacyConfig(parsed: Record<string, unknown>): RunConfig | undefined {
	const installCommand =
		typeof parsed.installCommand === "string" ? parsed.installCommand : undefined;
	const testCommand = typeof parsed.testCommand === "string" ? parsed.testCommand : undefined;

	if (!installCommand && !testCommand) return undefined;

	const commands: CommandEntry[] = [];
	if (installCommand) commands.push({ name: "install", run: installCommand });
	if (testCommand) commands.push({ name: "test", run: testCommand });

	return { commands };
}

export function loadRunConfig(projectDir: string): { config: RunConfig; migrated: boolean } {
	const path = configPath(projectDir);
	if (!existsSync(path)) return { config: {}, migrated: false };

	try {
		const content = readFileSync(path, "utf-8");
		const parsed = JSON.parse(content) as Record<string, unknown>;

		// Already in new format
		if (Array.isArray(parsed.commands)) {
			return { config: parsed as unknown as RunConfig, migrated: false };
		}

		// Detect and migrate legacy format (installCommand/testCommand) in place
		const migrated = migrateLegacyConfig(parsed);
		if (migrated) {
			writeRunConfig(projectDir, migrated);
			return { config: migrated, migrated: true };
		}

		return { config: {}, migrated: false };
	} catch {
		return { config: {}, migrated: false };
	}
}

export function resolveCommand(name: string, config: RunConfig): ResolvedCommand | undefined {
	const entry = config.commands?.find((c) => c.name === name);
	if (!entry) return undefined;

	return {
		run: entry.run,
		description: entry.description ?? "",
		timeout: entry.timeout ?? DEFAULT_TIMEOUT,
		fileExt: entry.fileExt ?? "",
	};
}

/**
 * Returns the ordered command strings for `chunk validate` (full suite).
 */
export function loadSequenceCommands(
	projectDir: string,
): { commands: string[] } | { ok: false; error: string; hint?: string } {
	const { config } = loadRunConfig(projectDir);
	const entries = config.commands ?? [];

	if (entries.length === 0) {
		return {
			ok: false,
			error: "No validate commands configured",
			hint: "Run `chunk validate:init` to detect your install and test commands.",
		};
	}

	return { commands: entries.map((e) => e.run) };
}

export function listCommands(
	projectDir: string,
): Array<{ name: string; run: string; description: string; timeout: number }> {
	const { config } = loadRunConfig(projectDir);
	if (!config.commands?.length) return [];

	return config.commands.map((entry) => {
		const resolved = resolveCommand(entry.name, config) as ResolvedCommand;
		return { name: entry.name, ...resolved };
	});
}

function writeRunConfig(projectDir: string, config: RunConfig): void {
	const dir = join(projectDir, CONFIG_DIR);
	if (!existsSync(dir)) mkdirSync(dir, { recursive: true });
	writeFileSync(configPath(projectDir), `${JSON.stringify(config, null, 2)}\n`);
}

export function saveCommand(projectDir: string, name: string, command: string): void {
	const { config } = loadRunConfig(projectDir);
	if (!config.commands) config.commands = [];
	const idx = config.commands.findIndex((c) => c.name === name);
	if (idx >= 0) {
		config.commands[idx] = { ...config.commands[idx], run: command } as CommandEntry;
	} else {
		config.commands.push({ name, run: command });
	}
	writeRunConfig(projectDir, config);
}

export function saveCommandsConfig(projectDir: string, commands: CommandEntry[]): void {
	const { config: existing } = loadRunConfig(projectDir);
	// Merge: existing entries not in the new list are preserved at the end
	const newNames = new Set(commands.map((c) => c.name));
	const preserved = (existing.commands ?? []).filter((c) => !newNames.has(c.name));
	writeRunConfig(projectDir, { commands: [...commands, ...preserved] });
}
