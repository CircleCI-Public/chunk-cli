import { afterEach, describe, expect, it } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { shellJoin, tofuVerifyHostKey } from "../utils/ssh";

const HOST = "sandbox-xyz.sandboxes.example.com";
const FINGERPRINT_A = "aabbccdd11223344aabbccdd11223344aabbccdd11223344aabbccdd11223344";
const FINGERPRINT_B = "deadbeef11223344deadbeef11223344deadbeef11223344deadbeef11223344";

let tmpDir: string;

function makeTmpFile(): string {
	tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-ssh-test-"));
	return path.join(tmpDir, "known_hosts");
}

afterEach(() => {
	if (tmpDir) {
		fs.rmSync(tmpDir, { recursive: true, force: true });
		tmpDir = "";
	}
});

describe("tofuVerifyHostKey", () => {
	it("accepts and writes an unknown host on first use", () => {
		const file = makeTmpFile();
		const result = tofuVerifyHostKey(file, HOST, FINGERPRINT_A);
		expect(result).toBe(true);
		const contents = fs.readFileSync(file, "utf8");
		expect(contents).toContain(`${HOST} ${FINGERPRINT_A}`);
	});

	it("accepts a known host with matching fingerprint", () => {
		const file = makeTmpFile();
		fs.writeFileSync(file, `${HOST} ${FINGERPRINT_A}\n`, "utf8");
		const result = tofuVerifyHostKey(file, HOST, FINGERPRINT_A);
		expect(result).toBe(true);
	});

	it("rejects a known host with a mismatched fingerprint", () => {
		const file = makeTmpFile();
		fs.writeFileSync(file, `${HOST} ${FINGERPRINT_A}\n`, "utf8");
		const result = tofuVerifyHostKey(file, HOST, FINGERPRINT_B);
		expect(result).toBe(false);
	});

	it("creates parent directories if they do not exist", () => {
		tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-ssh-test-"));
		const nested = path.join(tmpDir, "a", "b", "c", "known_hosts");
		const result = tofuVerifyHostKey(nested, HOST, FINGERPRINT_A);
		expect(result).toBe(true);
		expect(fs.existsSync(nested)).toBe(true);
	});
});

describe("shellJoin", () => {
	it("wraps each arg in single quotes", () => {
		expect(shellJoin(["echo", "hello"])).toBe("'echo' 'hello'");
	});

	it("escapes embedded single quotes", () => {
		expect(shellJoin(["it's"])).toBe("'it'\\''s'");
	});

	it("preserves shell metacharacters as literals", () => {
		const result = shellJoin(["bash", "-c", "echo hello && echo world"]);
		expect(result).toBe("'bash' '-c' 'echo hello && echo world'");
	});
});
