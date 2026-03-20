/**
 * Config loading and writing for `.chunk/commands.json`.
 *
 * Supports string shorthand ("npm test") and expanded form
 * ({ run, description, timeout }).
 */

import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const CONFIG_DIR = ".chunk";
const CONFIG_FILE = "commands.json";

export type CommandConfig =
	| string
	| { run: string; description?: string; timeout?: number; fileExt?: string };

export type RunConfig = { commands?: Record<string, CommandConfig> };

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

export function configExists(projectDir: string): boolean {
	return existsSync(configPath(projectDir));
}

export function loadRunConfig(projectDir: string): RunConfig {
	const path = configPath(projectDir);
	if (!existsSync(path)) return {};
	try {
		const content = readFileSync(path, "utf-8");
		return JSON.parse(content) as RunConfig;
	} catch {
		return {};
	}
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

export function saveCommand(projectDir: string, name: string, command: string): void {
	const dir = join(projectDir, CONFIG_DIR);
	if (!existsSync(dir)) mkdirSync(dir, { recursive: true });

	const config = loadRunConfig(projectDir);
	if (!config.commands) config.commands = {};
	config.commands[name] = command;

	writeFileSync(configPath(projectDir), `${JSON.stringify(config, null, 2)}\n`);
}
