import { readFileSync } from "node:fs";
import {
	addSandboxSshKey,
	CircleCIError,
	createSandbox,
	createSandboxAccessToken,
	type ExecCommandResponse,
	execCommand,
	listSandboxesForOrg,
	type Sandbox,
} from "../api/circleci";
import type { CommandResult } from "../types/index";
import { bold } from "../ui/colors";
import { printError } from "../utils/errors";

function requireToken(): string | null {
	const token = process.env.CIRCLECI_TOKEN;
	if (!token) {
		printError(
			"CircleCI token not found",
			"CIRCLECI_TOKEN environment variable is not set.",
			"Set CIRCLECI_TOKEN to your CircleCI personal API token.",
		);
		return null;
	}
	return token;
}

export async function listSandboxes(orgId: string): Promise<CommandResult> {
	const token = requireToken();
	if (!token) return { exitCode: 2 };

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
	const token = requireToken();
	if (!token) return { exitCode: 2 };

	let sandbox: Sandbox;
	try {
		sandbox = await createSandbox(organizationId, name, token, image);
	} catch (error) {
		if (error instanceof CircleCIError) {
			printError(
				"Failed to create sandbox",
				error.message,
				"Check your CIRCLECI_TOKEN and org ID.",
			);
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
	const token = requireToken();
	if (!token) return { exitCode: 2 };

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

const PRIVATE_KEY_RE = /-----BEGIN (?:[A-Z]+ )*PRIVATE KEY-----/;

function assertNotPrivateKey(key: string): void {
	if (PRIVATE_KEY_RE.test(key)) {
		throw new Error(
			"This looks like it might be a private key — please provide the public key instead.",
		);
	}
}

export function validatePublicKey(value: string): string {
	assertNotPrivateKey(value);
	return value;
}

export function resolvePublicKeyFile(filePath: string): string {
	let key: string;
	try {
		key = readFileSync(filePath, "utf8").trim();
	} catch (error) {
		const err = error as NodeJS.ErrnoException;
		if (err.code === "ENOENT") {
			throw new Error(`Public key file not found: ${filePath}`, { cause: error });
		}
		throw new Error(`Could not read public key file: ${(error as Error).message}`, {
			cause: error,
		});
	}
	assertNotPrivateKey(key);
	return key;
}

export async function addSshKeyToSandbox(
	organizationId: string,
	sandboxId: string,
	publicKey: string | undefined,
	publicKeyFile: string | undefined,
): Promise<CommandResult> {
	const token = requireToken();
	if (!token) return { exitCode: 2 };

	if (publicKey && publicKeyFile) {
		printError("SSH key error", "--public-key and --public-key-file are mutually exclusive");
		return { exitCode: 2 };
	}

	let resolvedKey: string;
	try {
		if (publicKeyFile) {
			resolvedKey = resolvePublicKeyFile(publicKeyFile);
		} else if (publicKey) {
			resolvedKey = validatePublicKey(publicKey);
		} else {
			printError("SSH key error", "One of --public-key or --public-key-file is required");
			return { exitCode: 2 };
		}
	} catch (error) {
		printError("SSH key error", error instanceof Error ? error.message : String(error));
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

	let result: Awaited<ReturnType<typeof addSandboxSshKey>>;
	try {
		result = await addSandboxSshKey(resolvedKey, accessToken);
	} catch (error) {
		if (error instanceof CircleCIError) {
			printError("Failed to add SSH key", error.message);
			return { exitCode: 2 };
		}
		throw error;
	}

	console.log(JSON.stringify(result, null, 2));

	return { exitCode: 0 };
}
