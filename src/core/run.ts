/**
 * Step functions for `chunk validate:<name>` — single-command resolution and execution.
 *
 * Returns structured data for the command layer to render.
 */

import { shouldPromptSave } from "./run.steps";
import {
	DEFAULT_TIMEOUT,
	listCommands,
	loadRunConfig,
	resolveCommand,
	saveCommand,
} from "./run-config";
import type { RunResult } from "./run-executor";
import { checkCache, executeCommand } from "./run-executor";

export { executeCommand, listCommands, saveCommand };

export type RunCommandOpts = {
	cmd?: string;
	save?: boolean;
	force?: boolean;
	status?: boolean;
};

export type RunCommandResult =
	| { type: "status-cached"; name: string; exitCode: number }
	| { type: "status-miss"; name: string }
	| { type: "not-configured"; name: string }
	| { type: "needs-setup"; name: string }
	| { type: "executed"; name: string; result: RunResult; saveAction: "save" | "prompt" | "skip" };

export function resolveRunCommand(
	projectDir: string,
	name: string,
	opts: RunCommandOpts,
	isTTY = false,
): RunCommandResult {
	const { config } = loadRunConfig(projectDir);
	const existingCommand = resolveCommand(name, config);

	// --status: check cache only
	if (opts.status) {
		const ext = existingCommand?.fileExt || undefined;
		const cached = checkCache(projectDir, name, ext);
		if (cached) {
			return { type: "status-cached", name, exitCode: cached.exitCode };
		}
		return { type: "status-miss", name };
	}

	if (opts.cmd) {
		const saveAction = shouldPromptSave({
			isTTY,
			saveFlag: opts.save === true,
			cmdProvided: true,
			existsInConfig: existingCommand !== undefined,
		});

		const result = executeCommand(projectDir, name, opts.cmd, {
			force: opts.force,
			timeout: DEFAULT_TIMEOUT,
		});

		return { type: "executed", name, result, saveAction };
	}

	if (existingCommand) {
		const result = executeCommand(projectDir, name, existingCommand.run, {
			force: opts.force,
			timeout: existingCommand.timeout,
			fileExt: existingCommand.fileExt || undefined,
		});

		return { type: "executed", name, result, saveAction: "skip" };
	}

	if (isTTY) {
		return { type: "needs-setup", name };
	}

	return { type: "not-configured", name };
}
