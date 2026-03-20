#!/usr/bin/env bun
import { registerHookCommands } from "@chunk/hook";
import { Command } from "@commander-js/extra-typings";
import { registerAuthCommands } from "./commands/auth";
import { registerBuildPromptCommand } from "./commands/build-prompt";
import { registerCompletionCommands } from "./commands/completion";
import { registerConfigCommands } from "./commands/config";
import { registerRunCommand } from "./commands/run";
import { registerSandboxCommands } from "./commands/sandbox";
import { registerSkillsCommands } from "./commands/skills";
import { registerTaskCommands } from "./commands/task";
import { registerUpgradeCommand } from "./commands/upgrade";
import { registerValidateCommands } from "./commands/validate";
import { initCompletions } from "./completions";
import { isAuthError, isNetworkError, printError } from "./utils/errors";

const program = new Command();
program.name("chunk").version(VERSION).description("AI code review CLI").helpOption("-h, --help");

async function main(): Promise<void> {
	registerBuildPromptCommand(program);
	registerAuthCommands(program);
	registerConfigCommands(program);
	registerSkillsCommands(program);
	registerTaskCommands(program);
	registerUpgradeCommand(program);
	registerRunCommand(program);
	registerCompletionCommands(program);

	// Hook commands — exec, task, sync, state, scope for AI agent hooks
	const hook = program
		.command("hook")
		.description("Manage AI coding agent hooks (exec, task, sync, state, scope)");
	registerHookCommands(hook);

	registerSandboxCommands(program);
	registerValidateCommands(program);

	// Cheap no-op when not handling a completion request — omelette
	// detects its own argv flags internally and exits when needed.
	initCompletions(program);

	program.action(() => {
		program.outputHelp();
		process.exit(0);
	});

	await program.parseAsync(process.argv);
}

main().catch((error) => {
	const err = error instanceof Error ? error : new Error(String(error));
	let suggestion: string;
	if (isNetworkError(err)) {
		suggestion = "Check your internet connection and try again.";
	} else if (isAuthError(err)) {
		suggestion = "Run `chunk auth login` to set up your API key.";
	} else {
		suggestion = "If this problem persists, please report an issue.";
	}
	printError(err.message, undefined, suggestion);
	process.exit(2);
});
