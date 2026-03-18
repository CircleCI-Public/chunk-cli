import {
	addSandboxSshKey,
	CircleCIError,
	createSandbox,
	createSandboxAccessToken,
	execCommand,
	listSandboxesForOrg,
} from "../api/circleci";
import type { CommandResult } from "../types/index";
import { generatePatch, getCurrentBranch, resolveRemoteBase } from "../utils/git";
import { detectGitHubOrgAndRepo } from "../utils/git-remote";
import { execOverSsh, shellEscape } from "../utils/ssh";
import { requireToken } from "../utils/tokens";
import { openSandboxSession, type SandboxSession } from "./sandbox-session";
import {
	buildSandboxInitCommand,
	resolvePublicKeyFile,
	validatePublicKey,
} from "./sandboxes.steps";

async function execOverSshSafe(
	...args: Parameters<typeof execOverSsh>
): Promise<Awaited<ReturnType<typeof execOverSsh>> | CommandResult> {
	try {
		return await execOverSsh(...args);
	} catch (error) {
		return {
			exitCode: 1,
			error: {
				title: "SSH connection failed",
				detail: error instanceof Error ? error.message : String(error),
				suggestion: "Check that the sandbox is running and your SSH key is registered.",
			},
		};
	}
}

async function withCircleCIError<T>(
	fn: () => Promise<T>,
	title: string,
	suggestion?: string,
): Promise<T | CommandResult> {
	try {
		return await fn();
	} catch (error) {
		if (error instanceof CircleCIError) {
			return {
				exitCode: 2,
				error: {
					title,
					detail: error.message,
					...(suggestion !== undefined && { suggestion }),
				},
			};
		}
		throw error;
	}
}

// Returns the access token string, or a CommandResult describing the failure.
function fetchAccessToken(
	sandboxId: string,
	organizationId: string,
	token: string,
): Promise<string | CommandResult> {
	return withCircleCIError(
		async () => {
			const { access_token } = await createSandboxAccessToken(sandboxId, organizationId, token);
			return access_token;
		},
		"Failed to get sandbox access token",
		"Check your CIRCLE_TOKEN, sandbox ID, and org ID.",
	);
}

// Returns the sandbox session, or a CommandResult describing the failure.
function openSession(
	sandboxId: string,
	organizationId: string,
	token: string,
	identityFile?: string,
): Promise<SandboxSession | CommandResult> {
	return withCircleCIError(
		() => openSandboxSession(sandboxId, organizationId, token, identityFile),
		"Failed to open sandbox session",
		"Check your CIRCLE_TOKEN, sandbox ID, and org ID.",
	);
}

export async function listSandboxes(orgId: string): Promise<CommandResult> {
	const token = requireToken();
	if (!token) return { exitCode: 2 };

	return withCircleCIError<CommandResult>(
		async () => {
			const { sandboxes } = await listSandboxesForOrg(orgId, token);
			return { exitCode: 0, data: sandboxes };
		},
		"CircleCI API error",
		"Check your CIRCLE_TOKEN and org ID.",
	);
}

export async function createNewSandbox(
	organizationId: string,
	name: string,
	image?: string,
): Promise<CommandResult> {
	const token = requireToken();
	if (!token) return { exitCode: 2 };

	return withCircleCIError<CommandResult>(
		async () => {
			const sandbox = await createSandbox(organizationId, name, token, image);
			return { exitCode: 0, data: sandbox };
		},
		"Failed to create sandbox",
		"Check your CIRCLE_TOKEN and org ID.",
	);
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

	return withCircleCIError<CommandResult>(async () => {
		const result = await execCommand(command, args, accessToken);
		return { exitCode: 0, data: result };
	}, "Failed to execute command");
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

	return withCircleCIError<CommandResult>(async () => {
		const result = await addSandboxSshKey(resolvedKey, accessToken);
		return { exitCode: 0, data: result };
	}, "Failed to add SSH key");
}

export async function runOverSsh(
	organizationId: string,
	sandboxId: string,
	command: string[],
	identityFile?: string,
): Promise<CommandResult> {
	const token = requireToken();
	if (!token) return { exitCode: 2 };

	const session = await openSession(sandboxId, organizationId, token, identityFile);
	if ("exitCode" in session) return session;

	const result = await execOverSshSafe(
		session.sandboxUrl,
		session.identityFile,
		session.knownHostsFile,
		command,
	);
	if (!("stdout" in result)) return result;
	return {
		exitCode: result.exitCode === 0 ? 0 : 1,
		data: { stdout: result.stdout, stderr: result.stderr },
	};
}

export async function syncToSandbox(
	organizationId: string,
	sandboxId: string,
	dest: string,
	identityFile?: string,
	bootstrap = false,
): Promise<CommandResult> {
	const token = requireToken();
	if (!token) return { exitCode: 2 };

	const session = await openSession(sandboxId, organizationId, token, identityFile);
	if ("exitCode" in session) return session;

	const cwd = process.cwd();

	if (bootstrap) {
		let repoUrl: string;
		try {
			const { org, repo } = await detectGitHubOrgAndRepo();
			repoUrl = `https://github.com/${org}/${repo}.git`;
		} catch (error) {
			return {
				exitCode: 2,
				error: {
					title: "Bootstrap failed",
					detail: error instanceof Error ? error.message : String(error),
				},
			};
		}
		const branch = await getCurrentBranch(cwd);
		const initCmd = buildSandboxInitCommand(repoUrl, branch, dest);
		const initResult = await execOverSshSafe(
			session.sandboxUrl,
			session.identityFile,
			session.knownHostsFile,
			["bash", "-c", initCmd],
		);
		if (!("stdout" in initResult)) return initResult;
		if (initResult.exitCode !== 0) {
			return {
				exitCode: 1,
				error: {
					title: "Bootstrap failed",
					detail: initResult.stderr || "git clone exited with a non-zero status.",
				},
			};
		}
	}

	const remoteBase = await resolveRemoteBase(cwd);
	if (!remoteBase) {
		return {
			exitCode: 2,
			error: {
				title: "Could not resolve remote base",
				detail: "No upstream tracking branch or origin/HEAD found.",
				suggestion: "Push your branch or ensure the repository has a remote configured.",
			},
		};
	}

	const patch = await generatePatch(cwd, remoteBase.sha);
	if (!patch) {
		return {
			exitCode: 0,
			data: { synced: false, reason: "No local changes relative to remote base." },
		};
	}

	const result = await execOverSshSafe(
		session.sandboxUrl,
		session.identityFile,
		session.knownHostsFile,
		[
			"bash",
			"-c",
			`git -C ${shellEscape(dest)} reset --hard ${shellEscape(remoteBase.sha)} && git -C ${shellEscape(dest)} clean -fd && git -C ${shellEscape(dest)} apply`,
		],
		{ stdin: patch },
	);
	if (!("stdout" in result)) return result;
	if (result.exitCode !== 0) {
		return {
			exitCode: 1,
			error: {
				title: "Sync failed",
				detail: result.stderr || "git apply exited with a non-zero status.",
			},
		};
	}
	return { exitCode: 0, data: { synced: true } };
}
