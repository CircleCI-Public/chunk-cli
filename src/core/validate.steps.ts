import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { parse as parseYaml } from "yaml";

interface HookConfig {
	execs?: Record<string, { command?: string }>;
}

export type LoadValidateCommandsResult = { commands: string[] } | { commands: []; error: string };

export function loadValidateCommands(projectDir: string): LoadValidateCommandsResult {
	const configPath = join(projectDir, ".chunk", "hook", "config.yml");
	if (!existsSync(configPath)) return { commands: [] };
	try {
		const content = readFileSync(configPath, "utf-8");
		const config = parseYaml(content) as HookConfig;
		if (!config.execs) return { commands: [] };
		const commands = Object.values(config.execs)
			.map((exec) => exec.command ?? "")
			.filter((cmd) => cmd.length > 0 && !cmd.includes("{{"));
		return { commands };
	} catch (e) {
		return { commands: [], error: `Failed to parse ${configPath}: ${e}` };
	}
}
