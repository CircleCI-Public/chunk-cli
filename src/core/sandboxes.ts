import {
	addSandboxSshKey,
	CircleCIError,
	createSandbox,
	createSandboxAccessToken,
	execCommand,
	listSandboxesForOrg,
} from "../api/circleci";
import type { CommandResult } from "../types/index";
import { requireToken } from "../utils/tokens";
import { resolvePublicKeyFile, validatePublicKey } from "./sandboxes.steps";

// Returns the access token string, or a CommandResult describing the failure.
async function fetchAccessToken(
	sandboxId: string,
	organizationId: string,
	token: string,
): Promise<string | CommandResult> {
	try {
		const { access_token } = await createSandboxAccessToken(sandboxId, organizationId, token);
		return access_token;
	} catch (error) {
		if (error instanceof CircleCIError) {
			return {
				exitCode: 2,
				error: {
					title: "Failed to get sandbox access token",
					detail: error.message,
					suggestion: "Check your CIRCLE_TOKEN, sandbox ID, and org ID.",
				},
			};
		}
		throw error;
	}
}

export async function listSandboxes(orgId: string): Promise<CommandResult> {
	const token = requireToken();
	if (!token) return { exitCode: 2 };

	try {
		const { sandboxes } = await listSandboxesForOrg(orgId, token);
		return { exitCode: 0, data: sandboxes };
	} catch (error) {
		if (error instanceof CircleCIError) {
			return {
				exitCode: 2,
				error: {
					title: "CircleCI API error",
					detail: error.message,
					suggestion: "Check your CIRCLE_TOKEN and org ID.",
				},
			};
		}
		throw error;
	}
}

export async function createNewSandbox(
	organizationId: string,
	name: string,
	image?: string,
): Promise<CommandResult> {
	const token = requireToken();
	if (!token) return { exitCode: 2 };

	try {
		const sandbox = await createSandbox(organizationId, name, token, image);
		return { exitCode: 0, data: sandbox };
	} catch (error) {
		if (error instanceof CircleCIError) {
			return {
				exitCode: 2,
				error: {
					title: "Failed to create sandbox",
					detail: error.message,
					suggestion: "Check your CIRCLE_TOKEN and org ID.",
				},
			};
		}
		throw error;
	}
}

export async function execCommandInSandbox(
	organizationId: string,
	sandboxId: string,
	command: string,
	args: string[],
): Promise<CommandResult> {
	const token = requireToken();
	if (!token) return { exitCode: 2 };

	const accessToken = await fetchAccessToken(sandboxId, organizationId, token);
	if (typeof accessToken !== "string") return accessToken;

	try {
		const result = await execCommand(command, args, accessToken);
		return { exitCode: 0, data: result };
	} catch (error) {
		if (error instanceof CircleCIError) {
			return {
				exitCode: 2,
				error: { title: "Failed to execute command", detail: error.message },
			};
		}
		throw error;
	}
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
		return {
			exitCode: 2,
			error: {
				title: "SSH key error",
				detail: "--public-key and --public-key-file are mutually exclusive",
			},
		};
	}

	let resolvedKey: string;
	try {
		if (publicKeyFile) {
			resolvedKey = resolvePublicKeyFile(publicKeyFile);
		} else if (publicKey) {
			resolvedKey = validatePublicKey(publicKey);
		} else {
			return {
				exitCode: 2,
				error: {
					title: "SSH key error",
					detail: "One of --public-key or --public-key-file is required",
				},
			};
		}
	} catch (error) {
		return {
			exitCode: 2,
			error: {
				title: "SSH key error",
				detail: error instanceof Error ? error.message : String(error),
			},
		};
	}

	const accessToken = await fetchAccessToken(sandboxId, organizationId, token);
	if (typeof accessToken !== "string") return accessToken;

	try {
		const result = await addSandboxSshKey(resolvedKey, accessToken);
		return { exitCode: 0, data: result };
	} catch (error) {
		if (error instanceof CircleCIError) {
			return {
				exitCode: 2,
				error: { title: "Failed to add SSH key", detail: error.message },
			};
		}
		throw error;
	}
}
