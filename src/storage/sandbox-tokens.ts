import { createSandboxAccessToken } from "../api/circleci";
import { deleteSecret, loadSecret, saveSecret } from "./keychain";

const REFRESH_BUFFER_MS = 60_000; // refresh if within 60s of expiry

function sandboxTokenKey(sandboxId: string): string {
	return `sandbox-token-${sandboxId}`;
}

function getTokenExpiry(jwt: string): Date | null {
	try {
		const segment = jwt.split(".")[1];
		if (!segment) return null;
		const payload = JSON.parse(atob(segment));
		return typeof payload.exp === "number" ? new Date(payload.exp * 1000) : null;
	} catch {
		return null;
	}
}

function isTokenFresh(jwt: string): boolean {
	const expiry = getTokenExpiry(jwt);
	if (!expiry) return true; // no exp claim — assume valid
	return expiry.getTime() - Date.now() > REFRESH_BUFFER_MS;
}

/**
 * Returns a valid sandbox access token, using a cached keychain entry when
 * available and fresh, generating and caching a new one when not.
 */
export async function getSandboxAccessToken(
	sandboxId: string,
	organizationId: string,
	circleciToken: string,
): Promise<string> {
	const key = sandboxTokenKey(sandboxId);
	const cached = await loadSecret(key);

	if (cached && isTokenFresh(cached)) {
		return cached;
	}

	const { access_token } = await createSandboxAccessToken(sandboxId, organizationId, circleciToken);
	await saveSecret(key, access_token);
	return access_token;
}

export async function clearSandboxAccessToken(sandboxId: string): Promise<void> {
	await deleteSecret(sandboxTokenKey(sandboxId));
}
