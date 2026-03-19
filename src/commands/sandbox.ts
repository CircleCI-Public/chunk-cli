import type { Command } from "@commander-js/extra-typings";
import type { Sandbox } from "../api/circleci";
import {
	addSshKeyToSandbox,
	createNewSandbox,
	execCommandInSandbox,
	listSandboxes,
	runOverSsh,
	syncToSandbox,
} from "../core/sandboxes";
import type { CommandResult } from "../types/index";
import { bold } from "../ui/colors";
import { printError } from "../utils/errors";
import { runSandboxPrepare } from "./sandbox/prepare";

function finalize(result: CommandResult): never {
	if (result.error) {
		printError(result.error.title, result.error.detail, result.error.suggestion);
	}
	process.exit(result.exitCode);
}

export function registerSandboxCommands(program: Command): void {
	const sandboxes = program
		.command("sandboxes")
		.description("Manage sandboxes")
		.enablePositionalOptions();

	sandboxes
		.command("list")
		.description("List all sandboxes in an organization")
		.requiredOption("--org-id <orgId>", "Org ID to list sandboxes for")
		.action(async (options) => {
			const result = await listSandboxes(options.orgId);
			if (result.exitCode === 0 && result.data) {
				const sandboxList = result.data as Sandbox[];
				console.log(`\n${bold("Sandboxes")}\n`);
				if (sandboxList.length === 0) {
					console.log("No sandboxes found.\n");
				} else {
					for (const sandbox of sandboxList) {
						console.log(`  ${sandbox.name}  ${sandbox.id}`);
					}
					console.log("");
				}
			}
			finalize(result);
		});

	sandboxes
		.command("create")
		.description("Create a new sandbox")
		.requiredOption("--org-id <orgId>", "Organization ID")
		.requiredOption("--name <name>", "Sandbox name")
		.option("--image <image>", "Sandbox image")
		.action(async (options) => {
			const result = await createNewSandbox(options.orgId, options.name, options.image);
			if (result.exitCode === 0 && result.data) {
				console.log(JSON.stringify(result.data, null, 2));
			}
			finalize(result);
		});

	sandboxes
		.command("add-ssh-key")
		.description("Add an SSH public key to a sandbox")
		.requiredOption("--org-id <orgId>", "Organization ID")
		.requiredOption("--sandbox-id <sandboxId>", "Sandbox ID")
		.option("--public-key <publicKey>", "SSH public key string to add")
		.option("--public-key-file <keyFile>", "Path to a .pub file containing the SSH public key")
		.action(async (options) => {
			const result = await addSshKeyToSandbox(
				options.orgId,
				options.sandboxId,
				options.publicKey,
				options.publicKeyFile,
			);
			if (result.exitCode === 0 && result.data) {
				const { url } = result.data as { url: string };
				console.log("Successfully added SSH key to sandbox.");
				console.log("");
				console.log(`Sandbox domain is: ${url}`);
				console.log("");
				console.log("To SSH into this sandbox, run:");
				console.log(
					`  chunk sandboxes ssh --org-id ${options.orgId} --sandbox-id ${options.sandboxId} --identity-file <path/to/private-key>`,
				);
			}
			finalize(result);
		});

	sandboxes
		.command("exec")
		.description("Execute a command in a sandbox")
		.requiredOption("--org-id <orgId>", "Org ID of sandbox")
		.requiredOption("--sandbox-id <sandboxId>", "Sandbox ID of sandbox")
		.requiredOption("--command <command>", "Command to execute")
		.option("--args <args...>", "Arguments to command", [])
		.action(async (options) => {
			const result = await execCommandInSandbox(
				options.orgId,
				options.sandboxId,
				options.command,
				options.args,
			);
			if (result.exitCode === 0 && result.data) {
				console.log(JSON.stringify(result.data, null, 2));
			}
			finalize(result);
		});

	sandboxes
		.command("ssh")
		.description("Run a command on a sandbox over SSH")
		.requiredOption("--org-id <orgId>", "Organization ID")
		.requiredOption("--sandbox-id <sandboxId>", "Sandbox ID")
		.option("--identity-file <keyFile>", "SSH private key to use (defaults to ~/.ssh/chunk_ai)")
		.argument("[args...]", "Command and arguments to run on the sandbox")
		.passThroughOptions()
		.action(async (args, options) => {
			const result = await runOverSsh(options.orgId, options.sandboxId, args, options.identityFile);
			if (result.data) {
				const { stdout, stderr } = result.data as { stdout: string; stderr: string };
				if (stdout) process.stdout.write(stdout);
				if (stderr) process.stderr.write(stderr);
			}
			finalize(result);
		});

	sandboxes
		.command("prepare")
		.description("[EXPERIMENTAL] Prepare the hook environment before a session begins")
		.option("--docker-sudo", "Run docker commands with sudo", false)
		.action(async (opts: { dockerSudo: boolean }) =>
			finalize(await runSandboxPrepare({ dockerSudo: opts.dockerSudo })),
		);

	sandboxes
		.command("sync")
		.description("Sync the current working directory to a sandbox over SSH")
		.requiredOption("--org-id <orgId>", "Organization ID")
		.requiredOption("--sandbox-id <sandboxId>", "Sandbox ID")
		.option("--dest <path>", "Destination path on sandbox", "/workspace")
		.option("--identity-file <keyFile>", "SSH private key to use (defaults to ~/.ssh/chunk_ai)")
		.option("--bootstrap", "Clone the repo on the sandbox before syncing", false)
		.action(async (options) => {
			const result = await syncToSandbox(
				options.orgId,
				options.sandboxId,
				options.dest,
				options.identityFile,
				options.bootstrap,
			);
			if (result.exitCode === 0 && result.data) {
				const { synced, reason } = result.data as { synced: boolean; reason?: string };
				if (synced) {
					console.log("Synced.");
				} else {
					console.log(reason ?? "Nothing to sync.");
				}
			}
			finalize(result);
		});
}
