import { afterEach, beforeEach, describe, expect, it, spyOn } from "bun:test";
import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import type { AgentEvent, HookAdapter } from "../lib/adapter";
import {
	blockNoCount,
	blockWithLimit,
	evaluateSentinel,
	guardStopEvent,
	resolveTriggerPatterns,
} from "../lib/check";
import type { ResolvedConfig } from "../lib/config";
import type { SentinelData } from "../lib/sentinel";
import { readBlockCount, readCoordination, writeCoordination } from "../lib/sentinel";

// ---------------------------------------------------------------------------
// Test adapter: calls process.exit / process.stderr.write like the real one
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
		isStopRecursion: (e: AgentEvent) =>
			e.eventName === "Stop" && (e.raw as Record<string, unknown>).stop_hook_active === true,
		isShellToolCall: (e: AgentEvent) => e.eventName === "PreToolUse" && e.toolName === "Bash",
		getShellCommand: (e: AgentEvent) => {
			const input = e.toolInput as Record<string, unknown> | undefined;
			return typeof input?.command === "string" ? input.command : undefined;
		},
		stateKey: (e: AgentEvent) => e.eventName,
		commandSummary: () => "",
		...overrides,
	};
}

function makeEvent(partial: Partial<AgentEvent & { stop_hook_active?: boolean }> = {}): AgentEvent {
	const { stop_hook_active, ...rest } = partial;
	const raw = (rest.raw ?? {}) as Record<string, unknown>;
	if (stop_hook_active !== undefined) {
		raw.stop_hook_active = stop_hook_active;
	}
	return {
		eventName: rest.eventName ?? "",
		toolName: rest.toolName,
		toolInput: rest.toolInput,
		raw,
	};
}

// ---------------------------------------------------------------------------
// evaluateSentinel()
// ---------------------------------------------------------------------------

describe("evaluateSentinel()", () => {
	it("returns missing when sentinel is undefined", () => {
		expect(evaluateSentinel(undefined)).toEqual({ kind: "missing" });
	});

	it("returns pending when sentinel status is pending", () => {
		const sentinel: SentinelData = {
			status: "pending",
			startedAt: "2024-01-01T00:00:00Z",
		};
		expect(evaluateSentinel(sentinel)).toEqual({ kind: "pending" });
	});

	it("returns pass when sentinel status is pass", () => {
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: "2024-01-01T00:00:00Z",
			finishedAt: "2024-01-01T00:01:00Z",
			exitCode: 0,
		};
		expect(evaluateSentinel(sentinel)).toEqual({ kind: "pass" });
	});

	it("returns fail with sentinel when status is fail", () => {
		const sentinel: SentinelData = {
			status: "fail",
			startedAt: "2024-01-01T00:00:00Z",
			finishedAt: "2024-01-01T00:01:00Z",
			exitCode: 1,
			output: "test failed",
			command: "bun test",
		};
		const result = evaluateSentinel(sentinel);
		expect(result).toEqual({ kind: "fail", sentinel });
	});

	// -- Session-aware staleness --

	it("returns missing when sessionId does not match", () => {
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: "2024-01-01T00:00:00Z",
			sessionId: "old-session",
		};
		expect(evaluateSentinel(sentinel, "current-session")).toEqual({ kind: "missing" });
	});

	it("returns missing when sentinel has no sessionId but session is active", () => {
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: "2024-01-01T00:00:00Z",
		};
		expect(evaluateSentinel(sentinel, "current-session")).toEqual({ kind: "missing" });
	});

	it("returns pass when sessionId matches", () => {
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: "2024-01-01T00:00:00Z",
			sessionId: "same-session",
		};
		expect(evaluateSentinel(sentinel, "same-session")).toEqual({ kind: "pass" });
	});

	it("skips session check when no currentSessionId is provided", () => {
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: "2024-01-01T00:00:00Z",
			sessionId: "any-session",
		};
		expect(evaluateSentinel(sentinel)).toEqual({ kind: "pass" });
	});

	// -- Content-aware staleness --

	it("returns missing when contentHash does not match", () => {
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: "2024-01-01T00:00:00Z",
			contentHash: "hash-old",
		};
		expect(evaluateSentinel(sentinel, undefined, "hash-new")).toEqual({ kind: "missing" });
	});

	it("returns missing when sentinel has no contentHash but caller provides one", () => {
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: "2024-01-01T00:00:00Z",
		};
		expect(evaluateSentinel(sentinel, undefined, "hash-current")).toEqual({ kind: "missing" });
	});

	it("returns pass when contentHash matches", () => {
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: "2024-01-01T00:00:00Z",
			contentHash: "hash-match",
		};
		expect(evaluateSentinel(sentinel, undefined, "hash-match")).toEqual({ kind: "pass" });
	});

	it("skips content check when no currentContentHash is provided", () => {
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: "2024-01-01T00:00:00Z",
		};
		expect(evaluateSentinel(sentinel)).toEqual({ kind: "pass" });
	});

	// -- Combined session + content checks --

	it("returns missing when session matches but contentHash does not", () => {
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: "2024-01-01T00:00:00Z",
			sessionId: "same",
			contentHash: "hash-old",
		};
		expect(evaluateSentinel(sentinel, "same", "hash-new")).toEqual({ kind: "missing" });
	});

	it("returns pass when both session and contentHash match", () => {
		const sentinel: SentinelData = {
			status: "pass",
			startedAt: "2024-01-01T00:00:00Z",
			sessionId: "same",
			contentHash: "hash-match",
		};
		expect(evaluateSentinel(sentinel, "same", "hash-match")).toEqual({ kind: "pass" });
	});

	it("pending sentinels bypass content check", () => {
		const sentinel: SentinelData = {
			status: "pending",
			startedAt: "2024-01-01T00:00:00Z",
			sessionId: "same",
		};
		expect(evaluateSentinel(sentinel, "same", "any-hash")).toEqual({ kind: "pending" });
	});
});

// ---------------------------------------------------------------------------
// resolveTriggerPatterns()
// ---------------------------------------------------------------------------

describe("resolveTriggerPatterns()", () => {
	const makeConfig = (triggers: Record<string, string[]> = {}): ResolvedConfig => ({
		triggers,
		execs: {},
		tasks: {},
		sentinelDir: "/tmp/sentinels",
		projectDir: "/test/project",
	});

	it("returns inline trigger from --trigger flag", () => {
		const config = makeConfig({ "pre-commit": ["git commit"] });
		const result = resolveTriggerPatterns("test", config, { trigger: "npm publish" });
		expect(result).toEqual(["npm publish"]);
	});

	it("returns patterns from named trigger group via --on flag", () => {
		const config = makeConfig({ "pre-commit": ["git commit", "git push"] });
		const result = resolveTriggerPatterns("test", config, { on: "pre-commit" });
		expect(result).toEqual(["git commit", "git push"]);
	});

	it("returns empty array when --on references unknown trigger group", () => {
		const config = makeConfig({});
		const result = resolveTriggerPatterns("test", config, { on: "nonexistent" });
		expect(result).toEqual([]);
	});

	it("returns empty array when no flags are set", () => {
		const config = makeConfig({ "pre-commit": ["git commit"] });
		const result = resolveTriggerPatterns("test", config, {});
		expect(result).toEqual([]);
	});

	it("--trigger takes precedence over --on", () => {
		const config = makeConfig({ "pre-commit": ["git commit"] });
		const result = resolveTriggerPatterns("test", config, {
			trigger: "deploy",
			on: "pre-commit",
		});
		expect(result).toEqual(["deploy"]);
	});
});

// ---------------------------------------------------------------------------
// guardStopEvent()
// ---------------------------------------------------------------------------

describe("guardStopEvent()", () => {
	let exitSpy: ReturnType<typeof spyOn>;
	const ExitError = class extends Error {};

	beforeEach(() => {
		exitSpy = spyOn(process, "exit").mockImplementation(() => {
			throw new ExitError("process.exit called");
		});
	});

	afterEach(() => {
		exitSpy.mockRestore();
	});

	it("auto-allows when stop_hook_active=true and limit=0", () => {
		const adapter = makeTestAdapter();
		const event = makeEvent({ eventName: "Stop", stop_hook_active: true });
		expect(() => guardStopEvent("test", adapter, event, 0)).toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(0);
	});

	it("returns normally when stop_hook_active=true and limit > 0", () => {
		const adapter = makeTestAdapter();
		const event = makeEvent({ eventName: "Stop", stop_hook_active: true });
		// Should NOT exit — defers to blockWithLimit
		guardStopEvent("test", adapter, event, 3);
		expect(exitSpy).not.toHaveBeenCalled();
	});

	it("returns normally when stop_hook_active=false", () => {
		const adapter = makeTestAdapter();
		const event = makeEvent({ eventName: "Stop", stop_hook_active: false });
		guardStopEvent("test", adapter, event, 0);
		expect(exitSpy).not.toHaveBeenCalled();
	});

	it("returns normally for non-Stop events", () => {
		const adapter = makeTestAdapter();
		const event = makeEvent({ eventName: "PreToolUse" });
		guardStopEvent("test", adapter, event, 0);
		expect(exitSpy).not.toHaveBeenCalled();
	});

	it("returns normally when stop_hook_active is absent", () => {
		const adapter = makeTestAdapter();
		const event = makeEvent({ eventName: "Stop" });
		guardStopEvent("test", adapter, event, 0);
		expect(exitSpy).not.toHaveBeenCalled();
	});
});

// ---------------------------------------------------------------------------
// blockWithLimit()
// ---------------------------------------------------------------------------

describe("blockWithLimit()", () => {
	let exitSpy: ReturnType<typeof spyOn>;
	let stderrSpy: ReturnType<typeof spyOn>;
	let tmpDir: string;
	const ExitError = class extends Error {};

	beforeEach(() => {
		tmpDir = mkdtempSync(join(tmpdir(), "chunk-hook-check-test-"));
		exitSpy = spyOn(process, "exit").mockImplementation(() => {
			throw new ExitError("process.exit called");
		});
		stderrSpy = spyOn(process.stderr, "write").mockImplementation(() => true);
		// Disable delayed consumption for deterministic test behavior
		process.env.CHUNK_HOOK_CONSUME_DELAY_MS = "0";
	});

	afterEach(() => {
		exitSpy.mockRestore();
		stderrSpy.mockRestore();
		rmSync(tmpDir, { recursive: true, force: true });
		delete process.env.CHUNK_HOOK_CONSUME_DELAY_MS;
	});

	const makeConfig = (sentinelDir: string): ResolvedConfig => ({
		triggers: {},
		execs: {},
		tasks: {},
		sentinelDir,
		projectDir: "/test/project",
	});

	it("blocks with exit 2 on first call (under limit)", () => {
		const adapter = makeTestAdapter();
		const config = makeConfig(tmpDir);
		expect(() => blockWithLimit("test", adapter, config, "mycheck", 3, "fail reason")).toThrow(
			ExitError,
		);
		expect(exitSpy).toHaveBeenCalledWith(2);
	});

	it("auto-allows when block count exceeds limit", () => {
		const adapter = makeTestAdapter();
		const config = makeConfig(tmpDir);
		// Block 3 times (limit=3), then the 4th should auto-allow
		for (let i = 0; i < 3; i++) {
			try {
				blockWithLimit("test", adapter, config, "mycheck", 3, "fail");
			} catch {
				/* exit spy */
			}
		}
		// 4th call: count=4 > limit=3 → auto-allow (exit 0)
		exitSpy.mockClear();
		expect(() => blockWithLimit("test", adapter, config, "mycheck", 3, "fail")).toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(0);
	});

	it("does NOT record in coordination on auto-allow", () => {
		const adapter = makeTestAdapter();
		const config = makeConfig(tmpDir);

		// Pre-seed coordination: another command already passed.
		writeCoordination(tmpDir, "/test/project", { results: { other: "pass" } });

		// Exhaust the limit (3 blocks)
		for (let i = 0; i < 3; i++) {
			try {
				blockWithLimit("test", adapter, config, "mycheck", 3, "fail");
			} catch {
				/* */
			}
		}
		// 4th call triggers auto-allow but does NOT record in coordination
		try {
			blockWithLimit("test", adapter, config, "mycheck", 3, "fail");
		} catch {
			/* */
		}
		const coord = readCoordination(tmpDir, "/test/project");
		// Coordination unchanged — only "other" remains, no "mycheck" entry added
		expect(coord.results).toEqual({ other: "pass" });
	});

	it("never auto-allows when limit=0 (unlimited)", () => {
		const adapter = makeTestAdapter();
		const config = makeConfig(tmpDir);
		// Call multiple times — should always block (exit 2)
		for (let i = 0; i < 5; i++) {
			exitSpy.mockClear();
			expect(() => blockWithLimit("test", adapter, config, "mycheck", 0, "fail")).toThrow(
				ExitError,
			);
			expect(exitSpy).toHaveBeenCalledWith(2);
		}
	});

	it("writes reason to stderr when blocking", () => {
		const adapter = makeTestAdapter();
		const config = makeConfig(tmpDir);
		try {
			blockWithLimit("test", adapter, config, "mycheck", 5, "test failure output");
		} catch {
			/* */
		}
		expect(stderrSpy).toHaveBeenCalledWith("[project: /test/project]\ntest failure output\n");
	});
});

// ---------------------------------------------------------------------------
// blockNoCount()
// ---------------------------------------------------------------------------

describe("blockNoCount()", () => {
	let exitSpy: ReturnType<typeof spyOn>;
	let stderrSpy: ReturnType<typeof spyOn>;
	const ExitError = class extends Error {};

	beforeEach(() => {
		exitSpy = spyOn(process, "exit").mockImplementation(() => {
			throw new ExitError("process.exit called");
		});
		stderrSpy = spyOn(process.stderr, "write").mockImplementation(() => true);
	});

	afterEach(() => {
		exitSpy.mockRestore();
		stderrSpy.mockRestore();
	});

	it("blocks with exit 2", () => {
		const adapter = makeTestAdapter();
		expect(() => blockNoCount("test", adapter, "waiting for infra")).toThrow(ExitError);
		expect(exitSpy).toHaveBeenCalledWith(2);
	});

	it("writes reason to stderr (no project header without projectDir)", () => {
		const adapter = makeTestAdapter();
		try {
			blockNoCount("test", adapter, "still pending");
		} catch {
			/* */
		}
		expect(stderrSpy).toHaveBeenCalledWith("still pending\n");
	});

	it("prepends project header when projectDir is provided", () => {
		const adapter = makeTestAdapter();
		try {
			blockNoCount("test", adapter, "still pending", "/my/repo");
		} catch {
			/* */
		}
		expect(stderrSpy).toHaveBeenCalledWith("[project: /my/repo]\nstill pending\n");
	});

	it("does not increment block counter (no file written)", () => {
		const adapter = makeTestAdapter();
		const tmpDir = mkdtempSync(join(tmpdir(), "chunk-hook-nocount-test-"));
		try {
			// Call blockNoCount multiple times
			for (let i = 0; i < 5; i++) {
				try {
					blockNoCount("test", adapter, "pending");
				} catch {
					/* */
				}
			}
			// Counter should still be 0 — blockNoCount doesn't touch it
			expect(readBlockCount(tmpDir, "/test/project", "mycheck")).toBe(0);
		} finally {
			rmSync(tmpDir, { recursive: true, force: true });
		}
	});
});
