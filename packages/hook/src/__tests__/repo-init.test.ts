import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { runRepoInit } from "../commands/repo-init";
import { TEMPLATE_FILES } from "../lib/templates";

describe("repo-init", () => {
	const testDir = join(tmpdir(), "chunk-hook-test-repo-init", String(Date.now()));

	beforeEach(() => {
		mkdirSync(testDir, { recursive: true });
	});

	afterEach(() => {
		rmSync(testDir, { recursive: true, force: true });
	});

	it("creates all template files in an empty directory", () => {
		const results = runRepoInit({ targetDir: testDir, force: false });

		expect(results).toHaveLength(TEMPLATE_FILES.length);
		for (const r of results) {
			expect(r.action).toBe("created");
			expect(existsSync(join(testDir, r.relativePath))).toBe(true);
		}
	});

	it("creates .chunk/hook/ directory structure", () => {
		runRepoInit({ targetDir: testDir, force: false });

		expect(existsSync(join(testDir, ".chunk", "hook", "config.yml"))).toBe(true);
		expect(existsSync(join(testDir, ".chunk", "hook", ".gitignore"))).toBe(true);
		expect(existsSync(join(testDir, ".chunk", "hook", "code-review-instructions.md"))).toBe(true);
		expect(existsSync(join(testDir, ".chunk", "hook", "code-review-schema.json"))).toBe(true);
		expect(existsSync(join(testDir, ".claude", "settings.json"))).toBe(true);
	});

	it("substitutes __PROJECT__ with the directory basename", () => {
		const subDir = join(testDir, "my-awesome-project");
		mkdirSync(subDir, { recursive: true });

		runRepoInit({ targetDir: subDir, force: false });

		const settings = readFileSync(join(subDir, ".claude", "settings.json"), "utf-8");
		expect(settings).toContain("my-awesome-project");
		expect(settings).not.toContain("__PROJECT__");
	});

	it("does not substitute __PROJECT__ in non-settings files", () => {
		const subDir = join(testDir, "my-project");
		mkdirSync(subDir, { recursive: true });

		runRepoInit({ targetDir: subDir, force: false });

		const config = readFileSync(join(subDir, ".chunk", "hook", "config.yml"), "utf-8");
		expect(config).not.toContain("my-project");
	});

	it("creates .example files when destination already exists", () => {
		// Pre-create a config file
		const configDir = join(testDir, ".chunk", "hook");
		mkdirSync(configDir, { recursive: true });
		writeFileSync(join(configDir, "config.yml"), "existing: true\n");

		const results = runRepoInit({ targetDir: testDir, force: false });

		const configResult = results.find((r) => r.relativePath === ".chunk/hook/config.yml");
		expect(configResult).toBeDefined();
		expect(configResult?.action).toBe("example");

		// Original file should be unchanged
		const original = readFileSync(join(configDir, "config.yml"), "utf-8");
		expect(original).toBe("existing: true\n");

		// Example file should exist
		expect(existsSync(join(configDir, "config.example.yml"))).toBe(true);
	});

	it("creates .example for files without extension", () => {
		// Pre-create .gitignore
		const hookDir = join(testDir, ".chunk", "hook");
		mkdirSync(hookDir, { recursive: true });
		writeFileSync(join(hookDir, ".gitignore"), "existing\n");

		const results = runRepoInit({ targetDir: testDir, force: false });

		const gitignoreResult = results.find((r) => r.relativePath === ".chunk/hook/.gitignore");
		expect(gitignoreResult).toBeDefined();
		expect(gitignoreResult?.action).toBe("example");
	});

	it("overwrites existing files when --force is used", () => {
		// Pre-create config
		const configDir = join(testDir, ".chunk", "hook");
		mkdirSync(configDir, { recursive: true });
		writeFileSync(join(configDir, "config.yml"), "old: true\n");

		const results = runRepoInit({ targetDir: testDir, force: true });

		const configResult = results.find((r) => r.relativePath === ".chunk/hook/config.yml");
		expect(configResult?.action).toBe("created");

		// File should be overwritten with template content
		const content = readFileSync(join(configDir, "config.yml"), "utf-8");
		expect(content).toContain("CHUNK_HOOK_ENABLE");
		expect(content).not.toContain("old: true");
	});

	it("handles mixed existing and new files", () => {
		// Pre-create only settings.json
		const claudeDir = join(testDir, ".claude");
		mkdirSync(claudeDir, { recursive: true });
		writeFileSync(join(claudeDir, "settings.json"), '{"existing": true}');

		const results = runRepoInit({ targetDir: testDir, force: false });

		const settingsResult = results.find((r) => r.relativePath === ".claude/settings.json");
		expect(settingsResult?.action).toBe("example");

		// Other files should be created normally
		const configResult = results.find((r) => r.relativePath === ".chunk/hook/config.yml");
		expect(configResult?.action).toBe("created");
	});

	it("settings.json contains chunk hook commands", () => {
		runRepoInit({ targetDir: testDir, force: false });

		const settings = readFileSync(join(testDir, ".claude", "settings.json"), "utf-8");
		expect(settings).toContain("chunk hook exec");
		expect(settings).toContain("chunk hook sync");
		expect(settings).toContain("chunk hook scope");
		expect(settings).toContain("chunk hook state");
		expect(settings).toContain("Bash(chunk:*)");
	});

	it("config.yml references .chunk/hook/ paths", () => {
		runRepoInit({ targetDir: testDir, force: false });

		const config = readFileSync(join(testDir, ".chunk", "hook", "config.yml"), "utf-8");
		expect(config).toContain(".chunk/hook/code-review-instructions.md");
		expect(config).toContain(".chunk/hook/code-review-schema.json");
		expect(config).toContain("CHUNK_HOOK_ENABLE");
	});

	it("gitignore contains .chunk-hook-active", () => {
		runRepoInit({ targetDir: testDir, force: false });

		const gitignore = readFileSync(join(testDir, ".chunk", "hook", ".gitignore"), "utf-8");
		expect(gitignore).toContain(".chunk-hook-active");
	});

	it("creates .example.json for settings.json when it already exists", () => {
		const claudeDir = join(testDir, ".claude");
		mkdirSync(claudeDir, { recursive: true });
		writeFileSync(join(claudeDir, "settings.json"), '{"existing": true}');

		runRepoInit({ targetDir: testDir, force: false });

		expect(existsSync(join(claudeDir, "settings.example.json"))).toBe(true);
		const example = readFileSync(join(claudeDir, "settings.example.json"), "utf-8");
		expect(example).toContain("chunk hook");
	});
});
