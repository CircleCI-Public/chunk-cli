import { afterEach, beforeAll, beforeEach, describe, expect, it } from "bun:test";
import * as crypto from "node:crypto";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { resolvePublicKeyFile, validatePublicKey } from "../commands/sandbox";

// Generated once — ed25519 is fast and covers PKCS8/SPKI formats
let privPKCS8: string;
let pubSPKI: string;

beforeAll(() => {
	const { privateKey, publicKey } = crypto.generateKeyPairSync("ed25519");
	privPKCS8 = (privateKey.export({ type: "pkcs8", format: "pem" }) as string).trim();
	pubSPKI = (publicKey.export({ type: "spki", format: "pem" }) as string).trim();
});

describe("validatePublicKey", () => {
	it("returns a PEM public key unchanged", () => {
		expect(validatePublicKey(pubSPKI)).toBe(pubSPKI);
	});

	it("passes through OpenSSH authorized_keys format unchanged", () => {
		const key =
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl user@host";
		expect(validatePublicKey(key)).toBe(key);
	});

	it("throws for a PKCS8 private key", () => {
		expect(() => validatePublicKey(privPKCS8)).toThrow(/private key/i);
	});

	it.each([
		["OpenSSH", "-----BEGIN OPENSSH PRIVATE KEY-----\nAAA\n-----END OPENSSH PRIVATE KEY-----"],
		["RSA PEM", "-----BEGIN RSA PRIVATE KEY-----\nAAA\n-----END RSA PRIVATE KEY-----"],
		["EC PEM", "-----BEGIN EC PRIVATE KEY-----\nAAA\n-----END EC PRIVATE KEY-----"],
		["DSA PEM", "-----BEGIN DSA PRIVATE KEY-----\nAAA\n-----END DSA PRIVATE KEY-----"],
	])("throws for a %s private key header", (_label, key) => {
		expect(() => validatePublicKey(key)).toThrow(/private key/i);
	});

	it("passes through arbitrary non-private-key content unchanged", () => {
		expect(validatePublicKey("not a key at all")).toBe("not a key at all");
	});
});

describe("resolvePublicKeyFile", () => {
	let tmpDir: string;

	beforeEach(() => {
		tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-sandbox-test-"));
	});

	afterEach(() => {
		fs.rmSync(tmpDir, { recursive: true, force: true });
	});

	it("reads the key from a .pub file when given a valid path", () => {
		const keyFile = path.join(tmpDir, "id_ed25519.pub");
		fs.writeFileSync(keyFile, pubSPKI);
		expect(resolvePublicKeyFile(keyFile)).toBe(pubSPKI);
	});

	it("reads the key from a file without .pub extension when content is valid", () => {
		const keyFile = path.join(tmpDir, "authorized_keys");
		const key =
			"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl user@host";
		fs.writeFileSync(keyFile, key);
		expect(resolvePublicKeyFile(keyFile)).toBe(key);
	});

	it("trims trailing whitespace and newlines from file contents", () => {
		const keyFile = path.join(tmpDir, "id_ed25519.pub");
		fs.writeFileSync(keyFile, `${pubSPKI}\n`);
		expect(resolvePublicKeyFile(keyFile)).toBe(pubSPKI);
	});

	it("throws a descriptive error for a non-existent file path", () => {
		const nonExistentPath = path.join(tmpDir, "nonexistent.pub");
		expect(() => resolvePublicKeyFile(nonExistentPath)).toThrow(/file not found/i);
	});

	it("rejects a private key read from a file", () => {
		const keyFile = path.join(tmpDir, "id_key.pub");
		fs.writeFileSync(keyFile, privPKCS8);
		expect(() => resolvePublicKeyFile(keyFile)).toThrow(/private key/i);
	});

	it("passes through non-key file content that is not a private key", () => {
		const keyFile = path.join(tmpDir, "misc.pub");
		fs.writeFileSync(keyFile, "this is not a key");
		expect(resolvePublicKeyFile(keyFile)).toBe("this is not a key");
	});
});
