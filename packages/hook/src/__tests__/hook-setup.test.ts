import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { runHookSetup } from "../commands/hook-setup";
import { TEMPLATE_FILES } from "../lib/templates";

describe("hook-setup", () => {
	const testDir = join(tmpdir(), "chunk-hook-test-setup", String(Date.now()));
	// Use a temp dir for startup files so we don't touch the real shell profile
	const startupFile = join(testDir, ".zprofile");

	beforeEach(() => {
		mkdirSync(testDir, { recursive: true });
		writeFileSync(startupFile, "");
	});

	afterEach(() => {
		rmSync(testDir, { recursive: true, force: true });
	});

	it("creates all repo template files", () => {
		const result = runHookSetup({
			targetDir: testDir,
			profile: "enable",
			force: false,
			skipEnv: true,
		});

		expect(result.copyResults).toHaveLength(TEMPLATE_FILES.length);
		for (const r of result.copyResults) {
			expect(r.action).toBe("created");
			expect(existsSync(join(testDir, r.relativePath))).toBe(true);
		}
	});

	it("returns null envResult when skipEnv is true", () => {
		const result = runHookSetup({
			targetDir: testDir,
			profile: "enable",
			force: false,
			skipEnv: true,
		});

		expect(result.envResult).toBeNull();
	});

	it("returns envResult when skipEnv is false", () => {
		const result = runHookSetup({
			targetDir: testDir,
			profile: "enable",
			force: false,
			skipEnv: false,
			startupFiles: [startupFile],
		});

		expect(result.envResult).not.toBeNull();
		expect(result.envResult?.profile).toBe("enable");
		expect(result.envResult?.startupFiles).toContain(startupFile);
	});

	it("is idempotent — second run shows example/skipped, no errors", () => {
		const opts = {
			targetDir: testDir,
			profile: "enable" as const,
			force: false,
			skipEnv: true,
		};

		runHookSetup(opts);
		const second = runHookSetup(opts);

		// All files already exist, so second run produces "example" actions
		for (const r of second.copyResults) {
			expect(["example", "skipped"]).toContain(r.action);
		}
	});

	it("overwrites files when --force is used on re-run", () => {
		const opts = {
			targetDir: testDir,
			profile: "enable" as const,
			force: false,
			skipEnv: true,
		};

		runHookSetup(opts);

		const second = runHookSetup({ ...opts, force: true });
		for (const r of second.copyResults) {
			expect(r.action).toBe("created");
		}
	});

	it("creates .chunk/hook/ and .claude/ directories", () => {
		runHookSetup({
			targetDir: testDir,
			profile: "enable",
			force: false,
			skipEnv: true,
		});

		expect(existsSync(join(testDir, ".chunk", "hook", "config.yml"))).toBe(true);
		expect(existsSync(join(testDir, ".chunk", "hook", ".gitignore"))).toBe(true);
		expect(existsSync(join(testDir, ".claude", "settings.json"))).toBe(true);
	});
});
