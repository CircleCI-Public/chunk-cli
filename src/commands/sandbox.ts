import {
	CircleCIError,
	type Sandbox,
	createSandbox,
	createSandboxAccessToken,
	type ExecCommandResponse,
	execCommand,
	listSandboxesForOrg,
} from "../api/circleci";
import type { CommandResult } from "../types/index";
import { bold } from "../ui/colors";
import { printError } from "../utils/errors";

export async function listSandboxes(orgId: string): Promise<CommandResult> {
	const token = process.env.CIRCLECI_TOKEN;
	if (!token) {
		printError(
			"CircleCI token not found",
			"CIRCLECI_TOKEN environment variable is not set.",
			"Set CIRCLECI_TOKEN to your CircleCI personal API token.",
		);
		return { exitCode: 2 };
	}

	console.log(`\n${bold("Sandboxes")}\n`);

	let result: Awaited<ReturnType<typeof listSandboxesForOrg>>;
	try {
		result = await listSandboxesForOrg(orgId, token);
	} catch (error) {
		if (error instanceof CircleCIError) {
			printError("CircleCI API error", error.message, "Check your CIRCLECI_TOKEN and org ID.");
			return { exitCode: 2 };
		}
		throw error;
	}

	if (result.sandboxes.length === 0) {
		console.log("No sandboxes found.\n");
		return { exitCode: 0 };
	}

	for (const sandbox of result.sandboxes) {
		console.log(`  ${sandbox.name}  ${sandbox.id}`);
	}
	console.log("");

	return { exitCode: 0 };
}

export async function createNewSandbox(
	organizationId: string,
	name: string,
	image?: string,
): Promise<CommandResult> {
	const token = process.env.CIRCLECI_TOKEN;
	if (!token) {
		printError(
			"CircleCI token not found",
			"CIRCLECI_TOKEN environment variable is not set.",
			"Set CIRCLECI_TOKEN to your CircleCI personal API token.",
		);
		return { exitCode: 2 };
	}

	let sandbox: Sandbox;
	try {
		sandbox = await createSandbox(organizationId, name, token, image);
	} catch (error) {
		if (error instanceof CircleCIError) {
			printError("Failed to create sandbox", error.message, "Check your CIRCLECI_TOKEN and org ID.");
			return { exitCode: 2 };
		}
		throw error;
	}

	console.log(JSON.stringify(sandbox, null, 2));

	return { exitCode: 0 };
}

export async function execCommandInSandbox(
	organizationId: string,
	sandboxId: string,
	command: string,
	args: string[],
): Promise<CommandResult> {
	const token = process.env.CIRCLECI_TOKEN;
	if (!token) {
		printError(
			"CircleCI token not found",
			"CIRCLECI_TOKEN environment variable is not set.",
			"Set CIRCLECI_TOKEN to your CircleCI personal API token.",
		);
		return { exitCode: 2 };
	}

	let accessToken: string;
	try {
		const tokenResp = await createSandboxAccessToken(sandboxId, organizationId, token);
		accessToken = tokenResp.access_token;
	} catch (error) {
		if (error instanceof CircleCIError) {
			printError(
				"Failed to get sandbox access token",
				error.message,
				"Check your CIRCLECI_TOKEN, sandbox ID, and org ID.",
			);
			return { exitCode: 2 };
		}
		throw error;
	}

	let result: ExecCommandResponse;
	try {
		result = await execCommand(command, args, accessToken);
	} catch (error) {
		if (error instanceof CircleCIError) {
			printError("Failed to execute command", error.message);
			return { exitCode: 2 };
		}
		throw error;
	}

	console.log(JSON.stringify(result, null, 2));

	return { exitCode: 0 };
}
