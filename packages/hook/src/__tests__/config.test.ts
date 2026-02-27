import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { getExec, getTask, getTriggerPatterns, loadConfig } from "../lib/config";

describe("config", () => {
	const testProjectDir = join(tmpdir(), "chunk-hook-test-config", String(Date.now()));
	const configDir = join(testProjectDir, ".chunk", "hook");
	const configFile = join(configDir, "config.yml");
	const saved: Record<string, string | undefined> = {};

	function setEnv(key: string, val: string | undefined) {
		saved[key] = process.env[key];
		if (val === undefined) delete process.env[key];
		else process.env[key] = val;
	}

	beforeEach(() => {
		mkdirSync(configDir, { recursive: true });
		setEnv("CLAUDE_PROJECT_DIR", testProjectDir);
		setEnv("CHUNK_HOOK_ENABLE", undefined);
		setEnv("CHUNK_HOOK_CONFIG", undefined);
		setEnv("CHUNK_HOOK_SENTINELS_DIR", undefined);
	});

	afterEach(() => {
		rmSync(testProjectDir, { recursive: true, force: true });
		for (const [k, v] of Object.entries(saved)) {
			if (v === undefined) delete process.env[k];
			else process.env[k] = v;
		}
	});

	it("returns defaults when no config file exists", () => {
		rmSync(configFile, { force: true });
		const config = loadConfig();
		expect(config.projectDir).toBe(testProjectDir);
		expect(Object.keys(config.execs)).toEqual([]);
		expect(Object.keys(config.tasks)).toEqual([]);
		// Built-in triggers are always present
		expect(config.triggers["pre-commit"]).toEqual(["git commit", "git push"]);
	});

	it("reads YAML config with execs, tasks, and triggers", () => {
		writeFileSync(
			configFile,
			`
triggers:
  pre-deploy:
    - "deploy"
    - "kubectl apply"

execs:
  tests:
    command: "go test ./..."
    fileExt: ".go"
    always: true
    timeout: 120
  lint:
    command: "golangci-lint run"
    timeout: 30

tasks:
  review:
    always: true
    limit: 5
    instructions: ".claude/review.md"
  security:
    limit: 2
`,
			"utf-8",
		);

		const config = loadConfig();

		// Execs
		const tests = getExec(config, "tests");
		expect(tests).toBeDefined();
		expect(tests?.command).toBe("go test ./...");
		expect(tests?.fileExt).toBe(".go");
		expect(tests?.always).toBe(true);
		expect(tests?.timeout).toBe(120);

		const lint = getExec(config, "lint");
		expect(lint).toBeDefined();
		expect(lint?.command).toBe("golangci-lint run");
		expect(lint?.timeout).toBe(30);
		expect(lint?.always).toBe(false);

		// Triggers (user-defined + built-in)
		expect(getTriggerPatterns(config, "pre-deploy")).toEqual(["deploy", "kubectl apply"]);
		expect(getTriggerPatterns(config, "pre-commit")).toEqual(["git commit", "git push"]);

		// Tasks
		const review = getTask(config, "review");
		expect(review).toBeDefined();
		expect(review?.always).toBe(true);
		expect(review?.limit).toBe(5);
		expect(review?.instructions).toBe(".claude/review.md");

		const security = getTask(config, "security");
		expect(security).toBeDefined();
		expect(security?.limit).toBe(2);
		expect(security?.always).toBe(false);
	});

	it("env timeout override takes precedence over YAML", () => {
		writeFileSync(
			configFile,
			`
execs:
  tests:
    command: "go test ./..."
    timeout: 120
`,
			"utf-8",
		);

		setEnv("CHUNK_HOOK_TIMEOUT_TESTS", "60");

		const config = loadConfig();
		const tests = getExec(config, "tests");
		expect(tests).toBeDefined();
		expect(tests?.timeout).toBe(60);
	});

	it("sentinel dir env override works", () => {
		setEnv("CHUNK_HOOK_SENTINELS_DIR", "/custom/sentinels");
		const config = loadConfig();
		expect(config.sentinelDir).toBe("/custom/sentinels");
	});

	it("uses custom config path from CHUNK_HOOK_CONFIG", () => {
		const customPath = join(testProjectDir, "custom-config.yml");
		writeFileSync(customPath, `execs:\n  tests:\n    command: "custom test cmd"\n`, "utf-8");
		setEnv("CHUNK_HOOK_CONFIG", customPath);
		const config = loadConfig();
		const tests = getExec(config, "tests");
		expect(tests).toBeDefined();
		expect(tests?.command).toBe("custom test cmd");
	});

	it("user triggers override built-in triggers with same name", () => {
		writeFileSync(
			configFile,
			`
triggers:
  pre-commit:
    - "git add"
    - "git commit"
`,
			"utf-8",
		);

		const config = loadConfig();
		expect(getTriggerPatterns(config, "pre-commit")).toEqual(["git add", "git commit"]);
	});

	it("getExec returns undefined for unknown exec", () => {
		const config = loadConfig();
		expect(getExec(config, "nonexistent")).toBeUndefined();
	});

	it("getTask returns undefined for unknown task", () => {
		const config = loadConfig();
		expect(getTask(config, "nonexistent")).toBeUndefined();
	});

	it("getTriggerPatterns returns undefined for unknown trigger", () => {
		const config = loadConfig();
		expect(getTriggerPatterns(config, "nonexistent")).toBeUndefined();
	});

	it("exec defaults are applied correctly", () => {
		writeFileSync(
			configFile,
			`
execs:
  minimal:
    command: "echo hello"
`,
			"utf-8",
		);

		const config = loadConfig();
		const exec = getExec(config, "minimal");
		expect(exec).toBeDefined();
		expect(exec?.command).toBe("echo hello");
		expect(exec?.fileExt).toBe("");
		expect(exec?.always).toBe(false);
		expect(exec?.timeout).toBe(300);
	});
});
