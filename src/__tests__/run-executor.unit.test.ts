import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { execSync } from "node:child_process";
import * as fs from "node:fs";
import * as os from "node:os";
import * as path from "node:path";
import { checkCache, executeCommand } from "../core/run-executor";

describe("core/run-executor", () => {
	let testDir: string;

	beforeEach(() => {
		testDir = fs.mkdtempSync(path.join(os.tmpdir(), "chunk-run-test-"));
		execSync("git init", { cwd: testDir, stdio: "ignore" });
		execSync("git commit --allow-empty -m 'init'", { cwd: testDir, stdio: "ignore" });
	});

	afterEach(() => {
		fs.rmSync(testDir, { recursive: true, force: true });
	});

	describe("executeCommand", () => {
		it("returns pass for successful command", () => {
			const result = executeCommand(testDir, "test", "echo hello", { force: true });
			expect(result.status).toBe("pass");
			expect(result.exitCode).toBe(0);
			expect(result.output).toContain("hello");
		});

		it("returns fail for failing command", () => {
			const result = executeCommand(testDir, "test", "exit 1", { force: true });
			expect(result.status).toBe("fail");
			expect(result.exitCode).toBe(1);
		});

		it("caches results on second run", () => {
			executeCommand(testDir, "test", "echo first", { force: true });
			const result = executeCommand(testDir, "test", "echo second");
			expect(result.status).toBe("cached");
			expect(result.output).toContain("first");
		});

		it("invalidates cache after git changes", () => {
			// Create and commit a tracked file
			fs.writeFileSync(path.join(testDir, "file.txt"), "original");
			execSync("git add file.txt && git commit -m 'add file'", { cwd: testDir, stdio: "ignore" });

			executeCommand(testDir, "test", "echo cached", { force: true });

			// Modify the tracked file so git diff changes
			fs.writeFileSync(path.join(testDir, "file.txt"), "modified");

			const result = executeCommand(testDir, "test", "echo fresh");
			expect(result.status).toBe("pass");
			expect(result.output).toContain("fresh");
		});

		it("ignores cache when force is true", () => {
			executeCommand(testDir, "test", "echo first", { force: true });
			const result = executeCommand(testDir, "test", "echo second", { force: true });
			expect(result.status).toBe("pass");
			expect(result.output).toContain("second");
		});
	});

	describe("checkCache", () => {
		it("returns undefined when no cache", () => {
			expect(checkCache(testDir, "test")).toBeUndefined();
		});

		it("returns cached result when valid", () => {
			executeCommand(testDir, "test", "echo ok", { force: true });
			const cached = checkCache(testDir, "test");
			expect(cached).toBeDefined();
			expect(cached?.status).toBe("cached");
			expect(cached?.output).toContain("ok");
		});

		it("returns undefined when git state changed", () => {
			// Create and commit a tracked file
			fs.writeFileSync(path.join(testDir, "file.txt"), "original");
			execSync("git add file.txt && git commit -m 'add file'", { cwd: testDir, stdio: "ignore" });

			executeCommand(testDir, "test", "echo ok", { force: true });

			// Modify the tracked file
			fs.writeFileSync(path.join(testDir, "file.txt"), "modified");
			expect(checkCache(testDir, "test")).toBeUndefined();
		});
	});

	describe("fileExt scoping", () => {
		it("cache remains valid when unrelated file changes", () => {
			// Commit a .ts and a .md file
			fs.writeFileSync(path.join(testDir, "app.ts"), "const x = 1;");
			fs.writeFileSync(path.join(testDir, "readme.md"), "hello");
			execSync("git add -A && git commit -m 'add files'", { cwd: testDir, stdio: "ignore" });

			// Run scoped to .ts
			executeCommand(testDir, "test", "echo ts-scoped", { force: true, fileExt: ".ts" });

			// Change only the .md file
			fs.writeFileSync(path.join(testDir, "readme.md"), "updated");

			// Cache should still be valid since .ts files didn't change
			const cached = checkCache(testDir, "test", ".ts");
			expect(cached).toBeDefined();
			expect(cached?.status).toBe("cached");
		});

		it("cache invalidates when scoped file changes", () => {
			fs.writeFileSync(path.join(testDir, "app.ts"), "const x = 1;");
			execSync("git add -A && git commit -m 'add ts'", { cwd: testDir, stdio: "ignore" });

			executeCommand(testDir, "test", "echo ts-scoped", { force: true, fileExt: ".ts" });

			// Change the .ts file
			fs.writeFileSync(path.join(testDir, "app.ts"), "const x = 2;");

			const cached = checkCache(testDir, "test", ".ts");
			expect(cached).toBeUndefined();
		});
	});
});
