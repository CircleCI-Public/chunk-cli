#!/usr/bin/env bun
import { registerHookCommands } from "@chunk/hook";
import { Command } from "@commander-js/extra-typings";
import { registerAuthCommands } from "./commands/auth";
import { registerBuildPromptCommand } from "./commands/build-prompt";
import { registerConfigCommands } from "./commands/config";
import { registerSkillsCommands } from "./commands/skills";
import { registerTaskCommands } from "./commands/task";
import { registerUpgradeCommand } from "./commands/upgrade";
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

	// Hook commands — exec, task, sync, state, scope for AI agent hooks
	const hook = program
		.command("hook")
		.description("Manage AI coding agent hooks (exec, task, sync, state, scope)");
	registerHookCommands(hook);

	const sandboxes = program.command("sandboxes").description("Manage sandboxes");
	sandboxes
		.command("list")
		.description("List all sandboxes in an organization")
		.requiredOption("--org-id <orgId>", "Org ID to list sandboxes for")
		.action(async (options) => process.exit((await listSandboxes(options.orgId)).exitCode));

	sandboxes
		.command("create")
		.description("Create a new sandbox")
		.requiredOption("--org-id <orgId>", "Organization ID")
		.requiredOption("--name <name>", "Sandbox name")
		.option("--image <image>", "Sandbox image")
		.action(async (options) =>
			process.exit((await createNewSandbox(options.orgId, options.name, options.image)).exitCode),
		);

	sandboxes
		.command("add-ssh-key")
		.description("Add an SSH public key to a sandbox")
		.requiredOption("--org-id <orgId>", "Organization ID")
		.requiredOption("--sandbox-id <sandboxId>", "Sandbox ID")
		.option("--public-key <publicKey>", "SSH public key string to add")
		.option("--public-key-file <keyFile>", "Path to a .pub file containing the SSH public key")
		.action(async (options) =>
			process.exit(
				(
					await addSshKeyToSandbox(
						options.orgId,
						options.sandboxId,
						options.publicKey,
						options.publicKeyFile,
					)
				).exitCode,
			),
		);

	sandboxes
		.command("exec")
		.description("Execute a command in a sandbox")
		.requiredOption("--org-id <orgId>", "Org ID of sandbox")
		.requiredOption("--sandbox-id <sandboxId>", "Sandbox ID of sandbox")
		.requiredOption("--command <command>", "Command to execute")
		.option("--args <args...>", "Arguments to command", [])
		.action(async (options) =>
			process.exit(
				(
					await execCommandInSandbox(
						options.orgId,
						options.sandboxId,
						options.command,
						options.args,
					)
				).exitCode,
			),
		);

	sandboxes
		.command("prepare")
		.description("[EXPERIMENTAL] Prepare the hook environment before a session begins")
		.option("--docker-sudo", "Run docker commands with sudo", false)
		.action(async (opts: { dockerSudo: boolean }) =>
			process.exit((await runSandboxPrepare({ dockerSudo: opts.dockerSudo })).exitCode),
		);

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
