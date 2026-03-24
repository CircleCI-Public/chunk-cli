import { afterEach, beforeEach, describe, expect, it, mock } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { validateLocally, validateOnSandbox } from "../core/validate";
import { loadValidateCommands } from "../core/validate.steps";
import type { SshExec } from "../utils/ssh";

// Mock withSshConnection so validateOnSandbox tests can control SSH execution
// without a real connection. execOverSsh is provided as a no-op to avoid
// undefined errors if the mock leaks into other modules that import it.
const mockWithSshConnection = mock();

mock.module("../utils/ssh", () => ({
	withSshConnection: mockWithSshConnection,
	execOverSsh: mock(),
	shellEscape: (s: string) => `'${s.replace(/'/g, "'\\''")}'`,
	shellJoin: (args: string[]) => args.map((s: string) => `'${s}'`).join(" "),
}));

describe("loadValidateCommands", () => {
	let tmpDir: string;

	beforeEach(() => {
		tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-validate-test-"));
	});

	afterEach(() => {
		fs.rmSync(tmpDir, { recursive: true, force: true });
	});

	function writeConfig(content: string) {
		const configDir = path.join(tmpDir, ".chunk", "hook");
		fs.mkdirSync(configDir, { recursive: true });
		fs.writeFileSync(path.join(configDir, "config.yml"), content);
	}

	it("returns empty commands when config file does not exist", () => {
		expect(loadValidateCommands(tmpDir)).toEqual({ commands: [] });
	});

	it("returns empty commands when config has no execs key", () => {
		writeConfig("other_key: value\n");
		expect(loadValidateCommands(tmpDir)).toEqual({ commands: [] });
	});

	it("returns empty commands when execs is empty", () => {
		writeConfig("execs: {}\n");
		expect(loadValidateCommands(tmpDir)).toEqual({ commands: [] });
	});

	it("returns commands from valid execs config", () => {
		writeConfig("execs:\n  tests:\n    command: bun test\n  lint:\n    command: bun run lint\n");
		expect(loadValidateCommands(tmpDir)).toEqual({ commands: ["bun test", "bun run lint"] });
	});

	it("skips execs with empty commands", () => {
		writeConfig('execs:\n  tests:\n    command: bun test\n  stub:\n    command: ""\n');
		expect(loadValidateCommands(tmpDir)).toEqual({ commands: ["bun test"] });
	});

	it("skips execs with placeholder commands", () => {
		writeConfig(
			"execs:\n  tests:\n    command: bun test\n  changed:\n    command: bun test {{CHANGED_FILES}}\n",
		);
		expect(loadValidateCommands(tmpDir)).toEqual({ commands: ["bun test"] });
	});

	it("skips execs with no command field", () => {
		writeConfig("execs:\n  tests:\n    command: bun test\n  noop:\n    timeout: 60\n");
		expect(loadValidateCommands(tmpDir)).toEqual({ commands: ["bun test"] });
	});

	it("returns error result on malformed YAML without throwing", () => {
		writeConfig("execs: [\nunclosed");
		const result = loadValidateCommands(tmpDir);
		expect(result.commands).toEqual([]);
		expect("error" in result).toBe(true);
	});
});

describe("validateLocally", () => {
	let tmpDir: string;
	let originalCwd: string;

	beforeEach(() => {
		tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-validate-test-"));
		// Create a .git dir so findRepoRoot() resolves to tmpDir
		fs.mkdirSync(path.join(tmpDir, ".git"));
		originalCwd = process.cwd();
		process.chdir(tmpDir);
	});

	afterEach(() => {
		process.chdir(originalCwd);
		fs.rmSync(tmpDir, { recursive: true, force: true });
	});

	function writeProjectConfig(config: { installCommand?: string; testCommand?: string }) {
		const configDir = path.join(tmpDir, ".chunk");
		fs.mkdirSync(configDir, { recursive: true });
		fs.writeFileSync(path.join(configDir, "config.json"), JSON.stringify(config, null, 2));
	}

	it("returns ok: false with 'No validate commands configured' when config.json is missing", async () => {
		const result = await validateLocally(tmpDir);
		expect(result.ok).toBe(false);
		if (!result.ok) {
			expect(result.error).toBe("No validate commands configured");
		}
	});

	it("returns ok: false with 'No validate commands configured' when no commands exist", async () => {
		writeProjectConfig({});
		const result = await validateLocally(tmpDir);
		expect(result.ok).toBe(false);
		if (!result.ok) {
			expect(result.error).toBe("No validate commands configured");
		}
	});

	it("returns ok: true with empty skipped when all commands pass", async () => {
		writeProjectConfig({ installCommand: "true", testCommand: "true" });
		const result = await validateLocally(tmpDir);
		expect(result.ok).toBe(true);
		if (result.ok) {
			expect(result.results).toEqual([
				{ command: "true", exitCode: 0, stdout: "", stderr: "" },
				{ command: "true", exitCode: 0, stdout: "", stderr: "" },
			]);
			expect(result.skipped).toEqual([]);
		}
	});

	it("stops on first failure and moves remaining commands to skipped", async () => {
		writeProjectConfig({ installCommand: "false", testCommand: "true" });
		const result = await validateLocally(tmpDir);
		expect(result.ok).toBe(true);
		if (result.ok) {
			expect(result.results).toEqual([{ command: "false", exitCode: 1, stdout: "", stderr: "" }]);
			expect(result.skipped).toEqual(["true"]);
		}
	});

	it("returns subprocess stdout in step result", async () => {
		// Use printf (not echo) to get predictable output without a trailing newline
		writeProjectConfig({ testCommand: "printf hello" });
		const result = await validateLocally(tmpDir);
		expect(result.ok).toBe(true);
		if (result.ok) {
			expect(result.results[0]?.stdout).toBe("hello");
		}
	});
});

describe("validateOnSandbox", () => {
	let tmpDir: string;
	let originalCwd: string;
	let keyDir: string;
	let identityFile: string;
	const fetchMock = mock();
	let originalFetch: typeof globalThis.fetch;

	beforeEach(() => {
		tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-validate-test-"));
		fs.mkdirSync(path.join(tmpDir, ".git"));
		originalCwd = process.cwd();
		process.chdir(tmpDir);
		originalFetch = globalThis.fetch;
		// @ts-expect-error - Mock doesn't fully implement fetch type
		globalThis.fetch = fetchMock;
		// Provide real SSH key files so openSandboxSession can read them
		keyDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-key-test-"));
		identityFile = path.join(keyDir, "test_key");
		fs.writeFileSync(identityFile, "fake-private-key");
		fs.writeFileSync(`${identityFile}.pub`, "ssh-ed25519 AAAA fake-public-key test@test.com");
		mockWithSshConnection.mockReset();
	});

	afterEach(() => {
		process.chdir(originalCwd);
		fs.rmSync(tmpDir, { recursive: true, force: true });
		fs.rmSync(keyDir, { recursive: true, force: true });
		fetchMock.mockReset();
		globalThis.fetch = originalFetch;
	});

	function writeProjectConfig(config: { installCommand?: string; testCommand?: string }) {
		const configDir = path.join(tmpDir, ".chunk");
		fs.mkdirSync(configDir, { recursive: true });
		fs.writeFileSync(path.join(configDir, "config.json"), JSON.stringify(config, null, 2));
	}

	function sshKeyResponse(url = "sandbox.example.com") {
		return {
			ok: true,
			status: 200,
			text: async () => JSON.stringify({ url }),
		} as Response;
	}

	function errorResponse(status: number) {
		return {
			ok: false,
			status,
			text: async () => JSON.stringify({ message: "Error" }),
		} as Response;
	}

	it("returns ok: false with 'No validate commands configured' when no commands exist", async () => {
		const result = await validateOnSandbox("org-1", "sandbox-1", "token", tmpDir);
		expect(result.ok).toBe(false);
		if (!result.ok) {
			expect(result.error).toBe("No validate commands configured");
		}
	});

	it("returns ok: false with hint when addSandboxSshKey returns a CircleCIError", async () => {
		writeProjectConfig({ testCommand: "bun test" });
		fetchMock.mockImplementation(async () => errorResponse(401));
		const result = await validateOnSandbox("org-1", "sandbox-1", "token", tmpDir, identityFile);
		expect(result.ok).toBe(false);
		if (!result.ok) {
			expect(result.error).toBe("Invalid CircleCI API token");
			expect(result.hint).toMatch(/CIRCLE_TOKEN/);
		}
	});

	it("rethrows non-CircleCIError from addSandboxSshKey", async () => {
		writeProjectConfig({ testCommand: "bun test" });
		fetchMock.mockImplementation(async () => ({
			ok: true,
			status: 200,
			text: async () => {
				throw new TypeError("stream error");
			},
		}));
		await expect(
			validateOnSandbox("org-1", "sandbox-1", "token", tmpDir, identityFile),
		).rejects.toBeInstanceOf(TypeError);
	});

	it("returns ok: false when addSandboxSshKey fails with a CircleCIError", async () => {
		writeProjectConfig({ testCommand: "bun test" });
		fetchMock.mockImplementationOnce(async () => errorResponse(500));
		const result = await validateOnSandbox("org-1", "sandbox-1", "token", tmpDir, identityFile);
		expect(result.ok).toBe(false);
		if (!result.ok) {
			expect(result.error).toContain("CircleCI server error (500)");
		}
	});

	it("returns ok: false when SSH connection fails", async () => {
		writeProjectConfig({ testCommand: "bun test" });
		fetchMock.mockImplementationOnce(async () => sshKeyResponse());
		mockWithSshConnection.mockRejectedValueOnce(new Error("Timed out waiting for TLS connection"));
		const result = await validateOnSandbox("org-1", "sandbox-1", "token", tmpDir, identityFile);
		expect(result.ok).toBe(false);
		if (!result.ok) {
			expect(result.error).toBe("Timed out waiting for TLS connection");
			expect(result.hint).toMatch(/SSH key is registered/);
		}
	});

	it("stops on non-zero exit code and moves remaining commands to skipped", async () => {
		writeProjectConfig({ installCommand: "false", testCommand: "true" });
		fetchMock.mockImplementationOnce(async () => sshKeyResponse());
		mockWithSshConnection.mockImplementationOnce(
			async (
				_url: string,
				_key: string,
				_hosts: string,
				fn: (exec: SshExec) => Promise<unknown>,
			) => {
				const execMock: SshExec = mock().mockResolvedValueOnce({
					exitCode: 1,
					stdout: "",
					stderr: "",
				});
				return fn(execMock);
			},
		);
		const result = await validateOnSandbox("org-1", "sandbox-1", "token", tmpDir, identityFile);
		expect(result.ok).toBe(true);
		if (result.ok) {
			expect(result.results).toEqual([{ command: "false", exitCode: 1, stdout: "", stderr: "" }]);
			expect(result.skipped).toEqual(["true"]);
		}
	});

	it("returns ok: true with all results and empty skipped when all commands pass", async () => {
		writeProjectConfig({ installCommand: "bun install", testCommand: "bun test" });
		fetchMock.mockImplementationOnce(async () => sshKeyResponse());
		mockWithSshConnection.mockImplementationOnce(
			async (
				_url: string,
				_key: string,
				_hosts: string,
				fn: (exec: SshExec) => Promise<unknown>,
			) => {
				const execMock: SshExec = mock()
					.mockResolvedValueOnce({ exitCode: 0, stdout: "install done\n", stderr: "" })
					.mockResolvedValueOnce({ exitCode: 0, stdout: "test passed\n", stderr: "" });
				return fn(execMock);
			},
		);
		const result = await validateOnSandbox("org-1", "sandbox-1", "token", tmpDir, identityFile);
		expect(result.ok).toBe(true);
		if (result.ok) {
			expect(result.results).toEqual([
				{ command: "bun install", exitCode: 0, stdout: "install done\n", stderr: "" },
				{ command: "bun test", exitCode: 0, stdout: "test passed\n", stderr: "" },
			]);
			expect(result.skipped).toEqual([]);
		}
	});
});
