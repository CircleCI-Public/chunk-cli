import { deleteSecret, loadSecret, saveSecret } from "../storage/keychain";
import type { CommandResult } from "../types/index";
import { bold, dim } from "../ui/colors";
import { printSuccess, printWarning } from "../ui/format";
import { promptInput } from "../ui/prompt";
import { printError } from "../utils/errors";

export const CIRCLECI_TOKEN_KEY = "https://circleci.com";

/**
 * Resolve the CircleCI token from env → keychain.
 * Returns undefined if not found in either place.
 */
export async function resolveCircleCIToken(): Promise<string | undefined> {
	return process.env.CIRCLECI_TOKEN ?? (await loadSecret(CIRCLECI_TOKEN_KEY));
}

export async function runCircleCIAuthLogin(): Promise<CommandResult> {
	console.log(`\n${bold("Chunk CLI - CircleCI Token Setup")}\n`);
	console.log("Enter your CircleCI personal API token.");
	console.log("The token will be stored in your OS keychain and never displayed.\n");

	const token = await promptInput("CircleCI Token: ", { hidden: true });

	if (!token || token.trim() === "") {
		console.log("");
		printError(
			"Token cannot be empty",
			"You must enter a CircleCI personal API token.",
			"Get a token from https://app.circleci.com/settings/user/tokens",
		);
		return { exitCode: 2 };
	}

	await saveSecret(CIRCLECI_TOKEN_KEY, token.trim());

	printSuccess("CircleCI token saved to keychain.");
	console.log(dim("Stored under service 'com.circleci.cli' (shared with CircleCI CLI)."));
	console.log("You can now use sandbox commands without setting CIRCLECI_TOKEN.\n");

	return { exitCode: 0 };
}

export async function runCircleCIAuthLogout(): Promise<CommandResult> {
	const deleted = await deleteSecret(CIRCLECI_TOKEN_KEY);

	if (!deleted) {
		printWarning("No CircleCI token found in keychain.");
		if (process.env.CIRCLECI_TOKEN) {
			console.log(dim("Note: CIRCLECI_TOKEN is still set in your environment."));
		}
		return { exitCode: 0 };
	}

	printSuccess("CircleCI token removed from keychain.");
	return { exitCode: 0 };
}

export async function runCircleCIAuthStatus(): Promise<CommandResult> {
	console.log(`\n${bold("CircleCI Authentication Status")}\n`);

	const W = 14;
	const fromEnv = !!process.env.CIRCLECI_TOKEN;
	const fromKeychain = !!(await loadSecret(CIRCLECI_TOKEN_KEY));

	if (!fromEnv && !fromKeychain) {
		printWarning("No CircleCI token found.");
		console.log(dim("Set CIRCLECI_TOKEN or run: chunk auth circleci login\n"));
		return { exitCode: 0 };
	}

	const source = fromEnv ? "env (CIRCLECI_TOKEN)" : "keychain";
	console.log(`${"Source:".padEnd(W)} ${source}`);
	if (fromEnv && fromKeychain) {
		console.log(dim("Note: env var takes precedence over keychain."));
	}
	console.log("");

	return { exitCode: 0 };
}
