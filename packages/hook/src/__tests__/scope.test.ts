import { afterEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
	activateScope,
	deactivateScope,
	extractFilePaths,
	MARKER_REL,
	MARKER_TTL_MS,
	matchesProject,
	readMarker,
} from "../commands/scope";

// ---------------------------------------------------------------------------
// extractFilePaths()
// ---------------------------------------------------------------------------

describe("extractFilePaths()", () => {
	it("extracts file_path from tool_input", () => {
		const raw = { tool_input: { file_path: "/repo/a/file.ts" } };
		expect(extractFilePaths(raw)).toEqual(["/repo/a/file.ts"]);
	});

	it("extracts multiple path keys", () => {
		const raw = {
			tool_input: {
				file_path: "/repo/file.ts",
				directory: "/repo/src",
			},
		};
		const paths = extractFilePaths(raw);
		expect(paths).toContain("/repo/file.ts");
		expect(paths).toContain("/repo/src");
	});

	it("extracts first absolute path from command key", () => {
		const raw = {
			tool_input: { command: "cd /repo/src && go test ./..." },
		};
		expect(extractFilePaths(raw)).toEqual(["/repo/src"]);
	});

	it("stops at shell pipe operator", () => {
		const raw = {
			tool_input: { command: "cat /repo/src/file.ts | grep TODO" },
		};
		expect(extractFilePaths(raw)).toEqual(["/repo/src/file.ts"]);
	});

	it("stops at shell redirect operator", () => {
		const raw = {
			tool_input: { command: "ls /repo/src > /tmp/output.txt" },
		};
		// Extracts the first absolute path, stops at >
		expect(extractFilePaths(raw)).toEqual(["/repo/src"]);
	});

	it("stops at semicolon", () => {
		const raw = {
			tool_input: { command: "cd /repo/src; go test ./..." },
		};
		expect(extractFilePaths(raw)).toEqual(["/repo/src"]);
	});

	it("stops at ampersand", () => {
		const raw = {
			tool_input: { command: "cd /repo/src && go test ./..." },
		};
		expect(extractFilePaths(raw)).toEqual(["/repo/src"]);
	});

	it("stops at closing parenthesis", () => {
		const raw = {
			tool_input: { command: "(cd /repo/src) && echo done" },
		};
		expect(extractFilePaths(raw)).toEqual(["/repo/src"]);
	});

	it("ignores relative paths", () => {
		const raw = { tool_input: { file_path: "relative/file.ts" } };
		expect(extractFilePaths(raw)).toEqual([]);
	});

	it("ignores non-string values", () => {
		const raw = { tool_input: { file_path: 42, directory: true } };
		expect(extractFilePaths(raw)).toEqual([]);
	});

	it("returns empty array when tool_input is missing", () => {
		expect(extractFilePaths({})).toEqual([]);
		expect(extractFilePaths({ tool_input: null })).toEqual([]);
	});

	it("returns empty array when tool_input is not an object", () => {
		expect(extractFilePaths({ tool_input: "string" })).toEqual([]);
	});
});

// ---------------------------------------------------------------------------
// matchesProject()
// ---------------------------------------------------------------------------

describe("matchesProject()", () => {
	it("returns 'match' when file_path starts with project dir", () => {
		const raw = { tool_input: { file_path: "/repo/a/file.ts" } };
		expect(matchesProject("/repo/a", raw)).toBe("match");
	});

	it("returns 'mismatch' when file_path targets a different repo", () => {
		const raw = { tool_input: { file_path: "/other-repo/file.ts" } };
		expect(matchesProject("/repo", raw)).toBe("mismatch");
	});

	it("returns 'no-paths' when no paths are extractable", () => {
		expect(matchesProject("/repo", {})).toBe("no-paths");
		expect(matchesProject("/repo", { tool_input: {} })).toBe("no-paths");
	});

	it("returns 'match' when at least one path matches", () => {
		const raw = {
			tool_input: {
				file_path: "/other/file.ts",
				directory: "/repo/src",
			},
		};
		expect(matchesProject("/repo", raw)).toBe("match");
	});

	it("does not match partial directory names", () => {
		const raw = { tool_input: { file_path: "/repo-extended/file.ts" } };
		expect(matchesProject("/repo", raw)).toBe("mismatch");
	});

	it("returns 'match' for exact project dir path", () => {
		const raw = { tool_input: { directory: "/repo" } };
		expect(matchesProject("/repo", raw)).toBe("match");
	});
});

// ---------------------------------------------------------------------------
// activateScope() / deactivateScope()
// ---------------------------------------------------------------------------

describe("activateScope() and deactivateScope()", () => {
	let tmpDir: string;

	afterEach(() => {
		if (tmpDir) {
			try {
				rmSync(tmpDir, { recursive: true, force: true });
			} catch {
				// ignore
			}
		}
	});

	function makeTmpDir(): string {
		tmpDir = join(
			tmpdir(),
			`chunk-hook-scope-test-${Date.now()}-${Math.random().toString(36).slice(2)}`,
		);
		mkdirSync(tmpDir, { recursive: true });
		return tmpDir;
	}

	it("creates marker file when paths match and sessionId provided", () => {
		const dir = makeTmpDir();
		const raw = { tool_input: { file_path: `${dir}/src/file.ts` } };

		const result = activateScope(dir, raw, "sess-test");

		expect(result).toBe(true);
		const markerPath = join(dir, MARKER_REL);
		expect(existsSync(markerPath)).toBe(true);
		const content = JSON.parse(readFileSync(markerPath, "utf-8").trim());
		expect(content.sessionId).toBe("sess-test");
		expect(content.timestamp).toBeGreaterThan(0);
	});

	it("does NOT create marker when no paths are extractable (no-paths events skip activation)", () => {
		const dir = makeTmpDir();

		const result = activateScope(dir, {}, "sess-test");

		// No paths = no auto-activation. Events like Stop/SessionStart
		// should not light up repos that were never touched.
		expect(result).toBe(false);
		expect(existsSync(join(dir, MARKER_REL))).toBe(false);
	});

	it("does not create marker without sessionId even when paths match", () => {
		const dir = makeTmpDir();
		const raw = { tool_input: { file_path: `${dir}/src/file.ts` } };

		const result = activateScope(dir, raw);

		expect(result).toBe(false);
		expect(existsSync(join(dir, MARKER_REL))).toBe(false);
	});

	it("does not create marker without sessionId even when no paths (conservative)", () => {
		const dir = makeTmpDir();

		const result = activateScope(dir, {});

		expect(result).toBe(false);
		expect(existsSync(join(dir, MARKER_REL))).toBe(false);
	});

	it("does not create marker when paths target a different repo", () => {
		const dir = makeTmpDir();
		const raw = { tool_input: { file_path: "/completely/other/repo/file.ts" } };

		const result = activateScope(dir, raw, "sess-1");

		expect(result).toBe(false);
		expect(existsSync(join(dir, MARKER_REL))).toBe(false);
	});

	it("creates .chunk/hook directory if it doesn't exist (when paths match)", () => {
		const dir = makeTmpDir();
		const raw = { tool_input: { file_path: `${dir}/src/file.ts` } };

		activateScope(dir, raw, "sess-test");

		expect(existsSync(join(dir, ".chunk", "hook"))).toBe(true);
	});

	it("deactivateScope removes the marker file", () => {
		const dir = makeTmpDir();
		const raw = { tool_input: { file_path: `${dir}/src/file.ts` } };

		activateScope(dir, raw, "sess-test");
		expect(existsSync(join(dir, MARKER_REL))).toBe(true);

		deactivateScope(dir);
		expect(existsSync(join(dir, MARKER_REL))).toBe(false);
	});

	it("deactivateScope is idempotent (no error when marker doesn't exist)", () => {
		const dir = makeTmpDir();
		expect(() => deactivateScope(dir)).not.toThrow();
	});

	it("stores session ID in marker file", () => {
		const dir = makeTmpDir();
		const raw = { tool_input: { file_path: `${dir}/src/file.ts` } };

		activateScope(dir, raw, "session-abc-123");

		const content = JSON.parse(readFileSync(join(dir, MARKER_REL), "utf-8").trim());
		expect(content.sessionId).toBe("session-abc-123");
	});

	it("returns true for previously activated project when paths mismatch (sticky scope)", () => {
		const dir = makeTmpDir();

		// First call: paths match, activate
		activateScope(dir, { tool_input: { file_path: `${dir}/file.ts` } }, "sess-1");

		// Second call: paths target different repo — but marker exists from same
		// session, so scope stays active (sticky).
		const result = activateScope(
			dir,
			{ tool_input: { file_path: "/other-repo/file.ts" } },
			"sess-1",
		);

		expect(result).toBe(true);
	});

	it("returns true for previously activated project on no-paths event (same session)", () => {
		const dir = makeTmpDir();

		// First call: paths match, activate
		activateScope(dir, { tool_input: { file_path: `${dir}/file.ts` } }, "sess-1");

		// Second call: no paths (e.g. Stop event) — checks existing marker
		const result = activateScope(dir, {}, "sess-1");

		expect(result).toBe(true);
	});

	it("returns false for valid marker from different session (path mismatch)", () => {
		const dir = makeTmpDir();

		// Activate with session 1
		activateScope(dir, { tool_input: { file_path: `${dir}/file.ts` } }, "sess-1");

		// Different session sees the valid marker via path mismatch
		const result = activateScope(
			dir,
			{ tool_input: { file_path: "/other-repo/file.ts" } },
			"sess-2",
		);

		expect(result).toBe(false);
	});

	it("preserves existing marker when different session matches same project (subagent safety)", () => {
		const dir = makeTmpDir();

		// Parent session activates
		activateScope(dir, { tool_input: { file_path: `${dir}/file.ts` } }, "parent-sess");

		// Subagent (different session) makes a tool call that also matches this project
		const result = activateScope(
			dir,
			{ tool_input: { file_path: `${dir}/other-file.ts` } },
			"subagent-sess",
		);

		// Should return true (scope is active) but NOT overwrite the marker
		expect(result).toBe(true);

		// Marker still belongs to parent session
		const content = JSON.parse(readFileSync(join(dir, MARKER_REL), "utf-8").trim());
		expect(content.sessionId).toBe("parent-sess");
	});

	// -------------------------------------------------------------------------
	// TTL-based marker expiry
	// -------------------------------------------------------------------------

	/** Write a marker with a custom timestamp for TTL testing. */
	function writeExpiredMarker(dir: string, sessionId: string, ageMs: number): void {
		const markerPath = join(dir, MARKER_REL);
		const markerDir = join(dir, ".chunk", "hook");
		if (!existsSync(markerDir)) {
			mkdirSync(markerDir, { recursive: true });
		}
		const content = { sessionId, timestamp: Date.now() - ageMs };
		writeFileSync(markerPath, `${JSON.stringify(content)}\n`);
	}

	it("reclaims expired marker from different session (path match)", () => {
		const dir = makeTmpDir();

		// Old session left an expired marker
		writeExpiredMarker(dir, "dead-sess", MARKER_TTL_MS + 1000);

		// New session activates with matching paths
		const result = activateScope(dir, { tool_input: { file_path: `${dir}/file.ts` } }, "new-sess");

		expect(result).toBe(true);
		const marker = readMarker(dir);
		expect(marker?.sessionId).toBe("new-sess");
	});

	it("reclaims expired marker from different session (no-path event)", () => {
		const dir = makeTmpDir();

		// Old session left an expired marker
		writeExpiredMarker(dir, "dead-sess", MARKER_TTL_MS + 1000);

		// New session encounters it via a no-paths event (e.g. Stop)
		const result = activateScope(dir, {}, "new-sess");

		expect(result).toBe(true);
		const marker = readMarker(dir);
		expect(marker?.sessionId).toBe("new-sess");
	});

	it("does NOT reclaim non-expired marker from different session (path match)", () => {
		const dir = makeTmpDir();

		// Recent marker from another session
		writeExpiredMarker(dir, "active-sess", MARKER_TTL_MS - 60_000);

		const result = activateScope(dir, { tool_input: { file_path: `${dir}/file.ts` } }, "new-sess");

		// Returns true (subagent safety) but marker is NOT overwritten
		expect(result).toBe(true);
		const marker = readMarker(dir);
		expect(marker?.sessionId).toBe("active-sess");
	});

	it("does NOT reclaim non-expired marker from different session (no-path event)", () => {
		const dir = makeTmpDir();

		writeExpiredMarker(dir, "active-sess", MARKER_TTL_MS - 60_000);

		const result = activateScope(
			dir,
			{ tool_input: { file_path: "/other-repo/file.ts" } },
			"new-sess",
		);

		expect(result).toBe(false);
	});

	it("refreshes marker timestamp on same-session no-path event", () => {
		const dir = makeTmpDir();

		// Activate, then artificially age the marker
		activateScope(dir, { tool_input: { file_path: `${dir}/file.ts` } }, "sess-1");
		const beforeTs = readMarker(dir)?.timestamp ?? 0;
		expect(beforeTs).toBeGreaterThan(0);

		// Write an older timestamp to simulate time passing
		writeExpiredMarker(dir, "sess-1", 120_000);
		const agedTs = readMarker(dir)?.timestamp ?? 0;
		expect(agedTs).toBeLessThan(beforeTs);

		// Same session, no-path event should refresh
		activateScope(dir, {}, "sess-1");
		const afterMarker = readMarker(dir);
		expect(afterMarker?.timestamp).toBeGreaterThan(agedTs);
		expect(afterMarker?.sessionId).toBe("sess-1");
	});

	it("skips session validation when no session ID provided (no-paths event)", () => {
		const dir = makeTmpDir();

		// Activate with a session ID
		activateScope(dir, { tool_input: { file_path: `${dir}/file.ts` } }, "sess-1");

		// No-session, no-paths call: checks existing marker, skips session validation
		const result = activateScope(dir, {});

		expect(result).toBe(true);
	});

	it("returns false when marker absent and paths don't match", () => {
		const dir = makeTmpDir();

		const result = activateScope(
			dir,
			{ tool_input: { file_path: "/other-repo/file.ts" } },
			"sess-1",
		);

		expect(result).toBe(false);
	});

	// -------------------------------------------------------------------------
	// System-path and no-path events (CWD matching does NOT grant trust)
	// -------------------------------------------------------------------------

	describe("system-path and no-path events", () => {
		it("does not activate on system-path-only tool calls (no file paths reference projectDir)", () => {
			const dir = makeTmpDir();
			const raw = { tool_input: { command: "/usr/local/go/bin/go test ./..." } };
			const result = activateScope(dir, raw, "sess-test");
			expect(result).toBe(false);
			expect(existsSync(join(dir, MARKER_REL))).toBe(false);
		});

		it("does not activate on no-paths event without marker (Stop event, never activated)", () => {
			const dir = makeTmpDir();
			const result = activateScope(dir, {}, "sess-test");
			expect(result).toBe(false);
		});

		it("does not activate on no-paths event without session ID", () => {
			const dir = makeTmpDir();
			const result = activateScope(dir, {});
			expect(result).toBe(false);
		});

		it("returns true on no-paths event when marker exists from prior file-path activation", () => {
			const dir = makeTmpDir();
			// First: activate via a tool call with matching file paths
			activateScope(dir, { tool_input: { file_path: `${dir}/src/file.ts` } }, "sess-test");
			expect(existsSync(join(dir, MARKER_REL))).toBe(true);

			// Second: no-paths event — marker exists, same session → active
			const result = activateScope(dir, {}, "sess-test");
			expect(result).toBe(true);
		});

		it("activates scope when file paths match projectDir", () => {
			const dir = makeTmpDir();
			const raw = { tool_input: { file_path: `${dir}/src/file.ts` } };
			const result = activateScope(dir, raw, "sess-test");
			expect(result).toBe(true);
			expect(existsSync(join(dir, MARKER_REL))).toBe(true);
		});
	});
});
