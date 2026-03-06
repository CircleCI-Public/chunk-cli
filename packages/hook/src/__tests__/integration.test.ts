/**
 * Integration tests for chunk hook CLI commands.
 *
 * These tests exercise the full CLI binary as a subprocess, simulating
 * how Claude Code invokes hooks: feeding JSON on stdin, checking exit
 * codes, and verifying stderr/stdout output.
 *
 * Goals:
 *   - Verify hooks properly fire and produce correct exit codes
 *   - Detect deadlocks (stdin reading, spinlock contention)
 *   - Detect unexpected behavior in the full flow
 *   - Test coordination between multiple commands on the same event
 *   - Test scope activation / deactivation lifecycle
 *   - Test state save / load / clear lifecycle
 *   - Test block limit enforcement and counter semantics
 *   - Verify re-runs do NOT reset the block counter (only pass does)
 *   - Test stop event recursion guard
 */

import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// Point to the main chunk CLI entry point — hook commands live under `chunk hook`
// Repo root where bunfig.toml lives — must be the cwd when spawning bun so
// that the VERSION define and .md loader are in effect.
const REPO_ROOT = join(import.meta.dir, "..", "..", "..", "..");
const CLI_PATH = join(REPO_ROOT, "src", "index.ts");

/**
 * Build a clean base environment by stripping CHUNK_HOOK_* and CLAUDE_* variables
 * from process.env. Defence-in-depth for local runs where the user might
 * have CHUNK_HOOK vars set in their shell.
 */
function cleanBaseEnv(): Record<string, string> {
	const clean: Record<string, string> = {};
	for (const [key, value] of Object.entries(process.env)) {
		if (value === undefined) continue;
		if (key.startsWith("CHUNK_HOOK_") || key.startsWith("CLAUDE_")) continue;
		clean[key] = value;
	}
	return clean;
}

/** Result of running the CLI. */
type CliResult = {
	exitCode: number;
	stdout: string;
	stderr: string;
};

/**
 * Run the chunk CLI as a subprocess with `hook` prefix.
 *
 * @param args - CLI arguments after `hook` (e.g., ["exec", "run", "tests"])
 * @param stdin - JSON string to feed on stdin (simulates hook input)
 * @param env - Extra environment variables
 * @param timeoutMs - Timeout in milliseconds (deadlock detection)
 */
async function runCli(
	args: string[],
	stdin: string = "",
	env: Record<string, string> = {},
	timeoutMs: number = 10_000,
): Promise<CliResult> {
	const proc = Bun.spawn(["bun", "run", CLI_PATH, "hook", ...args], {
		cwd: REPO_ROOT,
		stdin: "pipe",
		stdout: "pipe",
		stderr: "pipe",
		env: { ...cleanBaseEnv(), ...env },
	});

	// Write stdin and close — must not hang
	if (stdin) {
		proc.stdin.write(stdin);
	}
	proc.stdin.end();

	// Race the process exit against a timeout (deadlock detection)
	const timeoutPromise = new Promise<"timeout">((resolve) =>
		setTimeout(() => resolve("timeout"), timeoutMs),
	);

	const raceResult = await Promise.race([proc.exited, timeoutPromise]);

	if (raceResult === "timeout") {
		proc.kill("SIGKILL");
		await proc.exited;
		throw new Error(
			`CLI timed out after ${timeoutMs}ms — possible deadlock or hang.\n` +
				`Args: ${JSON.stringify(args)}\nStdin: ${stdin.slice(0, 200)}`,
		);
	}

	const [stdout, stderr] = await Promise.all([
		new Response(proc.stdout).text(),
		new Response(proc.stderr).text(),
	]);

	return {
		exitCode: proc.exitCode ?? 1,
		stdout: stdout.trim(),
		stderr: stderr.trim(),
	};
}

/** Create a hook event JSON payload. */
function hookEvent(overrides: Record<string, unknown> = {}): string {
	return JSON.stringify({
		hook_event_name: "PreToolUse",
		tool_name: "Bash",
		tool_input: { command: "echo hello" },
		session_id: "test-session-001",
		cwd: "/tmp/test-project",
		...overrides,
	});
}

/** Create a minimal config.yml content. */
function minimalConfig(_overrides: Record<string, unknown> = {}): string {
	// The config loader uses the `yaml` package, so we write actual YAML
	const yaml = `
execs:
  tests:
    command: "echo 'all tests passed'"
    always: true
    timeout: 30
  fail-cmd:
    command: "sh -c 'echo FAIL && exit 1'"
    always: true
    timeout: 30
  timeout-cmd:
    command: "sleep 999"
    always: true
    timeout: 2
  lint:
    command: "echo 'lint ok'"
    always: true
    timeout: 10
tasks:
  review:
    instructions: ".chunk/hook/review-instructions.md"
    schema: ""
    limit: 3
    always: true
triggers:
  pre-commit:
    - "git commit"
    - "git push"
`;
	return yaml;
}

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

let testProjectDir: string;
let sentinelDir: string;
let logDir: string;

beforeEach(() => {
	testProjectDir = mkdtempSync(join(tmpdir(), "chunk-hook-integ-"));
	sentinelDir = mkdtempSync(join(tmpdir(), "chunk-hook-sentinels-"));
	logDir = mkdtempSync(join(tmpdir(), "chunk-hook-logs-"));

	// Create .chunk/hook/config.yml
	const hookDir = join(testProjectDir, ".chunk", "hook");
	mkdirSync(hookDir, { recursive: true });
	writeFileSync(join(hookDir, "config.yml"), minimalConfig());

	// Create task instructions
	writeFileSync(
		join(hookDir, "review-instructions.md"),
		"# Code Review\n\nReview the changes for correctness.\n",
	);

	// Initialize a git repo so git-related checks work
	Bun.spawnSync(["git", "init"], { cwd: testProjectDir });
	Bun.spawnSync(["git", "config", "user.email", "test@test.com"], { cwd: testProjectDir });
	Bun.spawnSync(["git", "config", "user.name", "Test"], { cwd: testProjectDir });
	// Create an initial commit so HEAD exists
	writeFileSync(join(testProjectDir, "README.md"), "# Test\n");
	Bun.spawnSync(["git", "add", "."], { cwd: testProjectDir });
	Bun.spawnSync(["git", "commit", "-m", "initial"], { cwd: testProjectDir });

	// Pre-activate scope — mirrors production where scope is always activated
	// by a prior PreToolUse event before exec run --no-check fires.
	// Uses the same session_id as hookEvent() default.
	writeFileSync(
		join(hookDir, ".chunk-hook-active"),
		`${JSON.stringify({ sessionId: "test-session-001", timestamp: Date.now() })}\n`,
	);
});

afterEach(() => {
	rmSync(testProjectDir, { recursive: true, force: true });
	rmSync(sentinelDir, { recursive: true, force: true });
	rmSync(logDir, { recursive: true, force: true });
});

/**
 * Standard env for all tests — enables all commands and isolates all
 * filesystem-backed state (sentinels, logs, config) into per-test temp dirs.
 * This prevents collisions when `bun test` is spawned inside a chunk hook
 * (e.g. `hook exec run tests`), where the hook's own sentinels/logs would
 * otherwise share the same $TMPDIR/chunk-hook/ namespace.
 */
function testEnv(extra: Record<string, string> = {}): Record<string, string> {
	return {
		CHUNK_HOOK_ENABLE: "1",
		CHUNK_HOOK_SENTINELS_DIR: sentinelDir,
		CHUNK_HOOK_LOG_DIR: logDir,
		CHUNK_HOOK_CONFIG: join(testProjectDir, ".chunk", "hook", "config.yml"),
		CHUNK_HOOK_VERBOSE: "1",
		// Disable delayed consumption for deterministic test behavior
		CHUNK_HOOK_CONSUME_DELAY_MS: "0",
		...extra,
	};
}

/** Hook event with tool_input path pointing to the test project. */
function projectEvent(overrides: Record<string, unknown> = {}): string {
	return hookEvent({
		cwd: testProjectDir,
		tool_input: { file_path: join(testProjectDir, "src/main.go") },
		...overrides,
	});
}

// ===========================================================================
// 1. EXEC COMMAND TESTS
// ===========================================================================

describe("exec run (direct invocation)", () => {
	it("exits 0 when command passes", async () => {
		const result = await runCli(["exec", "run", "tests", "--always"], projectEvent(), testEnv());
		expect(result.exitCode).toBe(0);
	});

	it("exits 2 when command fails", async () => {
		const result = await runCli(["exec", "run", "fail-cmd", "--always"], projectEvent(), testEnv());
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("fail-cmd");
		expect(result.stderr).toContain("FAIL");
	});

	it("includes exit code in failure message", async () => {
		const result = await runCli(["exec", "run", "fail-cmd", "--always"], projectEvent(), testEnv());
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("exit 1");
	});
});

describe("exec run --no-check (deferred)", () => {
	it("always exits 0 even when command fails", async () => {
		const result = await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"", // --no-check skips stdin reading
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(result.exitCode).toBe(0);
	});

	it("always exits 0 when command passes", async () => {
		const result = await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(result.exitCode).toBe(0);
	});

	it("writes sentinel file that can be read by check", async () => {
		// Run the command (deferred)
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Now check should find the result
		const checkResult = await runCli(
			["exec", "check", "tests", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(checkResult.exitCode).toBe(0);
	});
});

describe("exec check (deferred check)", () => {
	it("exits 2 with 'no results' when no sentinel exists", async () => {
		const result = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("no results");
		expect(result.stderr).toContain("Run it first");
	});

	it("exits 0 after successful run --no-check", async () => {
		// Run command (deferred)
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Check should pass
		const result = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(result.exitCode).toBe(0);
	});

	it("exits 2 after failed run --no-check", async () => {
		// Run failing command (deferred)
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Check should block
		const result = await runCli(
			["exec", "check", "fail-cmd", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("FAIL");
	});

	it("includes runner command in missing-result block message", async () => {
		const result = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("chunk hook exec run tests --no-check");
	});
});

// ===========================================================================
// 2. FULL CYCLE: run → check → re-run → check
// ===========================================================================

describe("exec full lifecycle", () => {
	it("fail → fix → pass cycle works correctly", async () => {
		// Step 1: Run failing command (deferred)
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Step 2: Check shows failure
		const checkFail = await runCli(
			["exec", "check", "fail-cmd", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(checkFail.exitCode).toBe(2);

		// Step 3: "Fix" by running the passing command with the same name override
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always", "--cmd", "echo fixed"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Step 4: Check now passes
		const checkPass = await runCli(
			["exec", "check", "fail-cmd", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(checkPass.exitCode).toBe(0);
	});
});

// ===========================================================================
// 3. BLOCK LIMIT ENFORCEMENT
// ===========================================================================

describe("block limit", () => {
	it("auto-allows after exceeding --limit", async () => {
		// Write a failing sentinel
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Check 1: should block (count 1)
		const r1 = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "2"],
			projectEvent(),
			testEnv(),
		);
		expect(r1.exitCode).toBe(2);

		// Check 2: should block (count 2)
		const r2 = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "2"],
			projectEvent(),
			testEnv(),
		);
		expect(r2.exitCode).toBe(2);

		// Check 3: count 3 > limit 2 → auto-allow
		const r3 = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "2"],
			projectEvent(),
			testEnv(),
		);
		expect(r3.exitCode).toBe(0);
	});

	it("re-run does NOT reset the block counter", async () => {
		// This test validates the fix for the block counter reset bug:
		// `run --no-check` must NOT reset the counter, otherwise the block
		// limit can never be reached in the check→re-run→check cycle.

		// Run a failing command
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Check twice (count → 1, then 2)
		const r1 = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "3"],
			projectEvent(),
			testEnv(),
		);
		expect(r1.exitCode).toBe(2);
		const r2 = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "3"],
			projectEvent(),
			testEnv(),
		);
		expect(r2.exitCode).toBe(2);

		// Re-run (agent retries after "fixing" the issue, but still fails)
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Counter should still be at 2 (not reset to 0).
		// Check 3: count 3 → blocks (equals limit)
		const r3 = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "3"],
			projectEvent(),
			testEnv(),
		);
		expect(r3.exitCode).toBe(2);

		// Check 4: count 4 > limit 3 → auto-allow (limit reached despite re-run)
		const r4 = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "3"],
			projectEvent(),
			testEnv(),
		);
		expect(r4.exitCode).toBe(0);
	});

	it("counter resets when check evaluates as pass", async () => {
		// Run a failing command, accumulate blocks, then run passing → counter resets
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Check twice (count → 1, then 2)
		await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "5"],
			projectEvent(),
			testEnv(),
		);
		await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "5"],
			projectEvent(),
			testEnv(),
		);

		// Now "fix" the command so it passes
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always", "--cmd", "echo fixed"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Check passes → counter resets to 0
		const rPass = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "5"],
			projectEvent(),
			testEnv(),
		);
		expect(rPass.exitCode).toBe(0);

		// Run failing again
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Counter was reset by the pass, so we need limit+1 checks to auto-allow
		for (let i = 0; i < 5; i++) {
			const r = await runCli(
				["exec", "check", "fail-cmd", "--always", "--limit", "5"],
				projectEvent(),
				testEnv(),
			);
			expect(r.exitCode).toBe(2);
		}

		// Count 6 > limit 5 → auto-allow (proving reset happened on pass)
		const rAllow = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "5"],
			projectEvent(),
			testEnv(),
		);
		expect(rAllow.exitCode).toBe(0);
	});
});

// ===========================================================================
// 4. STOP EVENT RECURSION GUARD
// ===========================================================================

describe("stop event recursion guard", () => {
	it("auto-allows on stop_hook_active=true with limit=0", async () => {
		// Write a failing sentinel first
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Stop event with stop_hook_active=true and no limit → auto-allow
		const result = await runCli(
			["exec", "check", "fail-cmd", "--always"],
			hookEvent({
				hook_event_name: "Stop",
				stop_hook_active: true,
				cwd: testProjectDir,
				tool_input: { file_path: join(testProjectDir, "main.go") },
			}),
			testEnv(),
		);
		expect(result.exitCode).toBe(0);
	});

	it("still enforces limit>0 on stop recursion", async () => {
		// Write a failing sentinel
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Stop event with stop_hook_active=true and limit=5 → blocks (defers to limit)
		const result = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "5"],
			hookEvent({
				hook_event_name: "Stop",
				stop_hook_active: true,
				cwd: testProjectDir,
				tool_input: { file_path: join(testProjectDir, "main.go") },
			}),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
	});
});

// ===========================================================================
// 5. TRIGGER MATCHING
// ===========================================================================

describe("trigger matching", () => {
	it("allows when trigger does not match the command", async () => {
		// Write a failing sentinel
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Check with trigger that doesn't match
		const result = await runCli(
			["exec", "check", "fail-cmd", "--always", "--trigger", "git commit"],
			hookEvent({
				hook_event_name: "PreToolUse",
				tool_name: "Bash",
				tool_input: { command: "npm test" },
				cwd: testProjectDir,
			}),
			testEnv(),
		);
		// Should allow because trigger doesn't match
		expect(result.exitCode).toBe(0);
	});

	it("blocks when trigger matches the command", async () => {
		// Write a failing sentinel
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Check with trigger that matches — tool_input includes both a file_path
		// (so activateScope recognizes the project) and a command (for trigger matching).
		const result = await runCli(
			["exec", "check", "fail-cmd", "--always", "--trigger", "git commit"],
			hookEvent({
				hook_event_name: "PreToolUse",
				tool_name: "Bash",
				tool_input: {
					command: "git commit -m 'test'",
					file_path: join(testProjectDir, "main.go"),
				},
				session_id: "trigger-sess",
				cwd: testProjectDir,
			}),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
	});

	it("matches named trigger group via --on", async () => {
		// Write a failing sentinel
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Check with --on pre-commit (matches git commit, git push).
		// tool_input includes file_path so activateScope recognizes the project.
		const result = await runCli(
			["exec", "check", "fail-cmd", "--always", "--on", "pre-commit"],
			hookEvent({
				hook_event_name: "PreToolUse",
				tool_name: "Bash",
				tool_input: {
					command: "git push origin main",
					file_path: join(testProjectDir, "main.go"),
				},
				session_id: "trigger-sess",
				cwd: testProjectDir,
			}),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
	});
});

// ===========================================================================
// 6. SCOPE ACTIVATION / DEACTIVATION
// ===========================================================================

describe("scope lifecycle", () => {
	it("activate + deactivate creates and removes marker", async () => {
		const markerPath = join(testProjectDir, ".chunk", "hook", ".chunk-hook-active");
		// Remove beforeEach marker — this test controls scope from scratch.
		rmSync(markerPath, { force: true });

		// Activate with matching file paths
		const activateResult = await runCli(
			["scope", "activate"],
			JSON.stringify({
				session_id: "sess-123",
				tool_input: { file_path: join(testProjectDir, "main.go") },
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(activateResult.exitCode).toBe(0);
		expect(existsSync(markerPath)).toBe(true);

		// Verify marker content
		const marker = JSON.parse(readFileSync(markerPath, "utf-8"));
		expect(marker.sessionId).toBe("sess-123");
		expect(typeof marker.timestamp).toBe("number");

		// Deactivate
		const deactivateResult = await runCli(
			["scope", "deactivate"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(deactivateResult.exitCode).toBe(0);
		expect(existsSync(markerPath)).toBe(false);
	});

	it("does not activate without file paths (no-paths event)", async () => {
		const markerPath = join(testProjectDir, ".chunk", "hook", ".chunk-hook-active");
		// Remove beforeEach marker — this test controls scope from scratch.
		rmSync(markerPath, { force: true });

		const result = await runCli(
			["scope", "activate"],
			JSON.stringify({
				session_id: "sess-123",
				hook_event_name: "Stop",
				// No tool_input with file paths
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(result.exitCode).toBe(0);
		expect(existsSync(markerPath)).toBe(false);
	});

	it("does not activate without session ID", async () => {
		const markerPath = join(testProjectDir, ".chunk", "hook", ".chunk-hook-active");
		// Remove beforeEach marker — this test controls scope from scratch.
		rmSync(markerPath, { force: true });

		const result = await runCli(
			["scope", "activate"],
			JSON.stringify({
				// No session_id
				tool_input: { file_path: join(testProjectDir, "main.go") },
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(result.exitCode).toBe(0);
		expect(existsSync(markerPath)).toBe(false);
	});

	it("does not overwrite marker from different session (subagent safety)", async () => {
		const markerPath = join(testProjectDir, ".chunk", "hook", ".chunk-hook-active");
		// Remove beforeEach marker — this test controls scope from scratch.
		rmSync(markerPath, { force: true });

		// Parent session activates
		await runCli(
			["scope", "activate"],
			JSON.stringify({
				session_id: "parent-session",
				tool_input: { file_path: join(testProjectDir, "main.go") },
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		const parentMarker = JSON.parse(readFileSync(markerPath, "utf-8"));
		expect(parentMarker.sessionId).toBe("parent-session");

		// Subagent tries to activate with different session
		await runCli(
			["scope", "activate"],
			JSON.stringify({
				session_id: "subagent-session",
				tool_input: { file_path: join(testProjectDir, "main.go") },
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Marker should still belong to parent
		const afterMarker = JSON.parse(readFileSync(markerPath, "utf-8"));
		expect(afterMarker.sessionId).toBe("parent-session");
	});
});

// ===========================================================================
// 7. STATE SAVE / LOAD / CLEAR
// ===========================================================================

describe("state lifecycle", () => {
	it("save + load cycle works", async () => {
		// Save a UserPromptSubmit event
		const saveResult = await runCli(
			["state", "save"],
			JSON.stringify({
				hook_event_name: "UserPromptSubmit",
				prompt: "fix the bug in main.go",
				session_id: "sess-001",
				cwd: testProjectDir,
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(saveResult.exitCode).toBe(0);

		// Load the entire state
		const loadAllResult = await runCli(
			["state", "load"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(loadAllResult.exitCode).toBe(0);
		const state = JSON.parse(loadAllResult.stdout);
		expect(state.UserPromptSubmit).toBeDefined();
		expect(state.UserPromptSubmit.__entries[0].prompt).toBe("fix the bug in main.go");
	});

	it("load with dot-notation field path", async () => {
		// Save
		await runCli(
			["state", "save"],
			JSON.stringify({
				hook_event_name: "UserPromptSubmit",
				prompt: "implement feature X",
				cwd: testProjectDir,
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Load specific field
		const result = await runCli(
			["state", "load", "UserPromptSubmit.prompt"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(result.exitCode).toBe(0);
		expect(result.stdout).toBe("implement feature X");
	});

	it("clear removes all state", async () => {
		// Save
		await runCli(
			["state", "save"],
			JSON.stringify({
				hook_event_name: "UserPromptSubmit",
				prompt: "test",
				cwd: testProjectDir,
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Clear
		const clearResult = await runCli(
			["state", "clear"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(clearResult.exitCode).toBe(0);

		// Load should return empty
		const loadResult = await runCli(
			["state", "load"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(loadResult.exitCode).toBe(0);
		// Empty state should be {} or empty
		const state = loadResult.stdout ? JSON.parse(loadResult.stdout) : {};
		expect(Object.keys(state).length).toBe(0);
	});

	it("saves multiple events independently", async () => {
		// Save UserPromptSubmit
		await runCli(
			["state", "save"],
			JSON.stringify({
				hook_event_name: "UserPromptSubmit",
				prompt: "hello",
				cwd: testProjectDir,
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Save Stop event
		await runCli(
			["state", "save"],
			JSON.stringify({
				hook_event_name: "Stop",
				transcript_path: "/tmp/transcript.json",
				cwd: testProjectDir,
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Both should be present
		const result = await runCli(
			["state", "load"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		const state = JSON.parse(result.stdout);
		expect(state.UserPromptSubmit.__entries[0].prompt).toBe("hello");
		expect(state.Stop.__entries[0].transcript_path).toBe("/tmp/transcript.json");
	});

	it("append accumulates entries", async () => {
		// First append
		await runCli(
			["state", "append"],
			JSON.stringify({
				hook_event_name: "UserPromptSubmit",
				prompt: "first prompt",
				cwd: testProjectDir,
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Second append with different prompt
		await runCli(
			["state", "append"],
			JSON.stringify({
				hook_event_name: "UserPromptSubmit",
				prompt: "second prompt",
				cwd: testProjectDir,
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Load and verify both entries exist
		const result = await runCli(
			["state", "load"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		const state = JSON.parse(result.stdout);
		const entries = state.UserPromptSubmit.__entries;
		expect(entries).toHaveLength(2);
		expect(entries[0].prompt).toBe("first prompt");
		expect(entries[1].prompt).toBe("second prompt");
		// Both entries should have head and fingerprint from the git repo
		expect(entries[0].head).toBeString();
		expect(entries[0].fingerprint).toBeString();
		expect(entries[1].head).toBeString();
		expect(entries[1].fingerprint).toBeString();
	});

	it("append deduplicates consecutive same-prompt entries", async () => {
		await runCli(
			["state", "append"],
			JSON.stringify({
				hook_event_name: "UserPromptSubmit",
				prompt: "same prompt",
				cwd: testProjectDir,
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		await runCli(
			["state", "append"],
			JSON.stringify({
				hook_event_name: "UserPromptSubmit",
				prompt: "same prompt",
				cwd: testProjectDir,
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		const result = await runCli(
			["state", "load"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		const state = JSON.parse(result.stdout);
		expect(state.UserPromptSubmit.__entries).toHaveLength(1);
	});

	it("load with bracket notation after append", async () => {
		await runCli(
			["state", "append"],
			JSON.stringify({
				hook_event_name: "UserPromptSubmit",
				prompt: "first",
				cwd: testProjectDir,
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		await runCli(
			["state", "append"],
			JSON.stringify({
				hook_event_name: "UserPromptSubmit",
				prompt: "second",
				cwd: testProjectDir,
			}),
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Load first entry via bracket notation
		const r0 = await runCli(
			["state", "load", "UserPromptSubmit[0].prompt"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(r0.exitCode).toBe(0);
		expect(r0.stdout).toBe("first");

		// Load second entry via bracket notation
		const r1 = await runCli(
			["state", "load", "UserPromptSubmit[1].prompt"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(r1.exitCode).toBe(0);
		expect(r1.stdout).toBe("second");

		// Dot notation is sugar for [0]
		const rDot = await runCli(
			["state", "load", "UserPromptSubmit.prompt"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		expect(rDot.exitCode).toBe(0);
		expect(rDot.stdout).toBe("first");
	});
});

// ===========================================================================
// 8. DEADLOCK / HANG DETECTION
// ===========================================================================

describe("deadlock detection", () => {
	it("does not hang with empty stdin (exec check)", async () => {
		// Empty stdin should not cause a hang — readHookInput should return {}
		const result = await runCli(
			["exec", "check", "tests", "--always"],
			"", // Empty stdin
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
			5_000, // 5 second timeout
		);
		// Should complete within timeout (not hang)
		// The exact exit code depends on whether scope is active
		expect([0, 2]).toContain(result.exitCode);
	});

	it("does not hang with empty stdin (state load)", async () => {
		const result = await runCli(
			["state", "load"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
			5_000,
		);
		expect(result.exitCode).toBe(0);
	});

	it("does not hang with large stdin payload", async () => {
		// Generate a large JSON payload (~100KB)
		const bigData = "x".repeat(100_000);
		const result = await runCli(
			["exec", "run", "tests", "--always"],
			JSON.stringify({
				hook_event_name: "PreToolUse",
				tool_name: "Bash",
				tool_input: { command: "echo test", data: bigData },
				cwd: testProjectDir,
				session_id: "sess-big",
			}),
			testEnv(),
			10_000,
		);
		// Should complete without hanging
		expect([0, 2]).toContain(result.exitCode);
	});

	it("does not hang with malformed stdin JSON", async () => {
		const result = await runCli(
			["exec", "run", "tests", "--always"],
			"{ this is not valid JSON !!!",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
			5_000,
		);
		// Should not hang — may fail or allow depending on error handling
		expect([0, 1, 2]).toContain(result.exitCode);
	});
});

// ===========================================================================
// 9. COMMAND TIMEOUT
// ===========================================================================

describe("command timeout", () => {
	it("kills command that exceeds timeout and reports timeout", async () => {
		const result = await runCli(
			["exec", "run", "timeout-cmd", "--always", "--timeout", "2"],
			projectEvent(),
			testEnv(),
			15_000, // CLI timeout longer than command timeout
		);
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("timed out");
	});
});

// ===========================================================================
// 10. ENABLE / DISABLE
// ===========================================================================

describe("enable/disable gating", () => {
	it("allows silently when not enabled", async () => {
		const result = await runCli(["exec", "check", "tests"], projectEvent(), {
			CHUNK_HOOK_ENABLE: "0",
			CHUNK_HOOK_SENTINELS_DIR: sentinelDir,
		});
		expect(result.exitCode).toBe(0);
	});

	it("allows when per-command disable overrides global enable", async () => {
		const result = await runCli(["exec", "check", "tests"], projectEvent(), {
			CHUNK_HOOK_ENABLE: "1",
			CHUNK_HOOK_ENABLE_TESTS: "0",
			CHUNK_HOOK_SENTINELS_DIR: sentinelDir,
		});
		expect(result.exitCode).toBe(0);
	});

	it("fires when per-command enable overrides global disable", async () => {
		// Write a failing sentinel first
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Enable only fail-cmd while global is disabled
		const result = await runCli(["exec", "check", "fail-cmd", "--always"], projectEvent(), {
			CHUNK_HOOK_ENABLE: "0",
			"CHUNK_HOOK_ENABLE_FAIL-CMD": "1",
			CHUNK_HOOK_SENTINELS_DIR: sentinelDir,
		});
		expect(result.exitCode).toBe(2);
	});
});

// ===========================================================================
// 11. COORDINATION (MULTI-COMMAND ON SAME EVENT)
// ===========================================================================

describe("multi-command coordination", () => {
	it("sentinels are consumed only when all commands pass", async () => {
		// Run both commands (both pass)
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "lint", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Check tests first
		const r1 = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(r1.exitCode).toBe(0);

		// Check lint
		const r2 = await runCli(["exec", "check", "lint", "--always"], projectEvent(), testEnv());
		expect(r2.exitCode).toBe(0);

		// After both pass, sentinels should be consumed.
		// A subsequent check should find "missing" result.
		const r3 = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(r3.exitCode).toBe(2);
		expect(r3.stderr).toContain("no results");
	});

	it("sentinel consumed when first check passes (serial execution)", async () => {
		// In serial execution, the coordination mechanism consumes sentinels eagerly:
		// when the first command registers as "pass", it's the only entry in the
		// coordination file, so "all entries pass" is true → sentinel consumed.
		//
		// This is correct: in real hook usage, all hooks fire in parallel and register
		// simultaneously, so consumption only happens when ALL are pass. But in serial,
		// the first passer consumes immediately.

		// Run one pass, one fail
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Check tests first (pass) — coordination has {tests: "pass"}, all pass → consumed
		const r1 = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(r1.exitCode).toBe(0);

		// Check fail-cmd (block) — coordination starts fresh: {"fail-cmd": "fail"}
		const r2 = await runCli(["exec", "check", "fail-cmd", "--always"], projectEvent(), testEnv());
		expect(r2.exitCode).toBe(2);

		// Tests sentinel was already consumed by r1 → now missing → block
		const r3 = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(r3.exitCode).toBe(2);
		expect(r3.stderr).toContain("no results");
	});
});

// ===========================================================================
// 12. CONCURRENT LOCK CONTENTION
// ===========================================================================

describe("concurrent lock safety", () => {
	it("multiple parallel checks do not deadlock", async () => {
		// Run both commands first
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "lint", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Fire multiple checks in parallel (simulates hooks on same event)
		const [r1, r2, r3, r4] = await Promise.all([
			runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv(), 10_000),
			runCli(["exec", "check", "lint", "--always"], projectEvent(), testEnv(), 10_000),
			runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv(), 10_000),
			runCli(["exec", "check", "lint", "--always"], projectEvent(), testEnv(), 10_000),
		]);

		// All should complete (no deadlock) and be valid exit codes
		for (const r of [r1, r2, r3, r4]) {
			expect([0, 2]).toContain(r.exitCode);
		}
	});

	it("parallel standalone checks self-consume independently", async () => {
		// Run one pass, one fail
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Fire parallel checks
		const [r1, r2] = await Promise.all([
			runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv()),
			runCli(["exec", "check", "fail-cmd", "--always"], projectEvent(), testEnv()),
		]);

		// tests should pass (and self-consume), fail-cmd should block
		expect(r1.exitCode).toBe(0);
		expect(r2.exitCode).toBe(2);

		// tests sentinel was self-consumed on pass — re-check blocks
		const r3 = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(r3.exitCode).toBe(2);
		expect(r3.stderr).toContain("no results");

		// fail-cmd sentinel is NOT consumed (only pass self-consumes)
		const r4 = await runCli(["exec", "check", "fail-cmd", "--always"], projectEvent(), testEnv());
		expect(r4.exitCode).toBe(2);
		expect(r4.stderr).toContain("FAIL");
	});

	it("three-command coordination (tests, lint, review)", async () => {
		// Run all three commands
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "lint", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		// For review, write a passing sentinel directly
		const { writeSentinel } = await import("../lib/sentinel");
		writeSentinel(sentinelDir, testProjectDir, "review", {
			status: "pass",
			details: "Test review pass",
			project: testProjectDir,
			startedAt: new Date().toISOString(),
			sessionId: "test-session-001",
		});

		// Fire all three checks in parallel
		const [r1, r2, r3] = await Promise.all([
			runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv()),
			runCli(["exec", "check", "lint", "--always"], projectEvent(), testEnv()),
			runCli(["task", "check", "review", "--always"], projectEvent(), testEnv()),
		]);

		// All should pass
		expect(r1.exitCode).toBe(0);
		expect(r2.exitCode).toBe(0);
		expect(r3.exitCode).toBe(0);

		// After all pass, sentinels should be consumed
		const r4 = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(r4.exitCode).toBe(2);
		expect(r4.stderr).toContain("no results");
	});

	it("standalone checks self-consume independently (exec pass + task fail)", async () => {
		// Run exec pass, but task review fails
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		// Write a blocking review sentinel
		const { writeSentinel } = await import("../lib/sentinel");
		writeSentinel(sentinelDir, testProjectDir, "review", {
			status: "fail",
			details: "Issues found",
			project: testProjectDir,
			startedAt: new Date().toISOString(),
			sessionId: "test-session-001",
		});

		// Check both in parallel — each self-consumes independently
		const [r1, r2] = await Promise.all([
			runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv()),
			runCli(["task", "check", "review", "--always"], projectEvent(), testEnv()),
		]);

		expect(r1.exitCode).toBe(0); // tests pass (and self-consume)
		expect(r2.exitCode).toBe(2); // review blocks (fail not consumed)

		// tests sentinel was self-consumed — re-check blocks
		const r3 = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(r3.exitCode).toBe(2);
		expect(r3.stderr).toContain("no results");
	});
});

// ===========================================================================
// 12b. SELF-CONSUMPTION TIMING
// ===========================================================================

describe("self-consumption timing", () => {
	it("exec check self-consumes immediately on pass", async () => {
		const { readSentinel } = await import("../lib/sentinel");

		// Run a passing command
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Sentinel exists before check
		expect(readSentinel(sentinelDir, testProjectDir, "tests")).not.toBe(undefined);

		// Check: passes and self-consumes
		const r1 = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(r1.exitCode).toBe(0);

		// Sentinel is gone (self-consumed on pass)
		expect(readSentinel(sentinelDir, testProjectDir, "tests")).toBe(undefined);

		// Re-check blocks with "no results"
		const r2 = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(r2.exitCode).toBe(2);
		expect(r2.stderr).toContain("no results");
	});

	it("task check self-consumes immediately on pass", async () => {
		const { readSentinel, writeSentinel } = await import("../lib/sentinel");

		// Write a passing task sentinel
		writeSentinel(sentinelDir, testProjectDir, "review", {
			status: "pass",
			details: "Review passed",
			project: testProjectDir,
			startedAt: new Date().toISOString(),
			sessionId: "test-session-001",
		});

		// Sentinel exists before check
		expect(readSentinel(sentinelDir, testProjectDir, "review")).not.toBe(undefined);

		// Check: passes and self-consumes
		const r1 = await runCli(["task", "check", "review", "--always"], projectEvent(), testEnv());
		expect(r1.exitCode).toBe(0);

		// Sentinel is gone
		expect(readSentinel(sentinelDir, testProjectDir, "review")).toBe(undefined);
	});
});

// ===========================================================================
// 13. EXEC WITH --cmd OVERRIDE
// ===========================================================================

describe("--cmd override", () => {
	it("uses override command instead of config", async () => {
		const result = await runCli(
			["exec", "run", "tests", "--always", "--cmd", "echo custom-output-marker"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(0);
	});

	it("override command failure is properly reported", async () => {
		const result = await runCli(
			["exec", "run", "tests", "--always", "--cmd", "sh -c 'echo custom-fail && exit 1'"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("custom-fail");
	});
});

// ===========================================================================
// 14. MATCHER FILTER
// ===========================================================================

describe("matcher filter", () => {
	it("allows when tool name does not match --matcher", async () => {
		// Write a failing sentinel
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Check with matcher that doesn't match the tool
		const result = await runCli(
			["exec", "check", "fail-cmd", "--always", "--matcher", "^Write$"],
			hookEvent({
				hook_event_name: "PreToolUse",
				tool_name: "Bash",
				tool_input: { command: "echo test" },
				cwd: testProjectDir,
			}),
			testEnv(),
		);
		expect(result.exitCode).toBe(0);
	});
});

// ===========================================================================
// 15. ERROR HANDLING
// ===========================================================================

describe("error handling", () => {
	it("exits 1 for unknown command", async () => {
		const result = await runCli(["unknown"], "", testEnv(), 5_000);
		expect(result.exitCode).toBe(1);
	});

	it("exits 1 for missing exec subcommand", async () => {
		const result = await runCli(["exec"], "", testEnv(), 5_000);
		expect(result.exitCode).toBe(1);
	});

	it("exits 1 for missing exec name", async () => {
		const result = await runCli(["exec", "run"], "", testEnv(), 5_000);
		expect(result.exitCode).toBe(1);
	});

	it("--help exits 0", async () => {
		const result = await runCli(["--help"], "", {}, 5_000);
		expect(result.exitCode).toBe(0);
		expect(result.stdout).toContain("hook");
	});
});

// ===========================================================================
// 16. SCOPE AUTO-ACTIVATION VIA EXEC
// ===========================================================================

describe("scope auto-activation via exec", () => {
	it("exec auto-activates scope and runs when file paths match", async () => {
		// No prior scope activation — exec should auto-activate from the event
		const result = await runCli(
			["exec", "run", "tests", "--always"],
			hookEvent({
				hook_event_name: "PreToolUse",
				tool_name: "Bash",
				tool_input: {
					command: "echo test",
					file_path: join(testProjectDir, "main.go"),
				},
				session_id: "auto-sess",
				cwd: testProjectDir,
			}),
			testEnv(),
		);
		expect(result.exitCode).toBe(0);

		// Marker should now exist
		const markerPath = join(testProjectDir, ".chunk", "hook", ".chunk-hook-active");
		expect(existsSync(markerPath)).toBe(true);
	});

	it("exec silently allows when scope is not active (different project)", async () => {
		// Event targets a different project directory
		const result = await runCli(
			["exec", "check", "tests", "--always"],
			hookEvent({
				hook_event_name: "PreToolUse",
				tool_name: "Bash",
				tool_input: { file_path: "/some/other/project/file.go" },
				session_id: "other-sess",
				cwd: testProjectDir,
			}),
			testEnv(),
		);
		// Should allow silently (scope not active for this project)
		expect(result.exitCode).toBe(0);
	});
});

// ===========================================================================
// 17. PENDING SENTINEL (COMMAND STILL RUNNING)
// ===========================================================================

describe("pending sentinel", () => {
	it("blocks with 'still running' message on pending sentinel", async () => {
		// Manually write a pending sentinel using the library
		const { writeSentinel } = await import("../lib/sentinel");
		writeSentinel(sentinelDir, testProjectDir, "tests", {
			status: "pending",
			startedAt: new Date().toISOString(),
			project: testProjectDir,
			sessionId: "test-session-001",
		});

		const result = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("still running");
	});
});

// ===========================================================================
// 18. TASK RUN SUBCOMMAND REMOVED
// ===========================================================================

describe("task run rejected", () => {
	it("exits 1 for task run (removed subcommand)", async () => {
		// `task run` was removed — the CLI should reject it with an error
		const result = await runCli(["task", "run", "review"], "", testEnv(), 5_000);
		expect(result.exitCode).toBe(1);
		// Commander.js reports unknown commands with "error: unknown command 'run'"
		expect(result.stderr).toContain("unknown command");
	});
});

// ===========================================================================
// 19. MIXED DELEGATED + DIRECT COMMANDS
// ===========================================================================

describe("mixed delegated (exec check) and direct (exec run) on same event", () => {
	it("direct exec run lint is independent of delegated exec check coordination", async () => {
		// Scenario: Stop event fires three hooks:
		//   1. exec check tests  (delegated — participates in coordination)
		//   2. exec run lint      (direct — independent, no coordination)
		//   3. task check review   (delegated — participates in coordination)
		//
		// The direct `exec run` should pass/fail on its own without waiting for
		// the delegated commands, and without affecting their coordination.

		const delayEnv = { ...testEnv(), CHUNK_HOOK_CONSUME_DELAY_MS: "100" };

		// No sentinels exist for tests or review (simulating first Stop after commit consumed them).
		// Lint runs inline and should pass independently.
		const lintResult = await runCli(
			["exec", "run", "lint", "--always"],
			projectEvent({ hook_event_name: "Stop" }),
			delayEnv,
		);
		expect(lintResult.exitCode).toBe(0); // lint passes directly

		// Delegated checks fire in parallel — both should block (missing sentinels)
		const [testsResult, reviewResult] = await Promise.all([
			runCli(
				["exec", "check", "tests", "--always"],
				projectEvent({ hook_event_name: "Stop" }),
				delayEnv,
			),
			runCli(
				["task", "check", "review", "--always"],
				projectEvent({ hook_event_name: "Stop" }),
				delayEnv,
			),
		]);

		// Both delegated checks should block with "no results" / task instructions
		expect(testsResult.exitCode).toBe(2);
		expect(testsResult.stderr).toContain("no results");
		expect(reviewResult.exitCode).toBe(2);
	});

	it("direct exec run failure blocks independently even when delegated checks pass", async () => {
		// Scenario: tests sentinel exists and passes, review sentinel exists and passes,
		// but lint (direct exec run) fails.
		//
		// The direct lint failure should block the agent, even though the
		// delegated checks would allow.

		const { writeSentinel } = await import("../lib/sentinel");

		// Write passing sentinels for both delegated commands
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		writeSentinel(sentinelDir, testProjectDir, "review", {
			status: "pass",
			details: "Review passed",
			project: testProjectDir,
			startedAt: new Date().toISOString(),
			sessionId: "test-session-001",
		});

		// Run lint with a failing command (direct)
		const lintResult = await runCli(
			["exec", "run", "lint", "--always", "--cmd", "sh -c 'echo lint-fail && exit 1'"],
			projectEvent({ hook_event_name: "Stop" }),
			testEnv(),
		);
		expect(lintResult.exitCode).toBe(2);
		expect(lintResult.stderr).toContain("lint-fail");

		// Delegated checks should still pass (unaffected by direct lint failure)
		const [testsResult, reviewResult] = await Promise.all([
			runCli(
				["exec", "check", "tests", "--always"],
				projectEvent({ hook_event_name: "Stop" }),
				testEnv(),
			),
			runCli(
				["task", "check", "review", "--always"],
				projectEvent({ hook_event_name: "Stop" }),
				testEnv(),
			),
		]);
		expect(testsResult.exitCode).toBe(0);
		expect(reviewResult.exitCode).toBe(0);
	});

	it("direct exec run does not participate in sentinel coordination/consumption", async () => {
		// Scenario: After delegated checks pass and sentinels are consumed,
		// running lint directly should not be affected (it writes + evaluates inline).
		// Conversely, a direct run should not prevent sentinel consumption.

		const { readCoordination } = await import("../lib/sentinel");

		// Write passing test sentinel (delegated)
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Run lint directly (writes sentinel + evaluates inline)
		const lintResult = await runCli(
			["exec", "run", "lint", "--always"],
			projectEvent({ hook_event_name: "Stop" }),
			testEnv(),
		);
		expect(lintResult.exitCode).toBe(0);

		// The coordination file should NOT have a "lint" entry —
		// direct exec run does not call recordAndTryConsume.
		const coord = readCoordination(sentinelDir, testProjectDir);
		expect(coord.results.lint).toBeUndefined();

		// Delegated check for tests should still work normally
		const testsResult = await runCli(
			["exec", "check", "tests", "--always"],
			projectEvent({ hook_event_name: "Stop" }),
			testEnv(),
		);
		expect(testsResult.exitCode).toBe(0);
	});

	it("full Stop cycle: sentinel consumption does not carry over direct run results", async () => {
		// Scenario simulating the user's question:
		//
		//   1. Agent commits (PreToolUse fires tests-changed + lint checks → all pass → consumed)
		//   2. Agent introduces a new bug after commit
		//   3. Agent tries to Stop → Stop fires: exec check tests, exec run lint, task check review
		//   4. Tests sentinel was consumed on commit → missing → agent must re-run tests
		//   5. Lint runs directly → passes (or fails on its own)
		//   6. Agent fixes bug, re-runs tests, they pass
		//   7. Next Stop cycle: all three pass

		// --- Phase 1: Simulate that sentinels were already consumed (post-commit) ---
		// (No sentinels exist — they were consumed after the commit passed.)

		// --- Phase 2: Stop event fires ---

		// Lint runs directly — passes on its own
		const lint1 = await runCli(
			["exec", "run", "lint", "--always"],
			projectEvent({ hook_event_name: "Stop" }),
			testEnv(),
		);
		expect(lint1.exitCode).toBe(0);

		// Check tests — no sentinel → blocks with "run it first"
		const tests1 = await runCli(
			["exec", "check", "tests", "--always"],
			projectEvent({ hook_event_name: "Stop" }),
			testEnv(),
		);
		expect(tests1.exitCode).toBe(2);
		expect(tests1.stderr).toContain("no results");

		// --- Phase 3: Agent runs tests, they fail (bug) ---
		await runCli(
			["exec", "run", "tests", "--no-check", "--always", "--cmd", "sh -c 'echo BUG && exit 1'"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		const tests2 = await runCli(
			["exec", "check", "tests", "--always"],
			projectEvent({ hook_event_name: "Stop" }),
			testEnv(),
		);
		expect(tests2.exitCode).toBe(2);
		expect(tests2.stderr).toContain("BUG");

		// --- Phase 4: Agent fixes the bug, re-runs tests → pass ---
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		const tests3 = await runCli(
			["exec", "check", "tests", "--always"],
			projectEvent({ hook_event_name: "Stop" }),
			testEnv(),
		);
		expect(tests3.exitCode).toBe(0);

		// --- Phase 5: Lint still passes on re-run (direct, no stale sentinel issues) ---
		const lint2 = await runCli(
			["exec", "run", "lint", "--always"],
			projectEvent({ hook_event_name: "Stop" }),
			testEnv(),
		);
		expect(lint2.exitCode).toBe(0);

		// --- Phase 6: Tests sentinel was consumed → next Stop starts fresh again ---
		const tests4 = await runCli(
			["exec", "check", "tests", "--always"],
			projectEvent({ hook_event_name: "Stop" }),
			testEnv(),
		);
		expect(tests4.exitCode).toBe(2);
		expect(tests4.stderr).toContain("no results");
	});
});

// ===========================================================================
// 20. AUTO-ALLOW ISOLATION
// ===========================================================================

describe("auto-allow isolation", () => {
	it("auto-allow for one command does not affect other commands", async () => {
		// Scenario: fail-cmd keeps failing and eventually auto-allows via limit.
		// review has a passing sentinel. We verify the two are independent.

		const { writeSentinel } = await import("../lib/sentinel");

		// Run a failing exec command to create a failing sentinel
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Write a passing review sentinel
		writeSentinel(sentinelDir, testProjectDir, "review", {
			status: "pass",
			details: "Review passed",
			project: testProjectDir,
			startedAt: new Date().toISOString(),
			sessionId: "test-session-001",
		});

		// Check fail-cmd 3 times (limit=2) — blocks twice then auto-allows
		const r1 = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "2"],
			projectEvent(),
			testEnv(),
		);
		expect(r1.exitCode).toBe(2); // block 1

		const r2 = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "2"],
			projectEvent(),
			testEnv(),
		);
		expect(r2.exitCode).toBe(2); // block 2

		const r3 = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "2"],
			projectEvent(),
			testEnv(),
		);
		expect(r3.exitCode).toBe(0); // auto-allow (count 3 > limit 2)

		// Review still passes (independent of fail-cmd's auto-allow)
		const r4 = await runCli(["task", "check", "review", "--always"], projectEvent(), testEnv());
		expect(r4.exitCode).toBe(0);
	});

	it("self-consumed sentinel is not affected by other command's auto-allow", async () => {
		// lint passes and self-consumes, fail-cmd auto-allows.
		// Verify lint's consumption is clean and independent.

		await runCli(
			["exec", "run", "lint", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// lint check passes (and self-consumes)
		const l1 = await runCli(["exec", "check", "lint", "--always"], projectEvent(), testEnv());
		expect(l1.exitCode).toBe(0);

		// Exhaust fail-cmd block limit (limit=1, so 2nd check auto-allows)
		const t1 = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "1"],
			projectEvent(),
			testEnv(),
		);
		expect(t1.exitCode).toBe(2); // block 1

		const t2 = await runCli(
			["exec", "check", "fail-cmd", "--always", "--limit", "1"],
			projectEvent(),
			testEnv(),
		);
		expect(t2.exitCode).toBe(0); // auto-allow

		// lint sentinel was already consumed — re-check blocks
		const l2 = await runCli(["exec", "check", "lint", "--always"], projectEvent(), testEnv());
		expect(l2.exitCode).toBe(2);
		expect(l2.stderr).toContain("no results");
	});
});

// ===========================================================================
// 21. SYNC CHECK COMMAND
// ===========================================================================

describe("sync check (grouped sequential checks)", () => {
	it("allows when all exec sentinels pass", async () => {
		// Run both execs so passing sentinels exist.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "lint", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		const result = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(0);
	});

	it("blocks when first exec sentinel is missing", async () => {
		// No sentinels exist at all → first spec blocks with run directive.
		const result = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("no results");
		expect(result.stderr).toContain("tests");
	});

	it("blocks when second exec sentinel is missing after first passes", async () => {
		// Only tests has a passing sentinel; lint is missing.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		const result = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("no results");
		// Should mention the missing lint command, not tests
		expect(result.stderr).toContain("lint");
	});

	it("blocks and resets group when an exec sentinel fails", async () => {
		// Tests passes, fail-cmd fails.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// First sync check: tests passes → cached in group, fail-cmd fails → group reset.
		const r1 = await runCli(
			["sync", "check", "exec:tests", "exec:fail-cmd", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(r1.exitCode).toBe(2);
		expect(r1.stderr).toContain("fail-cmd");

		// Re-run tests (sentinel was consumed by group). Need new sentinel.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Second sync check: tests must re-pass because group was reset.
		// fail-cmd sentinel was removed on fail, so it's now missing.
		const r2 = await runCli(
			["sync", "check", "exec:tests", "exec:fail-cmd", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(r2.exitCode).toBe(2);
		// Should block on fail-cmd being missing now (sentinel was removed)
		expect(r2.stderr).toContain("no results");
	});

	it("caches passed specs in group sentinel and skips them on re-invocation", async () => {
		// Tests passes. Lint missing.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// First call: tests passes, lint blocks (missing).
		const r1 = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(r1.exitCode).toBe(2);

		// Now run lint so its sentinel exists.
		await runCli(
			["exec", "run", "lint", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Second call: tests is cached in group sentinel (skipped), lint now passes → allow.
		// Note: tests sentinel was consumed on first call, but group sentinel remembers it.
		const r2 = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(r2.exitCode).toBe(0);
	});

	it("blocks when a sentinel is pending (still running)", async () => {
		const { writeSentinel: ws } = await import("../lib/sentinel");

		// Write a pending sentinel for tests.
		ws(sentinelDir, testProjectDir, "tests", {
			status: "pending",
			details: "",
			project: testProjectDir,
			startedAt: new Date().toISOString(),
			sessionId: "test-session-001",
		});

		const result = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("still running");
	});

	it("works with mixed exec and task specs", async () => {
		const { writeSentinel: ws } = await import("../lib/sentinel");

		// Create passing exec sentinel.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Create passing task sentinel.
		ws(sentinelDir, testProjectDir, "review", {
			status: "pass",
			details: "Review passed",
			project: testProjectDir,
			startedAt: new Date().toISOString(),
			sessionId: "test-session-001",
		});

		const result = await runCli(
			["sync", "check", "exec:tests", "task:review", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(0);
	});

	it("blocks with task instructions when task sentinel is missing", async () => {
		// Tests passes, but review has no sentinel.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		const result = await runCli(
			["sync", "check", "exec:tests", "task:review", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		// Should contain task instructions from review-instructions.md
		expect(result.stderr).toContain("review");
		expect(result.stderr).toContain("subagent");
	});

	it("group sentinel cleanup: allow removes group sentinel", async () => {
		// Both specs pass.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "lint", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Sync check allows.
		const r1 = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(r1.exitCode).toBe(0);

		// Run both again so sentinels exist for second cycle.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "lint", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Second sync check: group sentinel was cleaned up, so both are re-checked.
		const r2 = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(r2.exitCode).toBe(0);
	});

	it("exits 1 when no specs are provided", async () => {
		const result = await runCli(["sync", "check", "--always"], projectEvent(), testEnv(), 5_000);
		expect(result.exitCode).toBe(1);
	});

	it("exits 1 for invalid spec format", async () => {
		const result = await runCli(
			["sync", "check", "invalid-spec", "--always"],
			projectEvent(),
			testEnv(),
			5_000,
		);
		expect(result.exitCode).toBe(1);
	});

	it("exits 1 for unknown spec type", async () => {
		const result = await runCli(
			["sync", "check", "unknown:foo", "--always"],
			projectEvent(),
			testEnv(),
			5_000,
		);
		expect(result.exitCode).toBe(1);
	});

	it("auto-passes exec specs when no files have changed (skip-if-no-changes)", async () => {
		// Override config: execs with fileExt filter and without always: true.
		// When fileExt is set and no matching files have changed, the
		// skip-if-no-changes gate auto-passes the spec without a sentinel.
		const configPath = join(testProjectDir, ".chunk", "hook", "config.yml");
		writeFileSync(
			configPath,
			`
execs:
  tests:
    command: "echo 'all tests passed'"
    fileExt: ".go"
    timeout: 30
  lint:
    command: "echo 'lint ok'"
    fileExt: ".go"
    timeout: 10
tasks:
  review:
    instructions: ".chunk/hook/review-instructions.md"
    schema: ""
    limit: 3
`,
		);

		// Commit the config change so only the fileExt filter matters.
		Bun.spawnSync(["git", "add", "."], { cwd: testProjectDir });
		Bun.spawnSync(["git", "commit", "-m", "update config"], { cwd: testProjectDir });

		// No sentinels exist, no --always flag, and no .go files have changed.
		// The sync check should auto-pass both exec specs via the no-changes gate.
		const result = await runCli(
			["sync", "check", "exec:tests", "exec:lint"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(0);
	});

	it("blocks exec specs when matching files have changed and no sentinel exists", async () => {
		// Override config: execs with fileExt filter and without always: true.
		const configPath = join(testProjectDir, ".chunk", "hook", "config.yml");
		writeFileSync(
			configPath,
			`
execs:
  tests:
    command: "echo 'all tests passed'"
    fileExt: ".go"
    timeout: 30
  lint:
    command: "echo 'lint ok'"
    fileExt: ".go"
    timeout: 10
tasks:
  review:
    instructions: ".chunk/hook/review-instructions.md"
    schema: ""
    limit: 3
`,
		);

		// Commit the config change, then create an uncommitted .go file.
		Bun.spawnSync(["git", "add", "."], { cwd: testProjectDir });
		Bun.spawnSync(["git", "commit", "-m", "update config"], { cwd: testProjectDir });
		writeFileSync(join(testProjectDir, "main.go"), "package main\n");

		// No sentinels exist, no --always flag, and a .go file HAS changed.
		// The sync check should block because a sentinel is required.
		const result = await runCli(
			["sync", "check", "exec:tests", "exec:lint"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("no results");
	});

	it("auto-passes only unchanged exec specs in a mixed group", async () => {
		// Override config: tests filters by .go, lint filters by .ts.
		// We create a .ts file change → lint has changes but tests does not.
		const configPath = join(testProjectDir, ".chunk", "hook", "config.yml");
		writeFileSync(
			configPath,
			`
execs:
  tests:
    command: "echo 'all tests passed'"
    fileExt: ".go"
    timeout: 30
  lint:
    command: "echo 'lint ok'"
    fileExt: ".ts"
    timeout: 10
tasks:
  review:
    instructions: ".chunk/hook/review-instructions.md"
    schema: ""
    limit: 3
`,
		);

		// Commit the config change, then create an uncommitted .ts file.
		Bun.spawnSync(["git", "add", "."], { cwd: testProjectDir });
		Bun.spawnSync(["git", "commit", "-m", "update config"], { cwd: testProjectDir });
		writeFileSync(join(testProjectDir, "change.ts"), "export const y = 2;\n");

		// No sentinels, no --always flag.
		// tests should auto-pass (no .go changes), lint should block (.ts changed, no sentinel).
		const result = await runCli(
			["sync", "check", "exec:tests", "exec:lint"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		// Should block on lint, not tests
		expect(result.stderr).toContain("lint");
	});
});

// ===========================================================================
// 22. SELF-CONSUMING CHECKS
// ===========================================================================

describe("self-consuming exec/task check", () => {
	it("exec check consumes sentinel after pass (not available for second check)", async () => {
		// Run tests to create a passing sentinel.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// First check: passes and self-consumes.
		const r1 = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(r1.exitCode).toBe(0);

		// Second check: sentinel was consumed → blocks with "no results".
		const r2 = await runCli(["exec", "check", "tests", "--always"], projectEvent(), testEnv());
		expect(r2.exitCode).toBe(2);
		expect(r2.stderr).toContain("no results");
	});

	it("task check consumes sentinel after pass (not available for second check)", async () => {
		const { writeSentinel: ws } = await import("../lib/sentinel");

		// Write a passing task sentinel.
		ws(sentinelDir, testProjectDir, "review", {
			status: "pass",
			details: "Review passed",
			project: testProjectDir,
			startedAt: new Date().toISOString(),
			sessionId: "test-session-001",
		});

		// First check: passes and self-consumes.
		const r1 = await runCli(["task", "check", "review", "--always"], projectEvent(), testEnv());
		expect(r1.exitCode).toBe(0);

		// Second check: sentinel was consumed → blocks.
		const r2 = await runCli(["task", "check", "review", "--always"], projectEvent(), testEnv());
		expect(r2.exitCode).toBe(2);
	});

	it("exec check does NOT consume sentinel on fail", async () => {
		// Run failing command to create a failing sentinel.
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// First check: blocks (fail).
		const r1 = await runCli(["exec", "check", "fail-cmd", "--always"], projectEvent(), testEnv());
		expect(r1.exitCode).toBe(2);
		expect(r1.stderr).toContain("FAIL");

		// Second check: sentinel still exists → still blocks with the failure.
		const r2 = await runCli(["exec", "check", "fail-cmd", "--always"], projectEvent(), testEnv());
		expect(r2.exitCode).toBe(2);
		expect(r2.stderr).toContain("FAIL");
	});
});

// ===========================================================================
// 23. SYNC --on-fail MODES
// ===========================================================================

describe("sync --on-fail modes", () => {
	it("--on-fail retry preserves passed specs when a later spec fails", async () => {
		// Tests passes, fail-cmd fails.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Sync check with --on-fail retry: tests passes (cached), fail-cmd fails.
		// Retry mode should keep "tests" in the group sentinel.
		const r1 = await runCli(
			["sync", "check", "exec:tests", "exec:fail-cmd", "--always", "--on-fail", "retry", "--bail"],
			projectEvent(),
			testEnv(),
		);
		expect(r1.exitCode).toBe(2);
		expect(r1.stderr).toContain("fail-cmd");

		// DON'T re-run tests — only fix fail-cmd. Override config to make it pass.
		const configPath = join(testProjectDir, ".chunk", "hook", "config.yml");
		writeFileSync(
			configPath,
			`
execs:
  tests:
    command: "echo 'all tests passed'"
    always: true
    timeout: 30
  fail-cmd:
    command: "echo 'now passing'"
    always: true
    timeout: 30
  timeout-cmd:
    command: "sleep 999"
    always: true
    timeout: 2
  lint:
    command: "echo 'lint ok'"
    always: true
    timeout: 10
tasks:
  review:
    instructions: ".chunk/hook/review-instructions.md"
    schema: ""
    limit: 3
    always: true
triggers:
  pre-commit:
    - "git commit"
    - "git push"
`,
		);

		// Run the now-passing fail-cmd so its sentinel exists.
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Second sync check: tests should be cached (from retry mode), fail-cmd now passes → allow.
		const r2 = await runCli(
			["sync", "check", "exec:tests", "exec:fail-cmd", "--always", "--on-fail", "retry", "--bail"],
			projectEvent(),
			testEnv(),
		);
		expect(r2.exitCode).toBe(0);
	});

	it("--on-fail restart (explicit) resets all specs on failure", async () => {
		// Tests passes, fail-cmd fails.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Sync with explicit --on-fail restart + --bail: tests passes, fail-cmd fails → group reset.
		const r1 = await runCli(
			[
				"sync",
				"check",
				"exec:tests",
				"exec:fail-cmd",
				"--always",
				"--on-fail",
				"restart",
				"--bail",
			],
			projectEvent(),
			testEnv(),
		);
		expect(r1.exitCode).toBe(2);

		// Re-run tests (sentinel was consumed, group was reset).
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Second sync check: since group was reset, tests must re-pass even though
		// it already passed before. fail-cmd sentinel was removed, so it should block
		// on fail-cmd being missing.
		const r2 = await runCli(
			[
				"sync",
				"check",
				"exec:tests",
				"exec:fail-cmd",
				"--always",
				"--on-fail",
				"restart",
				"--bail",
			],
			projectEvent(),
			testEnv(),
		);
		expect(r2.exitCode).toBe(2);
		// Should block on fail-cmd being missing (not on tests)
		expect(r2.stderr).toContain("no results");
	});

	it("default on-fail is restart (no --on-fail flag)", async () => {
		// Same as restart: tests passes, fail-cmd fails → group fully reset.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Sync without --on-fail flag: should use restart behavior.
		const r1 = await runCli(
			["sync", "check", "exec:tests", "exec:fail-cmd", "--always", "--bail"],
			projectEvent(),
			testEnv(),
		);
		expect(r1.exitCode).toBe(2);

		// Re-run only tests (fail-cmd sentinel removed on fail).
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Tests must re-pass (group was fully reset), then blocks on missing fail-cmd.
		const r2 = await runCli(
			["sync", "check", "exec:tests", "exec:fail-cmd", "--always", "--bail"],
			projectEvent(),
			testEnv(),
		);
		expect(r2.exitCode).toBe(2);
		expect(r2.stderr).toContain("no results");
	});

	it("--on-fail retry with three specs preserves first passed spec when second fails", async () => {
		// Three specs: tests (pass), lint (pass), fail-cmd (fail).
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "lint", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Sync with retry: tests + lint pass → cached, fail-cmd fails.
		const r1 = await runCli(
			[
				"sync",
				"check",
				"exec:tests",
				"exec:lint",
				"exec:fail-cmd",
				"--always",
				"--on-fail",
				"retry",
				"--bail",
			],
			projectEvent(),
			testEnv(),
		);
		expect(r1.exitCode).toBe(2);
		expect(r1.stderr).toContain("fail-cmd");

		// Don't re-run tests or lint. Override fail-cmd to pass.
		const configPath = join(testProjectDir, ".chunk", "hook", "config.yml");
		writeFileSync(
			configPath,
			`
execs:
  tests:
    command: "echo 'all tests passed'"
    always: true
    timeout: 30
  fail-cmd:
    command: "echo 'now passing'"
    always: true
    timeout: 30
  timeout-cmd:
    command: "sleep 999"
    always: true
    timeout: 2
  lint:
    command: "echo 'lint ok'"
    always: true
    timeout: 10
tasks:
  review:
    instructions: ".chunk/hook/review-instructions.md"
    schema: ""
    limit: 3
    always: true
triggers:
  pre-commit:
    - "git commit"
    - "git push"
`,
		);

		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Second sync: tests + lint should still be cached, fail-cmd now passes → allow.
		const r2 = await runCli(
			[
				"sync",
				"check",
				"exec:tests",
				"exec:lint",
				"exec:fail-cmd",
				"--always",
				"--on-fail",
				"retry",
				"--bail",
			],
			projectEvent(),
			testEnv(),
		);
		expect(r2.exitCode).toBe(0);
	});
});

// ===========================================================================
// 24. SYNC --bail vs DEFAULT (EVALUATE ALL)
// ===========================================================================

describe("sync --bail vs default evaluate-all", () => {
	it("default (no --bail): evaluates all specs and reports combined issues", async () => {
		// No sentinels exist — both tests and lint are missing.
		const result = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		// Default behavior evaluates all specs, so the block message should
		// mention BOTH tests and lint, not just the first one.
		expect(result.stderr).toContain("tests");
		expect(result.stderr).toContain("lint");
		// Should mention multiple issues
		expect(result.stderr).toContain("issue");
	});

	it("--bail: stops at first missing spec", async () => {
		// No sentinels — both missing.
		const result = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "--always", "--bail"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		// Bail mode stops at first missing (tests).
		expect(result.stderr).toContain('Exec "tests" has no results');
		expect(result.stderr).not.toContain('Exec "lint" has no results');
	});

	it("default: reports combined failure + missing in one message", async () => {
		// Create a failing sentinel for tests, leave lint missing.
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Sync with fail-cmd first (fails) and lint second (missing).
		const result = await runCli(
			["sync", "check", "exec:fail-cmd", "exec:lint", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		// Should mention both issues: the failure and the missing spec.
		expect(result.stderr).toContain("fail-cmd");
		expect(result.stderr).toContain("lint");
	});

	it("--bail: stops at failure without evaluating remaining specs", async () => {
		// Create a failing sentinel for fail-cmd.
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		const result = await runCli(
			["sync", "check", "exec:fail-cmd", "exec:lint", "--always", "--bail"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		// Bail stops at fail-cmd.
		expect(result.stderr).toContain('Exec "fail-cmd" failed');
		expect(result.stderr).not.toContain('Exec "lint"');
	});

	it("default: first spec passes, second and third both missing → combined message", async () => {
		// Only tests has a passing sentinel.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		const result = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "exec:fail-cmd", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		// tests passes, both lint and fail-cmd are missing → combined message.
		expect(result.stderr).toContain("lint");
		expect(result.stderr).toContain("fail-cmd");
	});

	it("default: all specs pass even without --bail", async () => {
		// All sentinels present and passing.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "lint", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		const result = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(0);
	});

	it("default with --on-fail retry: collects failure and preserves passed", async () => {
		// tests pass, fail-cmd fails, lint is missing.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// No --bail → evaluates all specs; --on-fail retry → preserves tests in group.
		const r1 = await runCli(
			[
				"sync",
				"check",
				"exec:tests",
				"exec:fail-cmd",
				"exec:lint",
				"--always",
				"--on-fail",
				"retry",
			],
			projectEvent(),
			testEnv(),
		);
		expect(r1.exitCode).toBe(2);
		// Should mention both fail-cmd (fail) and lint (missing).
		expect(r1.stderr).toContain("fail-cmd");
		expect(r1.stderr).toContain("lint");

		// Override fail-cmd to pass and run both fail-cmd and lint.
		const configPath = join(testProjectDir, ".chunk", "hook", "config.yml");
		writeFileSync(
			configPath,
			`
execs:
  tests:
    command: "echo 'all tests passed'"
    always: true
    timeout: 30
  fail-cmd:
    command: "echo 'now passing'"
    always: true
    timeout: 30
  timeout-cmd:
    command: "sleep 999"
    always: true
    timeout: 2
  lint:
    command: "echo 'lint ok'"
    always: true
    timeout: 10
tasks:
  review:
    instructions: ".chunk/hook/review-instructions.md"
    schema: ""
    limit: 3
    always: true
triggers:
  pre-commit:
    - "git commit"
    - "git push"
`,
		);

		await runCli(
			["exec", "run", "fail-cmd", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);
		await runCli(
			["exec", "run", "lint", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// tests is still cached (retry mode), fail-cmd + lint now pass → allow.
		const r2 = await runCli(
			[
				"sync",
				"check",
				"exec:tests",
				"exec:fail-cmd",
				"exec:lint",
				"--always",
				"--on-fail",
				"retry",
			],
			projectEvent(),
			testEnv(),
		);
		expect(r2.exitCode).toBe(0);
	});
});

// ===========================================================================
// 25. SESSION-AWARE STALENESS
// ===========================================================================

describe("sync session-aware staleness", () => {
	it("sentinel from different session is treated as missing", async () => {
		const { writeSentinel: ws } = await import("../lib/sentinel");

		// Write a passing sentinel with a DIFFERENT session ID.
		ws(sentinelDir, testProjectDir, "tests", {
			status: "pass",
			details: "all tests passed",
			project: testProjectDir,
			startedAt: new Date().toISOString(),
			sessionId: "old-session-000",
		});

		// Write a scope marker with the CURRENT session ID.
		const markerDir = join(testProjectDir, ".chunk", "hook");
		writeFileSync(
			join(markerDir, ".chunk-hook-active"),
			`${JSON.stringify({ sessionId: "current-session-001", timestamp: Date.now() })}\n`,
		);

		// Sync check: sentinel has sessionId "old-session-000" but marker says
		// "current-session-001" → sentinel is stale → treated as missing.
		const result = await runCli(
			["sync", "check", "exec:tests", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("no results");
	});

	it("sentinel from same session is treated as current (passes)", async () => {
		const { readSentinel: rs } = await import("../lib/sentinel");

		const sessionId = "current-session-001";

		// Write the scope marker FIRST — the marker must exist before exec run
		// and before sync check so the session ID is available.
		const markerDir = join(testProjectDir, ".chunk", "hook");
		writeFileSync(
			join(markerDir, ".chunk-hook-active"),
			`${JSON.stringify({ sessionId, timestamp: Date.now() })}\n`,
		);

		// Use exec run in a subprocess to create a sentinel with a correct
		// fingerprint (immune to module-level mocks in parallel test files).
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Read the sentinel created by exec run and verify it has the right sessionId.
		const sentinel = rs(sentinelDir, testProjectDir, "tests");
		expect(sentinel).toBeDefined();
		expect(sentinel?.sessionId).toBe(sessionId);

		// Sync check: sentinel and marker share the same sessionId → valid → passes.
		const result = await runCli(
			["sync", "check", "exec:tests", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(0);
	});

	it("sentinel without sessionId is rejected when session is active", async () => {
		const { writeSentinel: ws, readSentinel: rs } = await import("../lib/sentinel");

		// Write the scope marker first so exec run picks up the session.
		const markerDir = join(testProjectDir, ".chunk", "hook");
		writeFileSync(
			join(markerDir, ".chunk-hook-active"),
			`${JSON.stringify({ sessionId: "current-session-001", timestamp: Date.now() })}\n`,
		);

		// Use exec run to create a sentinel with correct fingerprint.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Read the sentinel's contentHash, then rewrite without sessionId.
		const original = rs(sentinelDir, testProjectDir, "tests");
		expect(original).toBeDefined();
		ws(sentinelDir, testProjectDir, "tests", {
			status: "pass",
			details: "all tests passed",
			project: testProjectDir,
			startedAt: new Date().toISOString(),
			contentHash: original?.contentHash,
			// no sessionId field — sentinel is treated as stale
		});

		// Sentinel has no sessionId → treated as stale → sync blocks (exit 2).
		const result = await runCli(
			["sync", "check", "exec:tests", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
	});

	it("exec run writes sessionId into sentinel, checked by sync", async () => {
		const { readSentinel: rs } = await import("../lib/sentinel");

		// exec run should pick up the marker's sessionId and write it into
		// the sentinel.
		await runCli(
			["exec", "run", "tests", "--no-check", "--always"],
			"",
			testEnv({ CLAUDE_PROJECT_DIR: testProjectDir }),
		);

		// Read the sentinel and verify it is present and passes.
		const sentinel = rs(sentinelDir, testProjectDir, "tests");
		expect(sentinel).toBeDefined();
		expect(sentinel?.status).toBe("pass");

		// Sync check in the same session → should pass.
		const result = await runCli(
			["sync", "check", "exec:tests", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(0);
	});

	it("stale sentinel in multi-spec group blocks that spec", async () => {
		const { writeSentinel: ws } = await import("../lib/sentinel");

		const currentSession = "session-current";
		const staleSession = "session-old";

		// Write passing sentinel for tests with CURRENT session.
		ws(sentinelDir, testProjectDir, "tests", {
			status: "pass",
			details: "all tests passed",
			project: testProjectDir,
			startedAt: new Date().toISOString(),
			sessionId: currentSession,
		});

		// Write passing sentinel for lint with STALE session.
		ws(sentinelDir, testProjectDir, "lint", {
			status: "pass",
			details: "lint ok",
			project: testProjectDir,
			startedAt: new Date().toISOString(),
			sessionId: staleSession,
		});

		// Write scope marker with current session.
		const markerDir = join(testProjectDir, ".chunk", "hook");
		writeFileSync(
			join(markerDir, ".chunk-hook-active"),
			`${JSON.stringify({ sessionId: currentSession, timestamp: Date.now() })}\n`,
		);

		// Sync: tests sentinel is current → passes; lint sentinel is stale → missing.
		const result = await runCli(
			["sync", "check", "exec:tests", "exec:lint", "--always"],
			projectEvent(),
			testEnv(),
		);
		expect(result.exitCode).toBe(2);
		expect(result.stderr).toContain("lint");
	});
});
