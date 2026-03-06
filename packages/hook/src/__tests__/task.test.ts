import { afterEach, beforeEach, describe, expect, it, spyOn } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { runTask } from "../commands/task";
import type { AgentEvent, HookAdapter } from "../lib/adapter";
import type { ResolvedConfig } from "../lib/config";
import type { SentinelData } from "../lib/sentinel";
import { sentinelPath, writeSentinel } from "../lib/sentinel";

// ---------------------------------------------------------------------------
// Test helpers (mirrors exec.test.ts conventions)
// ---------------------------------------------------------------------------

function makeTestAdapter(overrides: Partial<HookAdapter> = {}): HookAdapter {
	return {
		readEvent: async () => ({ eventName: "", raw: {} }),
		allow: () => {
			process.exit(0);
		},
		block: (reason: string) => {
			process.stderr.write(`${reason}\n`);
			process.exit(2);
		},
		getProjectDir: () => "/test/project",
		isStopRecursion: () => false,
		isShellToolCall: () => false,
		getShellCommand: () => undefined,
		stateKey: (e: AgentEvent) => e.eventName,
		commandSummary: () => "",
		...overrides,
	};
}

function makeEvent(partial: Partial<AgentEvent> = {}): AgentEvent {
	return { eventName: "", raw: {}, ...partial };
}

function makeConfig(sentinelDir: string, projectDir = "/test/project"): ResolvedConfig {
	return {
		triggers: {},
		execs: {},
		tasks: {
			review: {
				instructions: "",
				schema: "",
				limit: 3,
				always: true,
				timeout: 600,
			},
		},
		sentinelDir,
		projectDir,
	};
}

// ---------------------------------------------------------------------------
// runTask() check subcommand — session-aware staleness
// ---------------------------------------------------------------------------

describe("runTask() session-aware staleness", () => {
	let exitSpy: ReturnType<typeof spyOn>;
	let stderrSpy: ReturnType<typeof spyOn>;
	let tmpDir: string;
	let savedVerbose: string | undefined;
	const ExitError = class extends Error {};

	const blockMessage = () => {
		const calls = stderrSpy.mock.calls;
		return calls[calls.length - 1][0] as string;
	};

	beforeEach(() => {
		tmpDir = mkdtempSync(join(tmpdir(), "chunk-hook-task-test-"));
		exitSpy = spyOn(process, "exit").mockImplementation(() => {
			throw new ExitError("process.exit called");
		});
		stderrSpy = spyOn(process.stderr, "write").mockImplementation(() => true);
		process.env.CHUNK_HOOK_CONSUME_DELAY_MS = "0";
		savedVerbose = process.env.CHUNK_HOOK_VERBOSE;
		delete process.env.CHUNK_HOOK_VERBOSE;
	});

	afterEach(() => {
		exitSpy.mockRestore();
		stderrSpy.mockRestore();
		rmSync(tmpDir, { recursive: true, force: true });
		delete process.env.CHUNK_HOOK_CONSUME_DELAY_MS;
		if (savedVerbose !== undefined) process.env.CHUNK_HOOK_VERBOSE = savedVerbose;
	});

	it("blocks when task sentinel has a different sessionId (stale session)", async () => {
		const projectDir = "/test/project";
		const config = makeConfig(tmpDir, projectDir);

		// Write a passing task result with session-A
		const path = sentinelPath(tmpDir, projectDir, "review");
		writeFileSync(path, JSON.stringify({ decision: "allow" }), "utf-8");

		// Activate scope marker with session-B
		const hookDir = join(projectDir, ".chunk", "hook");
		// runTask reads the marker from config.projectDir — we can't write there
		// because /test/project doesn't exist. Instead, use a real tmpDir as projectDir.
		const realProjectDir = mkdtempSync(join(tmpdir(), "chunk-hook-task-proj-"));
		const realHookDir = join(realProjectDir, ".chunk", "hook");
		const { mkdirSync } = await import("node:fs");
		mkdirSync(realHookDir, { recursive: true });
		// Write marker with session-B
		writeFileSync(
			join(realHookDir, ".chunk-hook-active"),
			JSON.stringify({ sessionId: "session-B", timestamp: Date.now() }),
		);

		const configWithRealProject = makeConfig(tmpDir, realProjectDir);

		// Write task result via the real projectDir sentinel path
		const realPath = sentinelPath(tmpDir, realProjectDir, "review");
		writeFileSync(realPath, JSON.stringify({ decision: "allow" }), "utf-8");

		const flags = { subcommand: "check" as const, name: "review", always: true };
		// The sentinel was written without a sessionId, but readTaskResult will inject
		// "session-B" from the marker. The sentinel's sessionId won't match because
		// readTaskResult only injects the marker's sessionId into TaskResult conversions.
		// Actually: readTaskResult injects the sessionId passed to it (which comes from the marker).
		// The resulting sentinel will have sessionId: "session-B" injected.
		// evaluateSentinel(sentinel, "session-B") → sessionId matches → pass.
		//
		// The ACTUAL stale-session scenario is: sentinel was written by session-A's agent
		// (with sessionId embedded in the sentinel file), then session-B starts.
		// Let's test that with a proper SentinelData sentinel.
		writeSentinel(tmpDir, realProjectDir, "review", {
			status: "pass",
			startedAt: new Date().toISOString(),
			finishedAt: new Date().toISOString(),
			project: realProjectDir,
			sessionId: "session-A", // <-- old session
		});

		await expect(
			runTask(configWithRealProject, makeTestAdapter(), makeEvent(), flags),
		).rejects.toThrow(ExitError);
		// Should block because sentinel's sessionId (A) doesn't match marker's (B)
		expect(exitSpy).toHaveBeenCalledWith(2);

		rmSync(realProjectDir, { recursive: true, force: true });
	});

	it("allows when task sentinel has matching sessionId", async () => {
		const realProjectDir = mkdtempSync(join(tmpdir(), "chunk-hook-task-proj-"));
		const realHookDir = join(realProjectDir, ".chunk", "hook");
		const { mkdirSync } = await import("node:fs");
		mkdirSync(realHookDir, { recursive: true });
		// Write marker with session-A
		writeFileSync(
			join(realHookDir, ".chunk-hook-active"),
			JSON.stringify({ sessionId: "session-A", timestamp: Date.now() }),
		);

		const config = makeConfig(tmpDir, realProjectDir);

		// Write sentinel with matching session-A
		writeSentinel(tmpDir, realProjectDir, "review", {
			status: "pass",
			startedAt: new Date().toISOString(),
			finishedAt: new Date().toISOString(),
			project: realProjectDir,
			sessionId: "session-A",
		});

		const flags = { subcommand: "check" as const, name: "review", always: true };
		await expect(runTask(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(0);

		rmSync(realProjectDir, { recursive: true, force: true });
	});

	it("blocks when no sentinel exists", async () => {
		const realProjectDir = mkdtempSync(join(tmpdir(), "chunk-hook-task-proj-"));
		const realHookDir = join(realProjectDir, ".chunk", "hook");
		const { mkdirSync } = await import("node:fs");
		mkdirSync(realHookDir, { recursive: true });
		writeFileSync(
			join(realHookDir, ".chunk-hook-active"),
			JSON.stringify({ sessionId: "session-A", timestamp: Date.now() }),
		);

		const config = makeConfig(tmpDir, realProjectDir);
		const flags = { subcommand: "check" as const, name: "review", always: true };
		await expect(runTask(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(2);

		rmSync(realProjectDir, { recursive: true, force: true });
	});

	it("blocks when task sentinel has fail status", async () => {
		const realProjectDir = mkdtempSync(join(tmpdir(), "chunk-hook-task-proj-"));
		const realHookDir = join(realProjectDir, ".chunk", "hook");
		const { mkdirSync } = await import("node:fs");
		mkdirSync(realHookDir, { recursive: true });
		writeFileSync(
			join(realHookDir, ".chunk-hook-active"),
			JSON.stringify({ sessionId: "session-A", timestamp: Date.now() }),
		);

		const config = makeConfig(tmpDir, realProjectDir);

		// Write failing sentinel with matching session
		writeSentinel(tmpDir, realProjectDir, "review", {
			status: "fail",
			startedAt: new Date().toISOString(),
			finishedAt: new Date().toISOString(),
			project: realProjectDir,
			sessionId: "session-A",
			details: "SQL injection found",
		});

		const flags = { subcommand: "check" as const, name: "review", always: true };
		await expect(runTask(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(2);
		expect(blockMessage()).toContain("SQL injection found");

		rmSync(realProjectDir, { recursive: true, force: true });
	});
});

// ---------------------------------------------------------------------------
// runTask() — task result forgery prevention
// ---------------------------------------------------------------------------

describe("runTask() task result forgery prevention", () => {
	let exitSpy: ReturnType<typeof spyOn>;
	let stderrSpy: ReturnType<typeof spyOn>;
	let tmpDir: string;
	let savedVerbose: string | undefined;
	const ExitError = class extends Error {};

	beforeEach(() => {
		tmpDir = mkdtempSync(join(tmpdir(), "chunk-hook-task-forgery-"));
		exitSpy = spyOn(process, "exit").mockImplementation(() => {
			throw new ExitError("process.exit called");
		});
		stderrSpy = spyOn(process.stderr, "write").mockImplementation(() => true);
		process.env.CHUNK_HOOK_CONSUME_DELAY_MS = "0";
		savedVerbose = process.env.CHUNK_HOOK_VERBOSE;
		delete process.env.CHUNK_HOOK_VERBOSE;
	});

	afterEach(() => {
		exitSpy.mockRestore();
		stderrSpy.mockRestore();
		rmSync(tmpDir, { recursive: true, force: true });
		delete process.env.CHUNK_HOOK_CONSUME_DELAY_MS;
		if (savedVerbose !== undefined) process.env.CHUNK_HOOK_VERBOSE = savedVerbose;
	});

	it("blocks when task result file contains invalid JSON", async () => {
		const realProjectDir = mkdtempSync(join(tmpdir(), "chunk-hook-task-proj-"));
		const realHookDir = join(realProjectDir, ".chunk", "hook");
		const { mkdirSync } = await import("node:fs");
		mkdirSync(realHookDir, { recursive: true });
		writeFileSync(
			join(realHookDir, ".chunk-hook-active"),
			JSON.stringify({ sessionId: "session-A", timestamp: Date.now() }),
		);

		const config = makeConfig(tmpDir, realProjectDir);

		// Write malformed JSON to sentinel path
		const path = sentinelPath(tmpDir, realProjectDir, "review");
		writeFileSync(path, "not valid json at all {{{{", "utf-8");

		const flags = { subcommand: "check" as const, name: "review", always: true };
		await expect(runTask(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		// readTaskResult returns undefined for invalid JSON → evaluateSentinel → "missing" → block
		expect(exitSpy).toHaveBeenCalledWith(2);

		rmSync(realProjectDir, { recursive: true, force: true });
	});

	it("blocks when task result has invalid decision value", async () => {
		const realProjectDir = mkdtempSync(join(tmpdir(), "chunk-hook-task-proj-"));
		const realHookDir = join(realProjectDir, ".chunk", "hook");
		const { mkdirSync } = await import("node:fs");
		mkdirSync(realHookDir, { recursive: true });
		writeFileSync(
			join(realHookDir, ".chunk-hook-active"),
			JSON.stringify({ sessionId: "session-A", timestamp: Date.now() }),
		);

		const config = makeConfig(tmpDir, realProjectDir);

		// Write JSON with wrong decision value — agent trying to forge a pass
		const path = sentinelPath(tmpDir, realProjectDir, "review");
		writeFileSync(path, JSON.stringify({ decision: "pass", reason: "forged" }), "utf-8");

		const flags = { subcommand: "check" as const, name: "review", always: true };
		await expect(runTask(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		// "pass" is not a valid decision (must be "allow" or "block") → undefined → "missing" → block
		expect(exitSpy).toHaveBeenCalledWith(2);

		rmSync(realProjectDir, { recursive: true, force: true });
	});

	it("blocks when task result is valid JSON but missing decision field", async () => {
		const realProjectDir = mkdtempSync(join(tmpdir(), "chunk-hook-task-proj-"));
		const realHookDir = join(realProjectDir, ".chunk", "hook");
		const { mkdirSync } = await import("node:fs");
		mkdirSync(realHookDir, { recursive: true });
		writeFileSync(
			join(realHookDir, ".chunk-hook-active"),
			JSON.stringify({ sessionId: "session-A", timestamp: Date.now() }),
		);

		const config = makeConfig(tmpDir, realProjectDir);

		// Write JSON without decision field
		const path = sentinelPath(tmpDir, realProjectDir, "review");
		writeFileSync(path, JSON.stringify({ status: "pass", reason: "looks good" }), "utf-8");

		const flags = { subcommand: "check" as const, name: "review", always: true };
		await expect(runTask(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(2);

		rmSync(realProjectDir, { recursive: true, force: true });
	});
});
