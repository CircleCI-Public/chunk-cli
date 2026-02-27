import { afterEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
	activateScope,
	deactivateScope,
	extractFilePaths,
	MARKER_REL,
	matchesProject,
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
		// CWD trust tests call activateScope(process.cwd(), ...) which writes a
		// marker inside the workspace.  Clean up only the marker file so we don't
		// destroy the real .chunk/hook/ config files committed in the repo.
		const cwdMarker = join(process.cwd(), MARKER_REL);
		if (existsSync(cwdMarker)) {
			try {
				rmSync(cwdMarker, { force: true });
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

	it("returns false for stale marker (different session ID, path mismatch)", () => {
		const dir = makeTmpDir();

		// Activate with session 1
		activateScope(dir, { tool_input: { file_path: `${dir}/file.ts` } }, "sess-1");

		// Different session sees the stale marker via path mismatch
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
	// CWD trust — bypass scope gate in single-repo / CLI mode
	// -------------------------------------------------------------------------

	describe("CWD trust (process.cwd() === projectDir)", () => {
		it("returns true when cwd matches projectDir (even with mismatch paths)", () => {
			const dir = process.cwd();
			const raw = { tool_input: { command: "/usr/local/go/bin/go test ./..." } };
			const result = activateScope(dir, raw, "sess-test");
			expect(result).toBe(true);
		});

		it("returns true when cwd matches projectDir (no paths event)", () => {
			const dir = process.cwd();
			const result = activateScope(dir, {}, "sess-test");
			expect(result).toBe(true);
		});

		it("returns true when cwd matches projectDir (no session ID)", () => {
			const dir = process.cwd();
			const result = activateScope(dir, {});
			expect(result).toBe(true);
		});

		it("falls through to normal logic when cwd doesn't match projectDir", () => {
			const dir = makeTmpDir();
			const raw = { tool_input: { command: "/usr/local/go/bin/go test ./..." } };
			const result = activateScope(dir, raw, "sess-test");
			expect(result).toBe(false);
		});

		it("activates scope normally when cwd doesn't match but paths do", () => {
			const dir = makeTmpDir();
			const raw = { tool_input: { file_path: `${dir}/src/file.ts` } };
			const result = activateScope(dir, raw, "sess-test");
			expect(result).toBe(true);
			expect(existsSync(join(dir, MARKER_REL))).toBe(true);
		});
	});
});
