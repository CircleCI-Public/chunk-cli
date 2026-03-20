/**
 * Orchestrator for `chunk run`.
 */

import { bold, dim, green, red } from "../ui/colors";
import { promptConfirm, promptInput } from "../ui/prompt";
import { printError } from "../utils/errors";
import { formatCommandList, shouldPromptSave } from "./run.steps";
import { listCommands, loadRunConfig, resolveCommand, saveCommand } from "./run-config";
import { checkCache, executeCommand } from "./run-executor";

export async function runList(projectDir: string): Promise<void> {
	const commands = listCommands(projectDir);

	if (commands.length === 0) {
		console.log(`No commands configured.\n`);
		console.log(`Add commands to ${bold(".chunk/commands.json")}:\n`);
		console.log(`  ${dim('chunk run test --cmd "npm test" --save')}`);
		console.log(`  ${dim('chunk run lint --cmd "npm run lint" --save')}`);
		return;
	}

	console.log(formatCommandList(commands));
}

export type RunCommandOpts = {
	cmd?: string;
	save?: boolean;
	force?: boolean;
	status?: boolean;
	project?: string;
};

export async function runCommand(
	projectDir: string,
	name: string,
	opts: RunCommandOpts,
): Promise<number> {
	const config = loadRunConfig(projectDir);
	const existingCommand = resolveCommand(name, config);
	const isTTY = process.stdin.isTTY === true;

	// --status: check cache only
	if (opts.status) {
		const cached = checkCache(projectDir, name);
		if (cached) {
			console.log(`${green("✓")} ${name}: cached (${cached.exitCode === 0 ? "pass" : "fail"})`);
			return cached.exitCode;
		}
		console.log(`${dim("○")} ${name}: no cached result`);
		return 0;
	}

	// Determine which command to run
	let commandStr: string;
	let timeout: number;

	if (opts.cmd) {
		commandStr = opts.cmd;
		timeout = 120;

		// Handle save logic
		const action = shouldPromptSave({
			isTTY,
			saveFlag: opts.save === true,
			cmdProvided: true,
			existsInConfig: existingCommand !== undefined,
		});

		if (action === "save") {
			saveCommand(projectDir, name, commandStr);
			console.log(`${green("✓")} Saved ${bold(name)} to .chunk/commands.json`);
		} else if (action === "prompt") {
			const shouldSave = await promptConfirm(`Save ${bold(name)} to .chunk/commands.json?`);
			if (shouldSave) {
				saveCommand(projectDir, name, commandStr);
				console.log(`${green("✓")} Saved ${bold(name)} to .chunk/commands.json`);
			}
		}
	} else if (existingCommand) {
		commandStr = existingCommand.run;
		timeout = existingCommand.timeout;
	} else if (isTTY) {
		// Interactive setup: prompt for the command
		console.log(`Command ${bold(name)} is not configured yet.\n`);
		const input = await promptInput(`What command should ${bold(name)} run? `);
		const trimmed = input.trim();
		if (!trimmed) {
			console.log(dim("No command entered, aborting."));
			return 1;
		}
		commandStr = trimmed;
		timeout = 120;
		saveCommand(projectDir, name, commandStr);
		console.log(`${green("✓")} Saved ${bold(name)} to .chunk/commands.json\n`);
	} else {
		// Non-TTY: can't prompt, error out
		printError(
			`Command "${name}" is not configured`,
			undefined,
			`Add "${name}" to .chunk/commands.json`,
		);
		return 1;
	}

	// Execute
	const result = executeCommand(projectDir, name, commandStr, {
		force: opts.force,
		timeout,
	});

	if (result.status === "cached") {
		console.log(
			`${green("✓")} ${name}: cached result (${result.exitCode === 0 ? "pass" : "fail"})`,
		);
	} else if (result.status === "pass") {
		console.log(`${green("✓")} ${name}: pass`);
	} else {
		console.error(`${red("✗")} ${name}: fail (exit ${result.exitCode})`);
	}

	if (result.output) {
		const stream = result.status === "fail" ? process.stderr : process.stdout;
		stream.write(result.output);
		if (!result.output.endsWith("\n")) stream.write("\n");
	}

	return result.status === "fail" ? 1 : 0;
}
