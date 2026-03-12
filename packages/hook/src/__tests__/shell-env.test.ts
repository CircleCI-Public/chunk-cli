import { afterEach, beforeEach, describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { buildEnvUpdateOptions, migrateEnvFile, runEnvUpdate } from "../commands/env-update";
import {
	defaultEnvFile,
	defaultLogDir,
	defaultShellStartupFiles,
	generateEnvContent,
	legacyEnvFile,
	PROFILES,
	upsertManagedBlock,
} from "../lib/shell-env";

describe("shell-env", () => {
	const testDir = join(tmpdir(), "chunk-hook-test-shell-env", String(Date.now()));
	const saved: Record<string, string | undefined> = {};

	function setEnv(key: string, val: string | undefined) {
		saved[key] = process.env[key];
		if (val === undefined) delete process.env[key];
		else process.env[key] = val;
	}

	beforeEach(() => {
		mkdirSync(testDir, { recursive: true });
	});

	afterEach(() => {
		rmSync(testDir, { recursive: true, force: true });
		for (const [k, v] of Object.entries(saved)) {
			if (v === undefined) delete process.env[k];
			else process.env[k] = v;
		}
	});

	// -----------------------------------------------------------------------
	// generateEnvContent
	// -----------------------------------------------------------------------

	describe("generateEnvContent", () => {
		it("generates disable profile", () => {
			const content = generateEnvContent({
				profile: "disable",
				logDir: "/tmp/logs",
				verbose: false,
				envFile: "/tmp/env",
			});
			expect(content).toContain("export CHUNK_HOOK_ENABLE=0");
			expect(content).toContain("Profile: disable");
			// Should not have per-command enables as exports (they appear in header comments)
			expect(content).not.toContain("export CHUNK_HOOK_ENABLE_TESTS");
		});

		it("generates enable profile", () => {
			const content = generateEnvContent({
				profile: "enable",
				logDir: "/tmp/logs",
				verbose: false,
				envFile: "/tmp/env",
			});
			expect(content).toContain("export CHUNK_HOOK_ENABLE=1");
			expect(content).toContain("Profile: enable");
		});

		it("generates tests-lint profile", () => {
			const content = generateEnvContent({
				profile: "tests-lint",
				logDir: "/tmp/logs",
				verbose: false,
				envFile: "/tmp/env",
			});
			expect(content).toContain("export CHUNK_HOOK_ENABLE=0");
			expect(content).toContain("export CHUNK_HOOK_ENABLE_TESTS=1");
			expect(content).toContain("export CHUNK_HOOK_ENABLE_TESTS_CHANGED=1");
			expect(content).toContain("export CHUNK_HOOK_ENABLE_LINT=1");
		});

		it("generates review profile", () => {
			const content = generateEnvContent({
				profile: "review",
				logDir: "/tmp/logs",
				verbose: false,
				envFile: "/tmp/env",
			});
			expect(content).toContain("export CHUNK_HOOK_ENABLE=0");
			expect(content).toContain("export CHUNK_HOOK_ENABLE_REVIEW=1");
		});

		it("includes log dir", () => {
			const content = generateEnvContent({
				profile: "enable",
				logDir: "/custom/logs",
				verbose: false,
				envFile: "/tmp/env",
			});
			expect(content).toContain("export CHUNK_HOOK_LOG_DIR='/custom/logs'");
		});

		it("includes verbose when enabled", () => {
			const content = generateEnvContent({
				profile: "enable",
				logDir: "/tmp/logs",
				verbose: true,
				envFile: "/tmp/env",
			});
			expect(content).toContain("export CHUNK_HOOK_VERBOSE=1");
		});

		it("comments out verbose when disabled", () => {
			const content = generateEnvContent({
				profile: "enable",
				logDir: "/tmp/logs",
				verbose: false,
				envFile: "/tmp/env",
			});
			expect(content).toContain("# export CHUNK_HOOK_VERBOSE=1");
		});

		it("includes project root when set", () => {
			const content = generateEnvContent({
				profile: "enable",
				logDir: "/tmp/logs",
				verbose: false,
				projectRoot: "/home/user/workspace",
				envFile: "/tmp/env",
			});
			expect(content).toContain("export CHUNK_HOOK_PROJECT_ROOT='/home/user/workspace'");
		});

		it("comments out project root when not set", () => {
			const content = generateEnvContent({
				profile: "enable",
				logDir: "/tmp/logs",
				verbose: false,
				envFile: "/tmp/env",
			});
			expect(content).toContain("# export CHUNK_HOOK_PROJECT_ROOT=");
		});
	});

	// -----------------------------------------------------------------------
	// upsertManagedBlock
	// -----------------------------------------------------------------------

	describe("upsertManagedBlock", () => {
		it("appends block to existing file", () => {
			const file = join(testDir, "startup");
			writeFileSync(file, "# existing content\nexport FOO=1\n");

			upsertManagedBlock(file, "# marker", "value line");

			const content = readFileSync(file, "utf-8");
			expect(content).toContain("# marker\nvalue line");
			expect(content).toContain("# existing content");
		});

		it("creates file if it does not exist", () => {
			const file = join(testDir, "new-file");

			upsertManagedBlock(file, "# marker", "value line");

			expect(existsSync(file)).toBe(true);
			const content = readFileSync(file, "utf-8");
			expect(content).toContain("# marker\nvalue line");
		});

		it("updates existing block in place", () => {
			const file = join(testDir, "startup");
			writeFileSync(file, "before\n# marker\nold value\nafter\n");

			upsertManagedBlock(file, "# marker", "new value");

			const content = readFileSync(file, "utf-8");
			expect(content).toContain("# marker\nnew value");
			expect(content).not.toContain("old value");
			expect(content).toContain("before");
			expect(content).toContain("after");
		});

		it("is idempotent", () => {
			const file = join(testDir, "startup");
			writeFileSync(file, "existing\n");

			upsertManagedBlock(file, "# marker", "value");
			const first = readFileSync(file, "utf-8");

			upsertManagedBlock(file, "# marker", "value");
			const second = readFileSync(file, "utf-8");

			expect(first).toBe(second);
		});

		it("handles multiple different markers", () => {
			const file = join(testDir, "startup");
			writeFileSync(file, "");

			upsertManagedBlock(file, "# marker-a", "value-a");
			upsertManagedBlock(file, "# marker-b", "value-b");

			const content = readFileSync(file, "utf-8");
			expect(content).toContain("# marker-a\nvalue-a");
			expect(content).toContain("# marker-b\nvalue-b");
		});
	});

	// -----------------------------------------------------------------------
	// defaultShellStartupFiles
	// -----------------------------------------------------------------------

	describe("defaultShellStartupFiles", () => {
		it("returns files for current shell", () => {
			const files = defaultShellStartupFiles();
			expect(files.length).toBeGreaterThan(0);
			for (const f of files) {
				expect(f).toContain(process.env.HOME ?? "/home");
			}
		});

		it("returns zsh files for zsh shell", () => {
			setEnv("SHELL", "/bin/zsh");
			const files = defaultShellStartupFiles();
			expect(files.some((f) => f.endsWith(".zprofile"))).toBe(true);
			expect(files.some((f) => f.endsWith(".zshrc"))).toBe(true);
		});

		it("returns bash files for bash shell", () => {
			setEnv("SHELL", "/bin/bash");
			const files = defaultShellStartupFiles();
			expect(files.some((f) => f.endsWith(".bashrc"))).toBe(true);
		});
	});

	// -----------------------------------------------------------------------
	// PROFILES
	// -----------------------------------------------------------------------

	describe("PROFILES", () => {
		it("contains all four profiles", () => {
			expect(PROFILES).toEqual(["disable", "enable", "tests-lint", "review"]);
		});
	});

	// -----------------------------------------------------------------------
	// defaultEnvFile / defaultLogDir
	// -----------------------------------------------------------------------

	describe("default paths", () => {
		it("defaultEnvFile returns a path under .config/chunk/hook", () => {
			const envFile = defaultEnvFile();
			expect(envFile).toContain(".config/chunk/hook/env");
		});

		it("defaultEnvFile respects XDG_CONFIG_HOME", () => {
			setEnv("XDG_CONFIG_HOME", "/custom/config");
			const envFile = defaultEnvFile();
			expect(envFile).toBe("/custom/config/chunk/hook/env");
		});

		it("legacyEnvFile returns the old chunk-hook path", () => {
			const legacy = legacyEnvFile();
			expect(legacy).toContain(".config/chunk-hook/env");
		});

		it("defaultLogDir returns a platform-appropriate path", () => {
			const logDir = defaultLogDir();
			expect(logDir).toContain("chunk-hook");
		});
	});
});

describe("env-update", () => {
	const testDir = join(tmpdir(), "chunk-hook-test-env-update", String(Date.now()));

	beforeEach(() => {
		mkdirSync(testDir, { recursive: true });
	});

	afterEach(() => {
		rmSync(testDir, { recursive: true, force: true });
	});

	// -----------------------------------------------------------------------
	// buildEnvUpdateOptions
	// -----------------------------------------------------------------------

	describe("buildEnvUpdateOptions", () => {
		it("uses defaults when no flags provided", () => {
			const opts = buildEnvUpdateOptions({});
			expect(opts.profile).toBe("enable");
			expect(opts.verbose).toBe(false);
			expect(opts.envFile).toContain("chunk/hook");
			expect(opts.logDir).toContain("chunk-hook");
		});

		it("applies flag overrides", () => {
			const opts = buildEnvUpdateOptions({
				profile: "review",
				envFile: "/custom/env",
				logDir: "/custom/logs",
				verbose: true,
				projectRoot: "/workspace",
			});
			expect(opts.profile).toBe("review");
			expect(opts.envFile).toBe("/custom/env");
			expect(opts.logDir).toBe("/custom/logs");
			expect(opts.verbose).toBe(true);
			expect(opts.projectRoot).toBe("/workspace");
		});

		it("throws on unknown profile", () => {
			expect(() => buildEnvUpdateOptions({ profile: "foo" })).toThrow('Unknown profile: "foo"');
		});
	});

	// -----------------------------------------------------------------------
	// runEnvUpdate
	// -----------------------------------------------------------------------

	describe("runEnvUpdate", () => {
		it("creates ENV file with profile content", () => {
			const envFile = join(testDir, "env");
			const logDir = join(testDir, "logs");
			const fakeStartup = [join(testDir, ".zprofile"), join(testDir, ".zshrc")];

			const result = runEnvUpdate({
				profile: "tests-lint",
				envFile,
				logDir,
				verbose: false,
				startupFiles: fakeStartup,
			});

			expect(result.profile).toBe("tests-lint");
			expect(result.envFile).toBe(envFile);
			expect(result.logDir).toBe(logDir);
			expect(result.overwritten).toBe(false);

			const content = readFileSync(envFile, "utf-8");
			expect(content).toContain("export CHUNK_HOOK_ENABLE=0");
			expect(content).toContain("export CHUNK_HOOK_ENABLE_TESTS=1");

			// Verify startup files were written in the sandbox, not real home
			for (const f of fakeStartup) {
				expect(existsSync(f)).toBe(true);
				expect(readFileSync(f, "utf-8")).toContain(envFile);
			}
		});

		it("reports overwritten when ENV file already exists", () => {
			const envFile = join(testDir, "env");
			writeFileSync(envFile, "old content\n");

			const result = runEnvUpdate({
				profile: "enable",
				envFile,
				logDir: join(testDir, "logs"),
				verbose: false,
				startupFiles: [join(testDir, ".zprofile")],
			});

			expect(result.overwritten).toBe(true);
		});

		it("creates log directory", () => {
			const logDir = join(testDir, "nested", "logs");
			const envFile = join(testDir, "env");

			runEnvUpdate({
				profile: "enable",
				envFile,
				logDir,
				verbose: false,
				startupFiles: [join(testDir, ".zprofile")],
			});

			expect(existsSync(logDir)).toBe(true);
		});

		it("includes project root in ENV when provided", () => {
			const envFile = join(testDir, "env");

			runEnvUpdate({
				profile: "enable",
				envFile,
				logDir: join(testDir, "logs"),
				verbose: false,
				projectRoot: "/home/user/workspace",
				startupFiles: [join(testDir, ".zprofile")],
			});

			const content = readFileSync(envFile, "utf-8");
			expect(content).toContain("export CHUNK_HOOK_PROJECT_ROOT='/home/user/workspace'");
		});

		it("migrates legacy env file to new location", () => {
			const legacyDir = join(testDir, "legacy-config", "chunk-hook");
			const legacyFile = join(legacyDir, "env");
			mkdirSync(legacyDir, { recursive: true });
			writeFileSync(legacyFile, "export CHUNK_HOOK_ENABLE=1\n");

			const newEnvFile = join(testDir, "new-config", "chunk", "hook", "env");

			migrateEnvFile(newEnvFile, legacyFile);

			expect(existsSync(newEnvFile)).toBe(true);
			const content = readFileSync(newEnvFile, "utf-8");
			expect(content).toContain("export CHUNK_HOOK_ENABLE=1");

			// Legacy file should be moved (not copied)
			expect(existsSync(legacyFile)).toBe(false);
		});

		it("skips migration when new env file already exists", () => {
			const legacyDir = join(testDir, "legacy-config", "chunk-hook");
			const legacyFile = join(legacyDir, "env");
			mkdirSync(legacyDir, { recursive: true });
			writeFileSync(legacyFile, "export CHUNK_HOOK_ENABLE=0\n");

			const newDir = join(testDir, "new-config", "chunk", "hook");
			mkdirSync(newDir, { recursive: true });
			const newEnvFile = join(newDir, "env");
			writeFileSync(newEnvFile, "export CHUNK_HOOK_ENABLE=1\n");

			migrateEnvFile(newEnvFile, legacyFile);

			// New file should be unchanged
			const content = readFileSync(newEnvFile, "utf-8");
			expect(content).toContain("export CHUNK_HOOK_ENABLE=1");
			// Legacy should still exist (migration skipped)
			expect(existsSync(legacyFile)).toBe(true);
		});
	});
});
