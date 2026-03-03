import { afterEach, describe, expect, it } from "bun:test";
import {
	env,
	envBool,
	getEnvSentinelDir,
	getEnvTimeout,
	getProjectDir,
	isEnabled,
	isGloballyEnabled,
} from "../lib/env";

describe("env helpers", () => {
	const saved: Record<string, string | undefined> = {};

	function setEnv(key: string, val: string | undefined) {
		saved[key] = process.env[key];
		if (val === undefined) delete process.env[key];
		else process.env[key] = val;
	}

	afterEach(() => {
		for (const [k, v] of Object.entries(saved)) {
			if (v === undefined) delete process.env[k];
			else process.env[k] = v;
		}
	});

	describe("env()", () => {
		it("returns the value when set", () => {
			setEnv("CHUNK_HOOK_TEST_VAR", "hello");
			expect(env("CHUNK_HOOK_TEST_VAR")).toBe("hello");
		});
		it("returns fallback when unset", () => {
			setEnv("CHUNK_HOOK_TEST_VAR", undefined);
			expect(env("CHUNK_HOOK_TEST_VAR", "default")).toBe("default");
		});
		it("returns fallback for empty string", () => {
			setEnv("CHUNK_HOOK_TEST_VAR", "");
			expect(env("CHUNK_HOOK_TEST_VAR", "fallback")).toBe("fallback");
		});
	});

	describe("envBool()", () => {
		it("returns true for '1'", () => {
			setEnv("CHUNK_HOOK_BOOL", "1");
			expect(envBool("CHUNK_HOOK_BOOL")).toBe(true);
		});
		it("returns true for 'true'", () => {
			setEnv("CHUNK_HOOK_BOOL", "true");
			expect(envBool("CHUNK_HOOK_BOOL")).toBe(true);
		});
		it("returns true for 'YES' (case-insensitive)", () => {
			setEnv("CHUNK_HOOK_BOOL", "YES");
			expect(envBool("CHUNK_HOOK_BOOL")).toBe(true);
		});
		it("returns false for other values", () => {
			setEnv("CHUNK_HOOK_BOOL", "no");
			expect(envBool("CHUNK_HOOK_BOOL")).toBe(false);
		});
		it("returns undefined when unset", () => {
			setEnv("CHUNK_HOOK_BOOL", undefined);
			expect(envBool("CHUNK_HOOK_BOOL")).toBe(undefined);
		});
	});

	describe("isGloballyEnabled()", () => {
		it("returns false when CHUNK_HOOK_ENABLE is unset", () => {
			setEnv("CHUNK_HOOK_ENABLE", undefined);
			expect(isGloballyEnabled()).toBe(false);
		});
		it("returns true when CHUNK_HOOK_ENABLE=1", () => {
			setEnv("CHUNK_HOOK_ENABLE", "1");
			expect(isGloballyEnabled()).toBe(true);
		});
	});

	describe("isEnabled()", () => {
		it("falls back to global enable", () => {
			setEnv("CHUNK_HOOK_ENABLE", "1");
			setEnv("CHUNK_HOOK_ENABLE_TEST", undefined);
			expect(isEnabled("test")).toBe(true);
		});
		it("per-command override takes precedence", () => {
			setEnv("CHUNK_HOOK_ENABLE", "1");
			setEnv("CHUNK_HOOK_ENABLE_TEST", "0");
			expect(isEnabled("test")).toBe(false);
		});
		it("returns false when nothing is set", () => {
			setEnv("CHUNK_HOOK_ENABLE", undefined);
			setEnv("CHUNK_HOOK_ENABLE_LINT", undefined);
			expect(isEnabled("lint")).toBe(false);
		});
	});

	describe("getEnvTimeout()", () => {
		it("returns undefined when unset", () => {
			setEnv("CHUNK_HOOK_TIMEOUT_TEST", undefined);
			expect(getEnvTimeout("test")).toBe(undefined);
		});
		it("parses numeric timeout", () => {
			setEnv("CHUNK_HOOK_TIMEOUT_LINT", "30");
			expect(getEnvTimeout("lint")).toBe(30);
		});
		it("rejects non-positive values", () => {
			setEnv("CHUNK_HOOK_TIMEOUT_TEST", "-5");
			expect(getEnvTimeout("test")).toBe(undefined);
		});
	});

	describe("getEnvSentinelDir()", () => {
		it("returns override when set", () => {
			setEnv("CHUNK_HOOK_SENTINELS_DIR", "/custom/path");
			expect(getEnvSentinelDir()).toBe("/custom/path");
		});
	});

	describe("getProjectDir()", () => {
		it("returns CLAUDE_PROJECT_DIR when set", () => {
			setEnv("CLAUDE_PROJECT_DIR", "/my/project");
			expect(getProjectDir()).toBe("/my/project");
		});
		it("falls back to cwd when unset", () => {
			setEnv("CLAUDE_PROJECT_DIR", undefined);
			expect(getProjectDir()).toBe(process.cwd());
		});
	});
});
