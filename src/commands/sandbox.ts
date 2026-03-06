import { CircleCIError, listSandboxesForOrg } from "../api/circleci";
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
			printError(
				"CircleCI API error",
				error.message,
				"Check your CIRCLECI_TOKEN and org ID.",
			);
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
