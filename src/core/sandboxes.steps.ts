import { readFileSync } from "node:fs";

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
