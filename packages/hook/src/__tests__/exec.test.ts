import { afterEach, beforeEach, describe, expect, it, spyOn } from "bun:test";
import { execSync } from "node:child_process";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { buildRunnerCommand, formatFailureReason, runExec } from "../commands/exec";
import type { AgentEvent, HookAdapter } from "../lib/adapter";
import type { ResolvedConfig } from "../lib/config";
import type { SentinelData } from "../lib/sentinel";
import { writeSentinel } from "../lib/sentinel";

// ---------------------------------------------------------------------------
// Test helpers (mirrors check.test.ts conventions)
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

function makeConfig(sentinelDir: string): ResolvedConfig {
	return {
		triggers: {},
		execs: {},
		tasks: {},
		hooks: {},
		sentinelDir,
		projectDir: "/test/project",
	};
}

// ---------------------------------------------------------------------------
// buildRunnerCommand()
// ---------------------------------------------------------------------------

describe("buildRunnerCommand()", () => {
	it("builds minimal command with just name", () => {
		const flags = { subcommand: "check" as const, name: "tests" };
		expect(buildRunnerCommand(flags)).toBe("chunk hook exec run tests --no-check");
	});

	it("includes --cmd with single-quoted value", () => {
		const flags = { subcommand: "check" as const, name: "lint", cmd: "bun run lint" };
		expect(buildRunnerCommand(flags)).toBe(
			"chunk hook exec run lint --no-check --cmd 'bun run lint'",
		);
	});

	it("escapes single quotes in --cmd value", () => {
		const flags = { subcommand: "check" as const, name: "test", cmd: "echo 'hello'" };
		const result = buildRunnerCommand(flags);
		// shellQuote turns ' into '\''
		expect(result).toContain(`--cmd 'echo '\\''hello'\\'''`);
	});

	it("escapes single quotes in --file-ext value", () => {
		const flags = { subcommand: "check" as const, name: "test", fileExt: "ts'tsx" };
		const result = buildRunnerCommand(flags);
		expect(result).toContain(`--file-ext 'ts'\\''tsx'`);
	});

	it("includes --timeout flag", () => {
		const flags = { subcommand: "check" as const, name: "tests", timeout: 60 };
		expect(buildRunnerCommand(flags)).toContain("--timeout 60");
	});

	it("includes --staged flag", () => {
		const flags = { subcommand: "check" as const, name: "tests", staged: true };
		expect(buildRunnerCommand(flags)).toContain("--staged");
	});

	it("includes --always flag", () => {
		const flags = { subcommand: "check" as const, name: "tests", always: true };
		expect(buildRunnerCommand(flags)).toContain("--always");
	});

	it("omits optional flags when not set", () => {
		const flags = { subcommand: "check" as const, name: "tests" };
		const result = buildRunnerCommand(flags);
		expect(result).not.toContain("--cmd");
		expect(result).not.toContain("--timeout");
		expect(result).not.toContain("--file-ext");
		expect(result).not.toContain("--staged");
		expect(result).not.toContain("--always");
	});
});

// ---------------------------------------------------------------------------
// formatFailureReason()
// ---------------------------------------------------------------------------

describe("formatFailureReason()", () => {
	it("formats a normal failure", () => {
		const reason = formatFailureReason("tests", "bun test", 1, "FAIL src/lib/foo.test.ts");
		expect(reason).toContain('Exec "tests" failed (exit 1, command: bun test)');
		expect(reason).toContain("FAIL src/lib/foo.test.ts");
		expect(reason).toContain("Fix the issues and retry");
	});

	it("formats a timeout failure using exit code 124", () => {
		const reason = formatFailureReason("tests", "bun test", 124, "");
		expect(reason).toContain("timed out");
		expect(reason).not.toContain("failed");
	});

	it("includes command in failure message", () => {
		const reason = formatFailureReason("lint", "bun run lint", 2, "error: lint failed");
		expect(reason).toContain("bun run lint");
	});
});

// ---------------------------------------------------------------------------
// runExec() — check subcommand
// ---------------------------------------------------------------------------

describe("runExec() check subcommand", () => {
	let exitSpy: ReturnType<typeof spyOn>;
	let stderrSpy: ReturnType<typeof spyOn>;
	let tmpDir: string;
	let savedVerbose: string | undefined;
	const ExitError = class extends Error {};

	/** Return the block message (last stderr write before process.exit). */
	const blockMessage = () => {
		const calls = stderrSpy.mock.calls;
		return calls[calls.length - 1][0] as string;
	};

	beforeEach(() => {
		tmpDir = mkdtempSync(join(tmpdir(), "chunk-hook-exec-test-"));
		exitSpy = spyOn(process, "exit").mockImplementation(() => {
			throw new ExitError("process.exit called");
		});
		stderrSpy = spyOn(process.stderr, "write").mockImplementation(() => true);
		process.env.CHUNK_HOOK_CONSUME_DELAY_MS = "0";
		// Suppress log-to-stderr so only adapter.block() output lands in the spy.
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

	it("allows (exit 0) when sentinel shows pass", async () => {
		const config = makeConfig(tmpDir);
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: new Date().toISOString(),
			finishedAt: new Date().toISOString(),
			exitCode: 0,
		};
		writeSentinel(tmpDir, "/test/project", "myexec", sentinel);

		const flags = { subcommand: "check" as const, name: "myexec", always: true };
		await expect(runExec(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(0);
	});

	it("blocks (exit 2) when sentinel shows fail", async () => {
		const config = makeConfig(tmpDir);
		const sentinel: SentinelData = {
			status: "fail",
			startedAt: new Date().toISOString(),
			finishedAt: new Date().toISOString(),
			exitCode: 1,
			output: "3 tests failed",
			command: "bun test",
		};
		writeSentinel(tmpDir, "/test/project", "myexec", sentinel);

		const flags = { subcommand: "check" as const, name: "myexec", always: true, cmd: "bun test" };
		await expect(runExec(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(2);
		expect(blockMessage()).toContain("3 tests failed");
	});

	it("blocks with 'no results' message when no sentinel exists", async () => {
		const config = makeConfig(tmpDir);
		const flags = { subcommand: "check" as const, name: "myexec", always: true };
		await expect(runExec(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(2);
		const message = blockMessage();
		expect(message).toContain("has no results");
		expect(message).toContain("Run it first");
	});

	it("blocks with 'still running' message when sentinel is pending", async () => {
		const config = makeConfig(tmpDir);
		const sentinel: SentinelData = {
			status: "pending",
			startedAt: new Date().toISOString(),
		};
		writeSentinel(tmpDir, "/test/project", "myexec", sentinel);

		const flags = { subcommand: "check" as const, name: "myexec", always: true };
		await expect(runExec(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(2);
		expect(blockMessage()).toContain("still running");
	});

	it("runner command in 'no results' block message uses shell-quoted --cmd", async () => {
		const config = makeConfig(tmpDir);
		const flags = {
			subcommand: "check" as const,
			name: "myexec",
			always: true,
			cmd: "echo 'world'",
		};
		await expect(runExec(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		const message = blockMessage();
		// The suggested re-run command should have properly escaped single quotes
		expect(message).toContain(`--cmd 'echo '\\''world'\\'''`);
	});
});

// ---------------------------------------------------------------------------
// Skipped sentinel acceptance at push time
// ---------------------------------------------------------------------------

describe("runExec() skipped sentinel at push time", () => {
	let exitSpy: ReturnType<typeof spyOn>;
	let stderrSpy: ReturnType<typeof spyOn>;
	let tmpDir: string;
	let savedVerbose: string | undefined;
	const ExitError = class extends Error {};

	/** Adapter that reports the event as a `git push` shell command. */
	function makePushAdapter(overrides: Partial<HookAdapter> = {}): HookAdapter {
		return makeTestAdapter({
			isShellToolCall: () => true,
			getShellCommand: () => "git push origin main",
			...overrides,
		});
	}

	beforeEach(() => {
		tmpDir = mkdtempSync(join(tmpdir(), "chunk-hook-exec-push-test-"));
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

	it("allows push via skip-no-changes when repo has no modifications", async () => {
		// Initialize a real git repo so detectChanges can run and return false.
		execSync(
			'git init && git config user.email "test@example.com" && git config user.name "test user" && git commit --allow-empty -m init',
			{ cwd: tmpDir, stdio: "ignore" },
		);
		const config: ResolvedConfig = {
			triggers: {},
			execs: {},
			tasks: {},
			hooks: {},
			sentinelDir: tmpDir,
			projectDir: tmpDir,
		};

		const flags = { subcommand: "check" as const, name: "tests" };
		await expect(runExec(config, makePushAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(0);
	});

	it("allows push when sentinel shows pass", async () => {
		const config = makeConfig(tmpDir);
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: new Date().toISOString(),
			finishedAt: new Date().toISOString(),
			exitCode: 0,
		};
		writeSentinel(tmpDir, "/test/project", "tests", sentinel);

		const flags = { subcommand: "check" as const, name: "tests", always: true };
		await expect(runExec(config, makePushAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(0);
	});

	it("blocks push when sentinel shows fail", async () => {
		const config = makeConfig(tmpDir);
		const sentinel: SentinelData = {
			status: "fail",
			startedAt: new Date().toISOString(),
			finishedAt: new Date().toISOString(),
			exitCode: 1,
			output: "tests failed",
			command: "bun test",
		};
		writeSentinel(tmpDir, "/test/project", "tests", sentinel);

		const flags = { subcommand: "check" as const, name: "tests", cmd: "bun test", always: true };
		await expect(runExec(config, makePushAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(2);
		expect(stderrSpy.mock.calls[stderrSpy.mock.calls.length - 1][0]).toContain("tests failed");
	});
});

// ---------------------------------------------------------------------------
// Command validation (--cmd bypass prevention)
// ---------------------------------------------------------------------------

describe("runExec() command validation", () => {
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
		tmpDir = mkdtempSync(join(tmpdir(), "chunk-hook-exec-cmd-test-"));
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

	it("blocks when sentinel configuredCommand does not match --cmd flag", async () => {
		const config = makeConfig(tmpDir);
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: new Date().toISOString(),
			finishedAt: new Date().toISOString(),
			exitCode: 0,
			command: "true",
			configuredCommand: "bun test",
		};
		writeSentinel(tmpDir, "/test/project", "myexec", sentinel);

		// Check with a different command — should be treated as missing
		const flags = { subcommand: "check" as const, name: "myexec", always: true, cmd: "true" };
		await expect(runExec(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(2);
		expect(blockMessage()).toContain("has no results");
	});

	it("allows when sentinel configuredCommand matches --cmd flag", async () => {
		const config = makeConfig(tmpDir);
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: new Date().toISOString(),
			finishedAt: new Date().toISOString(),
			exitCode: 0,
			command: "bun test",
			configuredCommand: "bun test",
		};
		writeSentinel(tmpDir, "/test/project", "myexec", sentinel);

		const flags = { subcommand: "check" as const, name: "myexec", always: true, cmd: "bun test" };
		await expect(runExec(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(0);
	});

	it("falls back to sentinel.command when configuredCommand is absent", async () => {
		const config = makeConfig(tmpDir);
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: new Date().toISOString(),
			finishedAt: new Date().toISOString(),
			exitCode: 0,
			command: "bun test",
		};
		writeSentinel(tmpDir, "/test/project", "myexec", sentinel);

		// --cmd differs from sentinel.command → blocks
		const flags = { subcommand: "check" as const, name: "myexec", always: true, cmd: "true" };
		await expect(runExec(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(2);
		expect(blockMessage()).toContain("has no results");
	});

	it("skips command validation when sentinel has no command fields", async () => {
		const config = makeConfig(tmpDir);
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: new Date().toISOString(),
			finishedAt: new Date().toISOString(),
			exitCode: 0,
		};
		writeSentinel(tmpDir, "/test/project", "myexec", sentinel);

		// No command in sentinel → validation skipped → pass
		const flags = { subcommand: "check" as const, name: "myexec", always: true, cmd: "true" };
		await expect(runExec(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(0);
	});
});
