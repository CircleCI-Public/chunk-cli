import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { execSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { resolveRunCommand } from "../core/run";
import { saveCommand } from "../core/run-config";

describe("core/run – resolveRunCommand", () => {
	let testDir: string;

	beforeEach(() => {
		testDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-run-resolve-"));
		execSync("git init", { cwd: testDir, stdio: "ignore" });
		execSync("git config user.email 'test@test.com'", { cwd: testDir, stdio: "ignore" });
		execSync("git config user.name 'Test'", { cwd: testDir, stdio: "ignore" });
		execSync("git commit --allow-empty -m 'init'", { cwd: testDir, stdio: "ignore" });
	});

	afterEach(() => {
		fs.rmSync(testDir, { recursive: true, force: true });
	});

	it("returns not-configured when command is missing and isTTY is false", () => {
		const result = resolveRunCommand(testDir, "lint", {}, false);
		expect(result.type).toBe("not-configured");
		expect(result.name).toBe("lint");
	});

	it("returns needs-setup when command is missing and isTTY is true", () => {
		const result = resolveRunCommand(testDir, "lint", {}, true);
		expect(result.type).toBe("needs-setup");
		expect(result.name).toBe("lint");
	});

	it("returns status-miss when --status and no cache", () => {
		const result = resolveRunCommand(testDir, "test", { status: true });
		expect(result.type).toBe("status-miss");
	});

	it("returns status-cached when --status and cache exists", () => {
		saveCommand(testDir, "test", "echo ok");
		// Run once to populate cache
		resolveRunCommand(testDir, "test", { force: true });

		const result = resolveRunCommand(testDir, "test", { status: true });
		expect(result.type).toBe("status-cached");
		if (result.type === "status-cached") {
			expect(result.exitCode).toBe(0);
		}
	});

	it("returns executed with saveAction skip when running from config", () => {
		saveCommand(testDir, "test", "echo hello");
		const result = resolveRunCommand(testDir, "test", { force: true });
		expect(result.type).toBe("executed");
		if (result.type === "executed") {
			expect(result.saveAction).toBe("skip");
			expect(result.result.status).toBe("pass");
			expect(result.result.exitCode).toBe(0);
		}
	});

	it("returns executed with saveAction save when --cmd and --save", () => {
		const result = resolveRunCommand(
			testDir,
			"test",
			{ cmd: "echo inline", save: true, force: true },
			true,
		);
		expect(result.type).toBe("executed");
		if (result.type === "executed") {
			expect(result.saveAction).toBe("save");
			expect(result.result.output).toContain("inline");
		}
	});

	it("returns executed with saveAction prompt when --cmd, no --save, TTY, new command", () => {
		const result = resolveRunCommand(testDir, "test", { cmd: "echo inline", force: true }, true);
		expect(result.type).toBe("executed");
		if (result.type === "executed") {
			expect(result.saveAction).toBe("prompt");
		}
	});

	it("returns executed with saveAction skip when --cmd, no --save, no TTY", () => {
		const result = resolveRunCommand(testDir, "test", { cmd: "echo inline", force: true }, false);
		expect(result.type).toBe("executed");
		if (result.type === "executed") {
			expect(result.saveAction).toBe("skip");
		}
	});

	it("propagates failure exit code from config command", () => {
		saveCommand(testDir, "test", "exit 42");
		const result = resolveRunCommand(testDir, "test", { force: true });
		expect(result.type).toBe("executed");
		if (result.type === "executed") {
			expect(result.result.status).toBe("fail");
			expect(result.result.exitCode).toBe(42);
		}
	});
});
