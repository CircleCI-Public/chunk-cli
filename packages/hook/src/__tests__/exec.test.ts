import { afterEach, beforeEach, describe, expect, it, spyOn } from "bun:test";
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
	const ExitError = class extends Error {};

	beforeEach(() => {
		tmpDir = mkdtempSync(join(tmpdir(), "chunk-hook-exec-test-"));
		exitSpy = spyOn(process, "exit").mockImplementation(() => {
			throw new ExitError("process.exit called");
		});
		stderrSpy = spyOn(process.stderr, "write").mockImplementation(() => true);
		process.env.CHUNK_HOOK_CONSUME_DELAY_MS = "0";
	});

	afterEach(() => {
		exitSpy.mockRestore();
		stderrSpy.mockRestore();
		rmSync(tmpDir, { recursive: true, force: true });
		delete process.env.CHUNK_HOOK_CONSUME_DELAY_MS;
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

		const flags = { subcommand: "check" as const, name: "myexec", always: true };
		await expect(runExec(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(2);
		expect(stderrSpy.mock.calls[0][0]).toContain("3 tests failed");
	});

	it("blocks with 'no results' message when no sentinel exists", async () => {
		const config = makeConfig(tmpDir);
		const flags = { subcommand: "check" as const, name: "myexec", always: true };
		await expect(runExec(config, makeTestAdapter(), makeEvent(), flags)).rejects.toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(2);
		const message = stderrSpy.mock.calls[0][0] as string;
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
		expect(stderrSpy.mock.calls[0][0]).toContain("still running");
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
		const message = stderrSpy.mock.calls[0][0] as string;
		// The suggested re-run command should have properly escaped single quotes
		expect(message).toContain(`--cmd 'echo '\\''world'\\'''`);
	});
});
