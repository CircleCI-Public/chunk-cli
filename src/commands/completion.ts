import type { Command } from "@commander-js/extra-typings";
import { setupShellCompletion, teardownShellCompletion } from "../completions";
import type { CommandResult } from "../types";

export function registerCompletionCommands(program: Command): void {
	const cmd = program.command("completion").description("Manage shell tab completions");

	cmd
		.command("install")
		.description("Install shell tab completions for chunk")
		.addHelpText("after", "\nManual setup: add `. <(chunk --completion)` to ~/.zshrc or ~/.bashrc")
		.action(() => process.exit(runCompletionInstall(program).exitCode));

	cmd
		.command("uninstall")
		.description("Remove shell tab completions for chunk")
		.action(() => process.exit(runCompletionUninstall(program).exitCode));
}

function runCompletionInstall(program: Command): CommandResult {
	try {
		setupShellCompletion(program);
		console.log("Shell completions installed. Restart your terminal or source your shell config.");
		return { exitCode: 0 };
	} catch (err: unknown) {
		console.error(
			`Failed to install completions: ${err instanceof Error ? err.message : String(err)}`,
		);
		return { exitCode: 1 };
	}
}

function runCompletionUninstall(program: Command): CommandResult {
	try {
		teardownShellCompletion(program);
		console.log("Shell completions removed.");
		return { exitCode: 0 };
	} catch (err: unknown) {
		console.error(
			`Failed to remove completions: ${err instanceof Error ? err.message : String(err)}`,
		);
		return { exitCode: 1 };
	}
}
