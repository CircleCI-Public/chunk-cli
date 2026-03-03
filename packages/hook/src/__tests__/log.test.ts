import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, readdirSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { initLog, log } from "../lib/log";

describe("log", () => {
	const testDir = join(tmpdir(), "chunk-hook-test-logs", String(Date.now()));
	const saved: Record<string, string | undefined> = {};

	function setEnv(key: string, val: string | undefined) {
		saved[key] = process.env[key];
		if (val === undefined) delete process.env[key];
		else process.env[key] = val;
	}

	beforeEach(() => {
		mkdirSync(testDir, { recursive: true });
		setEnv("CLAUDE_PROJECT_DIR", "/fake/project");
		setEnv("CHUNK_HOOK_VERBOSE", undefined);
	});

	afterEach(() => {
		rmSync(testDir, { recursive: true, force: true });
		for (const [k, v] of Object.entries(saved)) {
			if (v === undefined) delete process.env[k];
			else process.env[k] = v;
		}
	});

	it("initLog creates a hash-based subdirectory", () => {
		initLog({ baseDir: testDir });
		const dirs = readdirSync(testDir);
		expect(dirs.length).toBe(1);
		// Subdirectory name is a 16-char hex hash
		expect(dirs[0]).toMatch(/^[0-9a-f]{16}$/);
	});

	it("log writes a timestamped entry to the log file", () => {
		initLog({ baseDir: testDir });
		log("test-tag", "hello world");

		const dirs = readdirSync(testDir);
		expect(dirs.length).toBe(1);
		const firstDir = dirs[0] as string;

		const logFile = join(testDir, firstDir, "chunk-hook.log");
		expect(existsSync(logFile)).toBe(true);

		const content = readFileSync(logFile, "utf-8");
		expect(content).toContain("[test-tag]");
		expect(content).toContain("hello world");
	});

	it("log appends multiple entries", () => {
		initLog({ baseDir: testDir });
		log("tag1", "first");
		log("tag2", "second");

		const dirs = readdirSync(testDir);
		const logFile = join(testDir, dirs[0] as string, "chunk-hook.log");
		const content = readFileSync(logFile, "utf-8");

		const lines = content.trim().split("\n");
		expect(lines.length).toBe(2);
		expect(lines[0]).toContain("[tag1] first");
		expect(lines[1]).toContain("[tag2] second");
	});
});
