import { existsSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { parse as parseYaml } from "yaml";
import type { CommandResult } from "../types/index";
import { bold, dim, green, red, yellow } from "../ui/colors";
import { printError } from "../utils/errors";

interface ValidateRawConfig {
	validate?: string[];
}

function loadValidateCommands(projectDir: string): string[] {
	const configPath = join(projectDir, ".chunk", "hook", "config.yml");
	if (!existsSync(configPath)) return [];
	try {
		const content = readFileSync(configPath, "utf-8");
		const config = parseYaml(content) as ValidateRawConfig;
		return Array.isArray(config.validate) ? config.validate : [];
	} catch {
		return [];
	}
}

type StepResult = {
	command: string;
	exitCode: number;
};

export async function runValidate(): Promise<CommandResult> {
	const projectDir = process.cwd();
	const commands = loadValidateCommands(projectDir);

	if (commands.length === 0) {
		printError(
			"No validate commands configured",
			"Add a 'validate' list to .chunk/hook/config.yml",
			"Example:\n  validate:\n    - bun test\n    - bun run typecheck",
		);
		return { exitCode: 1 };
	}

	const results: StepResult[] = [];

	for (const command of commands) {
		process.stdout.write(`\n${bold("$")} ${command}\n`);

		const proc = Bun.spawn(["sh", "-c", command], {
			cwd: projectDir,
			stdout: "inherit",
			stderr: "inherit",
		});

		await proc.exited;
		const exitCode = proc.exitCode ?? 1;
		results.push({ command, exitCode });

		if (exitCode !== 0) break;
	}

	const skipped = commands.slice(results.length);
	const passed = results.every((r) => r.exitCode === 0) && skipped.length === 0;

	// Summary
	process.stdout.write(`\n${bold("─".repeat(40))}\n`);
	for (const { command, exitCode } of results) {
		const icon = exitCode === 0 ? green("✓") : red("✗");
		process.stdout.write(`${icon} ${command}\n`);
	}
	for (const command of skipped) {
		process.stdout.write(`${dim("○")} ${yellow(command)} ${dim("(skipped)")}\n`);
	}

	return { exitCode: passed ? 0 : 1 };
}
