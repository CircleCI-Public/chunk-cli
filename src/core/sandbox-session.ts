import { existsSync, readFileSync } from "node:fs";
import { homedir } from "node:os";
import { join } from "node:path";
import { addSandboxSshKey, CircleCIError, createSandboxAccessToken } from "../api/circleci";

const SSH_DIR = join(homedir(), ".ssh");
export const CHUNK_PRIVATE_KEY_PATH = join(SSH_DIR, "chunk_ai");
export const CHUNK_PUBLIC_KEY_PATH = join(SSH_DIR, "chunk_ai.pub");
export const CHUNK_KNOWN_HOSTS_PATH = join(SSH_DIR, "chunk_ai_known_hosts");

export interface SandboxSession {
	sandboxUrl: string;
	identityFile: string;
	knownHostsFile: string;
}

export async function openSandboxSession(
	sandboxId: string,
	organizationId: string,
	token: string,
	identityFile?: string,
): Promise<SandboxSession> {
	const { access_token: accessToken } = await createSandboxAccessToken(
		sandboxId,
		organizationId,
		token,
	);

	const privateKeyPath = identityFile ?? CHUNK_PRIVATE_KEY_PATH;
	const publicKeyPath = `${privateKeyPath}.pub`;

	if (!existsSync(privateKeyPath)) {
		throw new Error(
			`SSH key not found: ${privateKeyPath}\n` +
				`Generate one with: ssh-keygen -t ed25519 -f ${privateKeyPath}\n` +
				`Or pass --identity-file to use an existing key.`,
		);
	}

	let publicKey: string;
	try {
		publicKey = readFileSync(publicKeyPath, "utf8").trim();
	} catch (err) {
		const e = err as NodeJS.ErrnoException;
		if (e.code === "ENOENT") {
			throw new Error(
				`SSH public key not found: ${publicKeyPath}\n` +
					`The public key must be co-located with the private key.\n` +
					`Generate a new keypair with: ssh-keygen -t ed25519 -f ${privateKeyPath}`,
			);
		}
		throw err;
	}

	let sandboxUrl: string;
	try {
		const keyResp = await addSandboxSshKey(publicKey, accessToken);
		sandboxUrl = keyResp.url;
	} catch (error) {
		if (error instanceof CircleCIError) {
			throw new CircleCIError(`Failed to register SSH key with sandbox: ${error.message}`);
		}
		throw error;
	}

	return {
		sandboxUrl,
		identityFile: privateKeyPath,
		knownHostsFile: CHUNK_KNOWN_HOSTS_PATH,
	};
}
