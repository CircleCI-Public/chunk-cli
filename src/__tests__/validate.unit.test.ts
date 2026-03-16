import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { loadValidateCommands, runValidate } from "../commands/validate";

// ── loadValidateCommands ───────────────────────────────────────────────────────

describe("loadValidateCommands", () => {
	let tmpDir: string;

	beforeEach(() => {
		tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-validate-test-"));
	});

	afterEach(() => {
		fs.rmSync(tmpDir, { recursive: true, force: true });
	});

	it("returns [] when the config file does not exist", () => {
		expect(loadValidateCommands(tmpDir)).toEqual([]);
	});

	it("returns [] for malformed YAML", () => {
		const configDir = path.join(tmpDir, ".chunk", "hook");
		fs.mkdirSync(configDir, { recursive: true });
		fs.writeFileSync(path.join(configDir, "config.yml"), "{ invalid: yaml: content: ]]]");

		expect(loadValidateCommands(tmpDir)).toEqual([]);
	});

	it("returns [] when the validate key is missing from config", () => {
		const configDir = path.join(tmpDir, ".chunk", "hook");
		fs.mkdirSync(configDir, { recursive: true });
		fs.writeFileSync(path.join(configDir, "config.yml"), "other_key:\n  - foo\n");

		expect(loadValidateCommands(tmpDir)).toEqual([]);
	});

	it("returns [] when validate is not an array", () => {
		const configDir = path.join(tmpDir, ".chunk", "hook");
		fs.mkdirSync(configDir, { recursive: true });
		fs.writeFileSync(path.join(configDir, "config.yml"), "validate: not-an-array\n");

		expect(loadValidateCommands(tmpDir)).toEqual([]);
	});

	it("returns the list of commands from a valid config", () => {
		const configDir = path.join(tmpDir, ".chunk", "hook");
		fs.mkdirSync(configDir, { recursive: true });
		fs.writeFileSync(
			path.join(configDir, "config.yml"),
			"validate:\n  - bun test\n  - bun run typecheck\n",
		);

		expect(loadValidateCommands(tmpDir)).toEqual(["bun test", "bun run typecheck"]);
	});

	it("returns an empty list when validate is an empty array", () => {
		const configDir = path.join(tmpDir, ".chunk", "hook");
		fs.mkdirSync(configDir, { recursive: true });
		fs.writeFileSync(path.join(configDir, "config.yml"), "validate: []\n");

		expect(loadValidateCommands(tmpDir)).toEqual([]);
	});
});

// ── runValidate ────────────────────────────────────────────────────────────────

describe("runValidate", () => {
	let tmpDir: string;
	let originalCwd: () => string;

	beforeEach(() => {
		tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-validate-run-"));
		originalCwd = process.cwd;
		process.cwd = () => tmpDir;
	});

	afterEach(() => {
		process.cwd = originalCwd;
		fs.rmSync(tmpDir, { recursive: true, force: true });
	});

	function writeConfig(commands: string[]): void {
		const configDir = path.join(tmpDir, ".chunk", "hook");
		fs.mkdirSync(configDir, { recursive: true });
		const yaml = `validate:\n${commands.map((c) => `  - ${c}`).join("\n")}\n`;
		fs.writeFileSync(path.join(configDir, "config.yml"), yaml);
	}

	it("returns exitCode 1 when no validate commands are configured", async () => {
		const result = await runValidate();
		expect(result.exitCode).toBe(1);
	});

	it("returns exitCode 0 when all commands succeed", async () => {
		writeConfig(["true", "true"]);

		const result = await runValidate();
		expect(result.exitCode).toBe(0);
	});

	it("returns exitCode 1 when a command fails", async () => {
		writeConfig(["false"]);

		const result = await runValidate();
		expect(result.exitCode).toBe(1);
	});

	it("stops after the first failing command and skips the rest", async () => {
		// Write a sentinel file if the second command runs — it should not
		const sentinel = path.join(tmpDir, "second-ran");
		writeConfig([`exit 1`, `touch ${sentinel}`]);

		await runValidate();

		expect(fs.existsSync(sentinel)).toBe(false);
	});

	it("returns exitCode 0 for a single succeeding command", async () => {
		writeConfig(["echo hello"]);

		const result = await runValidate();
		expect(result.exitCode).toBe(0);
	});
});
