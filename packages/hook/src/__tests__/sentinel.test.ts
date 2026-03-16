import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
	acquireLock,
	clearCoordination,
	clearCoordinationEntry,
	coordinationId,
	coordinationPath,
	incrementBlockCount,
	readBlockCount,
	readCoordination,
	readSentinel,
	recordAndTryConsume,
	resetBlockCount,
	sentinelId,
	sentinelPath,
	writeCoordination,
	writeSentinel,
} from "../lib/sentinel";

describe("sentinel", () => {
	const testDir = join(tmpdir(), "chunk-hook-test-sentinels", `${process.pid}-${Date.now()}`);
	const projectDir = "/fake/project";
	const name = "test" as const;

	beforeEach(() => {
		mkdirSync(testDir, { recursive: true });
		// Disable delayed consumption for deterministic test behavior
		process.env.CHUNK_HOOK_CONSUME_DELAY_MS = "0";
	});

	afterEach(() => {
		rmSync(testDir, { recursive: true, force: true });
		delete process.env.CHUNK_HOOK_CONSUME_DELAY_MS;
	});

	describe("sentinelId()", () => {
		it("returns a deterministic hash-based ID", () => {
			const id1 = sentinelId(projectDir, name);
			const id2 = sentinelId(projectDir, name);
			expect(id1).toBe(id2);
		});

		it("includes the command name as prefix", () => {
			const id = sentinelId(projectDir, name);
			expect(id).toStartWith("test-");
		});

		it("produces different IDs for different names", () => {
			const testId = sentinelId(projectDir, "test");
			const lintId = sentinelId(projectDir, "lint");
			expect(testId).not.toBe(lintId);
		});

		it("produces different IDs for different projects", () => {
			const id1 = sentinelId("/project/a", name);
			const id2 = sentinelId("/project/b", name);
			expect(id1).not.toBe(id2);
		});
	});

	describe("sentinelPath()", () => {
		it("returns a path inside the sentinel dir", () => {
			const path = sentinelPath(testDir, projectDir, name);
			expect(path).toStartWith(testDir);
			expect(path).toEndWith(".json");
		});
	});

	describe("write / read / remove", () => {
		it("writes and reads sentinel data", () => {
			const data = {
				status: "pass" as const,
				startedAt: "2024-01-01T00:00:00Z",
				finishedAt: "2024-01-01T00:01:00Z",
				exitCode: 0,
				command: "bun test",
				output: "all passed",
				project: projectDir,
			};

			const path = writeSentinel(testDir, projectDir, name, data);
			expect(existsSync(path)).toBe(true);

			const result = readSentinel(testDir, projectDir, name);
			expect(result).toEqual(data);
		});

		it("returns undefined for missing sentinel", () => {
			const result = readSentinel(testDir, "/nonexistent", name);
			expect(result).toBe(undefined);
		});
	});

	describe("block counter", () => {
		it("readBlockCount returns 0 when no counter file exists", () => {
			expect(readBlockCount(testDir, projectDir, name)).toBe(0);
		});

		it("incrementBlockCount increments from 0 to 1", () => {
			const count = incrementBlockCount(testDir, projectDir, name);
			expect(count).toBe(1);
			expect(readBlockCount(testDir, projectDir, name)).toBe(1);
		});

		it("incrementBlockCount increments consecutively", () => {
			incrementBlockCount(testDir, projectDir, name);
			incrementBlockCount(testDir, projectDir, name);
			const count = incrementBlockCount(testDir, projectDir, name);
			expect(count).toBe(3);
			expect(readBlockCount(testDir, projectDir, name)).toBe(3);
		});

		it("resetBlockCount clears the counter", () => {
			incrementBlockCount(testDir, projectDir, name);
			incrementBlockCount(testDir, projectDir, name);
			resetBlockCount(testDir, projectDir, name);
			expect(readBlockCount(testDir, projectDir, name)).toBe(0);
		});

		it("resetBlockCount is a no-op when no counter exists", () => {
			// Should not throw
			resetBlockCount(testDir, projectDir, name);
			expect(readBlockCount(testDir, projectDir, name)).toBe(0);
		});

		it("counters are independent per command name", () => {
			incrementBlockCount(testDir, projectDir, "exec-a");
			incrementBlockCount(testDir, projectDir, "exec-a");
			incrementBlockCount(testDir, projectDir, "exec-b");

			expect(readBlockCount(testDir, projectDir, "exec-a")).toBe(2);
			expect(readBlockCount(testDir, projectDir, "exec-b")).toBe(1);
		});
	});

	// -------------------------------------------------------------------------
	// Coordinated consumption
	// -------------------------------------------------------------------------

	describe("coordination", () => {
		describe("coordinationId()", () => {
			it("returns a deterministic ID for a project", () => {
				const id1 = coordinationId(projectDir);
				const id2 = coordinationId(projectDir);
				expect(id1).toBe(id2);
			});

			it("produces different IDs for different projects", () => {
				const id1 = coordinationId("/project/a");
				const id2 = coordinationId("/project/b");
				expect(id1).not.toBe(id2);
			});

			it("starts with coord- prefix", () => {
				expect(coordinationId(projectDir)).toStartWith("coord-");
			});
		});

		describe("coordinationPath()", () => {
			it("returns a path inside the sentinel dir", () => {
				const path = coordinationPath(testDir, projectDir);
				expect(path).toStartWith(testDir);
				expect(path).toEndWith(".json");
			});
		});

		describe("read / write / clear coordination", () => {
			it("returns empty results when no file exists", () => {
				const data = readCoordination(testDir, projectDir);
				expect(data).toEqual({ results: {} });
			});

			it("writes and reads coordination data", () => {
				const data = {
					results: { "cmd-a": "pass" as const, "cmd-b": "fail" as const },
				};
				writeCoordination(testDir, projectDir, data);
				const read = readCoordination(testDir, projectDir);
				expect(read).toEqual(data);
			});

			it("clearCoordination removes the file", () => {
				writeCoordination(testDir, projectDir, { results: { x: "pass" } });
				clearCoordination(testDir, projectDir);
				const data = readCoordination(testDir, projectDir);
				expect(data).toEqual({ results: {} });
			});
		});

		describe("recordAndTryConsume()", () => {
			it("single command pass: consumes sentinel and clears coordination", () => {
				writeSentinel(testDir, projectDir, "cmd-a", {
					status: "pass",
					startedAt: "2024-01-01T00:00:00Z",
				});

				const consumed = recordAndTryConsume(testDir, projectDir, "cmd-a", "pass");

				expect(consumed).toBe(true);
				// Sentinel file should be gone
				expect(readSentinel(testDir, projectDir, "cmd-a")).toBe(undefined);
				// Coordination file should be cleared
				expect(readCoordination(testDir, projectDir)).toEqual({ results: {} });
			});

			it("two commands, one pending: does not consume when second passes", () => {
				writeSentinel(testDir, projectDir, "cmd-a", {
					status: "pass",
					startedAt: "2024-01-01T00:00:00Z",
				});
				writeSentinel(testDir, projectDir, "cmd-b", {
					status: "pending",
					startedAt: "2024-01-01T00:00:00Z",
				});

				// cmd-b reports pending first, then cmd-a reports pass
				const consumed1 = recordAndTryConsume(testDir, projectDir, "cmd-b", "pending");
				expect(consumed1).toBe(false);
				const consumed2 = recordAndTryConsume(testDir, projectDir, "cmd-a", "pass");
				expect(consumed2).toBe(false);

				// Both sentinels still exist
				expect(readSentinel(testDir, projectDir, "cmd-a")).not.toBe(undefined);
				expect(readSentinel(testDir, projectDir, "cmd-b")).not.toBe(undefined);

				// Coordination shows both entries
				const coord = readCoordination(testDir, projectDir);
				expect(coord.results["cmd-a"]).toBe("pass");
				expect(coord.results["cmd-b"]).toBe("pending");
			});

			it("two commands, both pass: consumes both sentinels", () => {
				writeSentinel(testDir, projectDir, "cmd-a", {
					status: "pass",
					startedAt: "2024-01-01T00:00:00Z",
				});
				writeSentinel(testDir, projectDir, "cmd-b", {
					status: "pass",
					startedAt: "2024-01-01T00:00:00Z",
				});

				recordAndTryConsume(testDir, projectDir, "cmd-a", "pass");
				const consumed = recordAndTryConsume(testDir, projectDir, "cmd-b", "pass");

				expect(consumed).toBe(true);
				// Both sentinels consumed
				expect(readSentinel(testDir, projectDir, "cmd-a")).toBe(undefined);
				expect(readSentinel(testDir, projectDir, "cmd-b")).toBe(undefined);
				// Coordination cleared
				expect(readCoordination(testDir, projectDir)).toEqual({ results: {} });
			});

			it("mixed results (pass + fail): does not consume any sentinels", () => {
				writeSentinel(testDir, projectDir, "cmd-a", {
					status: "pass",
					startedAt: "2024-01-01T00:00:00Z",
				});
				writeSentinel(testDir, projectDir, "cmd-b", {
					status: "fail",
					startedAt: "2024-01-01T00:00:00Z",
					exitCode: 1,
				});

				recordAndTryConsume(testDir, projectDir, "cmd-b", "fail");
				const consumed = recordAndTryConsume(testDir, projectDir, "cmd-a", "pass");

				expect(consumed).toBe(false);
				// Both sentinels still intact
				expect(readSentinel(testDir, projectDir, "cmd-a")).not.toBe(undefined);
				expect(readSentinel(testDir, projectDir, "cmd-b")).not.toBe(undefined);
			});

			it("missing sentinel: records missing, does not consume", () => {
				const consumed = recordAndTryConsume(testDir, projectDir, "cmd-a", "missing");
				expect(consumed).toBe(false);

				const coord = readCoordination(testDir, projectDir);
				expect(coord.results["cmd-a"]).toBe("missing");
			});

			it("updates existing entry when called again (multi-command)", () => {
				writeSentinel(testDir, projectDir, "cmd-a", {
					status: "pass",
					startedAt: "2024-01-01T00:00:00Z",
				});
				writeSentinel(testDir, projectDir, "cmd-b", {
					status: "fail",
					startedAt: "2024-01-01T00:00:00Z",
					exitCode: 1,
				});

				// First cycle: both report, cmd-b fails
				recordAndTryConsume(testDir, projectDir, "cmd-b", "fail");
				recordAndTryConsume(testDir, projectDir, "cmd-a", "pass");
				expect(readCoordination(testDir, projectDir).results["cmd-a"]).toBe("pass");
				expect(readCoordination(testDir, projectDir).results["cmd-b"]).toBe("fail");

				// cmd-b re-runs and now passes — update its sentinel too
				writeSentinel(testDir, projectDir, "cmd-b", {
					status: "pass",
					startedAt: "2024-01-01T00:00:00Z",
				});
				const consumed = recordAndTryConsume(testDir, projectDir, "cmd-b", "pass");
				expect(consumed).toBe(true);
				// Both sentinels consumed, coordination cleared
				expect(readSentinel(testDir, projectDir, "cmd-a")).toBe(undefined);
				expect(readSentinel(testDir, projectDir, "cmd-b")).toBe(undefined);
				expect(readCoordination(testDir, projectDir)).toEqual({ results: {} });
			});

			it("does not consume before delay elapses, consumes after", async () => {
				writeSentinel(testDir, projectDir, "cmd-a", {
					status: "pass",
					startedAt: "2024-01-01T00:00:00Z",
				});

				const delayMs = 50;

				// First call: all pass, sets readyAt — not yet consumed
				const first = recordAndTryConsume(testDir, projectDir, "cmd-a", "pass", {
					consumeDelayMs: delayMs,
				});
				expect(first).toBe(false);
				expect(readCoordination(testDir, projectDir).readyAt).toBeDefined();
				expect(readSentinel(testDir, projectDir, "cmd-a")).not.toBe(undefined);

				// Wait for delay to elapse — generous buffer to avoid flaking under
				// CI load (10 ms was too tight; scheduler jitter easily exceeds that).
				await Bun.sleep(delayMs + 250);

				// Second call: delay elapsed — sentinel consumed
				const second = recordAndTryConsume(testDir, projectDir, "cmd-a", "pass", {
					consumeDelayMs: delayMs,
				});
				expect(second).toBe(true);
				expect(readSentinel(testDir, projectDir, "cmd-a")).toBe(undefined);
				expect(readCoordination(testDir, projectDir)).toEqual({ results: {} });
			});
		});

		describe("clearCoordinationEntry()", () => {
			it("removes a single entry from the coordination file", () => {
				writeCoordination(testDir, projectDir, {
					results: { "cmd-a": "pass", "cmd-b": "fail" },
				});

				clearCoordinationEntry(testDir, projectDir, "cmd-a");

				const coord = readCoordination(testDir, projectDir);
				expect(coord.results).toEqual({ "cmd-b": "fail" });
			});

			it("clears file when last entry removed", () => {
				writeCoordination(testDir, projectDir, {
					results: { "cmd-a": "pass" },
				});

				clearCoordinationEntry(testDir, projectDir, "cmd-a");

				expect(readCoordination(testDir, projectDir)).toEqual({ results: {} });
			});

			it("is a no-op when entry does not exist", () => {
				writeCoordination(testDir, projectDir, {
					results: { "cmd-a": "pass" },
				});

				clearCoordinationEntry(testDir, projectDir, "nonexistent");

				expect(readCoordination(testDir, projectDir).results).toEqual({
					"cmd-a": "pass",
				});
			});
		});

		describe("acquireLock()", () => {
			it("acquires and releases lock", () => {
				const fakePath = join(testDir, "test-lock-target");
				const release = acquireLock(fakePath);
				// Lock dir should exist
				expect(existsSync(`${fakePath}.lock`)).toBe(true);
				release();
				// Lock dir should be cleaned up
				expect(existsSync(`${fakePath}.lock`)).toBe(false);
			});

			it("two sequential acquires succeed", () => {
				const fakePath = join(testDir, "test-lock-target-2");
				const release1 = acquireLock(fakePath);
				release1();
				const release2 = acquireLock(fakePath);
				release2();
			});
		});
	});
});
